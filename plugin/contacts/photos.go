package contacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// photoStore keeps one user's contact photos as files named by the SHA-256
// of their bytes, so a photo is stored once however many cards and versions
// use it, and writing the same one again costs nothing. The cards table
// names a card's photo by that hash; the vCard's PHOTO is regenerated from
// the file on read.
//
// Files live outside the database, so no transaction covers them. A write
// stores the file while its transaction holds the database's write lock, and
// files no card refers to are removed under that lock too (Store.collect), so
// a file is never removed while a write that refers to it is in flight.
type photoStore struct {
	dir string
	log *slog.Logger
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *photoStore) path(hash string) string { return filepath.Join(s.dir, hash) }

// urlHash names the cached copy of a photo given by URL.
func urlHash(uri string) string { return "u-" + hashOf([]byte(uri)) }

// put stores a photo under its hash (hashOf).
func (s *photoStore) put(hash string, data []byte) error {
	if _, err := os.Stat(s.path(hash)); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("store photo: %w", err)
	}
	return s.write(hash, data)
}

// write replaces the file named hash with data.
func (s *photoStore) write(hash string, data []byte) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("store photo: %w", err)
	}
	f, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("store photo: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return fmt.Errorf("store photo: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("store photo: %w", err)
	}
	if err := os.Rename(f.Name(), s.path(hash)); err != nil {
		os.Remove(f.Name())
		return fmt.Errorf("store photo: %w", err)
	}
	return nil
}

// Remote photos: a card's PHOTO may be a URL (Google exports, FullContact).
// The web UI shows it through Cog, fetched with safehttp so a URL cannot
// reach the server's own network, and cached for a day under urlHash.

const (
	remotePhotoTTL = 24 * time.Hour
	maxPhotoSize   = 5 << 20
)

// remoteError is a photo URL that could not be fetched: the remote's
// problem, not the server's.
type remoteError struct{ err error }

func (e *remoteError) Error() string { return "fetch contact photo: " + e.err.Error() }
func (e *remoteError) Unwrap() error { return e.err }

// remote returns the photo at uri, from the cache when it is fresh. A stale
// copy is served when fetching fails.
func (s *photoStore) remote(ctx context.Context, client *http.Client, hash, uri string) ([]byte, error) {
	path := s.path(hash)
	cached, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read photo %s: %w", hash, err)
	}
	if cached != nil {
		if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < remotePhotoTTL {
			return cached, nil
		}
	}
	data, err := fetchPhoto(ctx, client, uri)
	if err != nil {
		if cached != nil {
			s.log.Warn("contact photo refresh failed; serving the cached copy", "url", uri, "err", err)
			return cached, nil
		}
		return nil, &remoteError{err}
	}
	if err := s.write(hash, data); err != nil {
		return nil, err
	}
	return data, nil
}

func fetchPhoto(ctx context.Context, client *http.Client, uri string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", uri, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPhotoSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", uri, maxPhotoSize)
	}
	if !isImage(data) {
		return nil, fmt.Errorf("%s: not an image", uri)
	}
	return data, nil
}

// isImage reports whether data sniffs as a raster image. Photos are served
// from Cog's origin, so anything else (HTML, SVG with script) is refused
// rather than trusted to a type a client wrote.
func isImage(data []byte) bool {
	return strings.HasPrefix(http.DetectContentType(data), "image/")
}

// read returns a photo's bytes. A missing file is logged and read as no
// photo, so one lost file does not make the card unreadable.
func (s *photoStore) read(hash string) ([]byte, error) {
	data, err := os.ReadFile(s.path(hash))
	if errors.Is(err, fs.ErrNotExist) {
		s.log.Error("contact photo missing", "hash", hash)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read photo %s: %w", hash, err)
	}
	return data, nil
}

// collect removes the files of photos no card refers to any more.
func (s *photoStore) collect(q davsql.Querier, hashes ...string) error {
	for _, h := range hashes {
		if h == "" {
			continue
		}
		var used bool
		if err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM cards WHERE photo_hash = ?)`, h).Scan(&used); err != nil {
			return fmt.Errorf("check photo %s: %w", h, err)
		}
		if used {
			continue
		}
		if err := os.Remove(s.path(h)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove photo %s: %w", h, err)
		}
	}
	return nil
}
