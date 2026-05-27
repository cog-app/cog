// Package assets serves embedded static files with content-hash-based cache
// busting. Hashes are computed once at construction; versioned URLs (built with
// URL) carry a ?v=<hash> query string so a far-future, immutable Cache-Control
// header never serves stale content after a deploy.
package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// hashLen is the number of hex characters of the SHA-256 used in URLs/ETags.
const hashLen = 12

// Assets serves files from an fs.FS with content-hash cache busting.
type Assets struct {
	mountPrefix string            // URL path the handler is mounted under, no trailing slash (e.g. "/static")
	hashes      map[string]string // relpath -> full sha256 hex
	data        map[string][]byte // relpath -> file bytes (assets are small; kept in memory)
	modTime     time.Time         // construction time, for Last-Modified
}

// New walks fsys, reading and hashing every file. mountPrefix is the URL path
// the handler will be mounted under (used only to build URLs via URL); it must
// not have a trailing slash. Returns an error if the walk or any read fails.
func New(fsys fs.FS, mountPrefix string) (*Assets, error) {
	a := &Assets{
		mountPrefix: strings.TrimSuffix(mountPrefix, "/"),
		hashes:      make(map[string]string),
		data:        make(map[string][]byte),
		modTime:     time.Now(),
	}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read asset %q: %w", p, err)
		}
		sum := sha256.Sum256(b)
		a.hashes[p] = hex.EncodeToString(sum[:])
		a.data[p] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk assets under %q: %w", mountPrefix, err)
	}
	return a, nil
}

// URL returns the versioned URL for name (a path relative to the FS root), e.g.
// "/static/style.css?v=1a2b3c4d5e6f". For an unknown name it returns the bare
// path and logs a warning, so a typo degrades to "uncached" rather than panics.
func (a *Assets) URL(name string) string {
	bare := a.mountPrefix + "/" + name
	h, ok := a.hashes[name]
	if !ok {
		slog.Warn("assets: URL for unknown asset", "name", name, "mount", a.mountPrefix)
		return bare
	}
	return bare + "?v=" + h[:hashLen]
}

// ServeHTTP serves the asset named by r.URL.Path. Mount it under
// http.StripPrefix(mountPrefix+"/", a) so r.URL.Path is the asset's relative
// path (which may include subdirectories, e.g. "vendor/x.js").
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if name == "." || strings.HasPrefix(name, "../") || name == ".." {
		http.NotFound(w, r)
		return
	}
	h, ok := a.hashes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := a.data[name]

	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("ETag", strconv.Quote(h[:hashLen]))
	if r.URL.Query().Get("v") == h[:hashLen] {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// No version (or a stale one): allow caching but force revalidation.
		// The ETag still yields a 304 when the content is unchanged.
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, a.modTime, bytes.NewReader(data))
}

// contentType returns the MIME type for an asset name. The platform mime table
// is unreliable for .js/.css, so those are set explicitly.
func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".js"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
