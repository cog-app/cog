package contacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// photoStore keeps one user's contact photos as files named by the SHA-256
// of their bytes, so a photo is stored once however many cards and versions
// use it, and writing the same one again costs nothing. The cards table
// names a card's photo by that hash; the vCard's PHOTO is regenerated from
// the file on read.
//
// Files live outside the database, so no transaction covers them. A write
// stores the file before the transaction that refers to it commits, and
// files nothing refers to are removed after it; Store serializes both
// (photoMu), so a file is never removed while a write that refers to it is
// still in flight.
type photoStore struct {
	dir string
	log *slog.Logger
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *photoStore) path(hash string) string { return filepath.Join(s.dir, hash) }

// put stores a photo and returns its hash.
func (s *photoStore) put(data []byte) (string, error) {
	hash := hashOf(data)
	if _, err := os.Stat(s.path(hash)); err == nil {
		return hash, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("store photo: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("store photo: %w", err)
	}
	f, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("store photo: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("store photo: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("store photo: %w", err)
	}
	if err := os.Rename(f.Name(), s.path(hash)); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("store photo: %w", err)
	}
	return hash, nil
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
