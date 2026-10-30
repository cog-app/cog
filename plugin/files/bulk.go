package files

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// --- Move ---

// ErrNotFolder is a move whose destination is not a folder.
var ErrNotFolder = errors.New("the destination is not a folder")

// MoveFailure is an item Move left where it was, and why.
type MoveFailure struct {
	Path string
	Err  error
}

// Move moves each of paths into the folder destDir, keeping its name and,
// like Rename, its stable ID (so favourites and shares follow it). An item
// that cannot go there — the root, a folder into itself, one already in
// destDir, one whose name destDir already has — is reported in failures and
// the rest still move. err is for a destination that cannot be used at all.
func (p *Plugin) Move(username string, paths []string, destDir string) (moved []string, failures []MoveFailure, err error) {
	destDir = normalizePath(destDir)
	db, err := p.DB(username)
	if err != nil {
		return nil, nil, err
	}
	dest, err := getByPath(db, destDir)
	if err != nil {
		return nil, nil, err
	}
	if !dest.IsDir {
		return nil, nil, ErrNotFolder
	}
	// What is on disk counts as taken, not just what the metadata knows of:
	// a rename onto an unlisted file would replace it.
	if err := p.reconcile(db, username, destDir); err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", destDir, err)
	}
	rows, err := db.Query(`SELECT name FROM files WHERE parent_path = ?`, destDir)
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", destDir, err)
	}
	taken := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("list %s: %w", destDir, err)
		}
		taken[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", destDir, err)
	}

	for _, src := range paths {
		src = normalizePath(src)
		name := path.Base(src)
		fail := func(err error) { failures = append(failures, MoveFailure{Path: src, Err: err}) }
		switch {
		case src == "/":
			fail(errors.New("the root folder cannot be moved"))
			continue
		case destDir == src || strings.HasPrefix(destDir, src+"/"):
			fail(errors.New("a folder cannot be moved into itself"))
			continue
		case parentDir(src) == destDir:
			fail(errors.New("it is already there"))
			continue
		case taken[name]:
			fail(fmt.Errorf("“%s” already has an item with that name", dirTitle(destDir)))
			continue
		}
		if _, err := getByPath(db, src); err != nil {
			fail(err)
			continue
		}
		if _, err := p.Rename(username, src, path.Join(destDir, name)); err != nil {
			fail(err)
			continue
		}
		taken[name] = true
		moved = append(moved, src)
	}
	return moved, failures, nil
}

// ListDirs returns the folders directly inside dirPath, in listing order.
func (p *Plugin) ListDirs(username, dirPath string) ([]FileMeta, error) {
	return p.list(username, dirPath, true)
}

// --- Zip download ---

// zipEntry is one file or folder of a download archive, under the name it
// has inside the archive.
type zipEntry struct {
	name string
	meta FileMeta
}

// zipEntries lists what a zip of paths holds: each path under its own name,
// and everything inside a folder under the folder's name. It reads metadata
// only, so a large tree costs a row per entry, not its contents.
func (p *Plugin) zipEntries(username string, paths []string) ([]zipEntry, error) {
	var entries []zipEntry
	var walk func(prefix, dir string) error
	walk = func(prefix, dir string) error {
		items, err := p.List(username, dir)
		if err != nil {
			return err
		}
		for _, it := range items {
			name := prefix + "/" + it.Name
			entries = append(entries, zipEntry{name: name, meta: it})
			if it.IsDir {
				if err := walk(name, it.Path); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, sel := range paths {
		meta, err := p.GetByPath(username, sel)
		if err != nil {
			return nil, err
		}
		if meta.Path == "/" {
			return nil, errors.New("the root folder cannot be downloaded as a selection")
		}
		entries = append(entries, zipEntry{name: meta.Name, meta: *meta})
		if meta.IsDir {
			if err := walk(meta.Name, meta.Path); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}

// writeZip streams entries into a zip archive on w, one file at a time, so
// memory stays flat however large the files are. Media and archives, which
// do not compress, are stored rather than deflated.
func (p *Plugin) writeZip(username string, entries []zipEntry, w io.Writer) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Modified: time.Unix(e.meta.MTime, 0)}
		if e.meta.IsDir {
			hdr.Name += "/"
			hdr.SetMode(os.ModeDir | 0o755)
			if _, err := zw.CreateHeader(hdr); err != nil {
				return fmt.Errorf("zip %s: %w", e.name, err)
			}
			continue
		}
		hdr.Method = zip.Deflate
		if incompressible(e.meta.MimeType) {
			hdr.Method = zip.Store
		}
		hdr.SetMode(0o644)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("zip %s: %w", e.name, err)
		}
		rc, err := p.open(username, e.meta.Path)
		if err != nil {
			return fmt.Errorf("zip %s: %w", e.name, err)
		}
		_, err = io.Copy(fw, rc)
		if cerr := rc.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("zip %s: %w", e.name, err)
		}
	}
	return zw.Close()
}

// incompressible reports whether a type is already compressed, so deflating
// it again would cost time for nothing.
func incompressible(mimeType string) bool {
	switch {
	case strings.HasPrefix(mimeType, "image/") && mimeType != "image/svg+xml" && mimeType != "image/bmp":
		return true
	case strings.HasPrefix(mimeType, "video/"), strings.HasPrefix(mimeType, "audio/"):
		return true
	}
	return isArchiveMIME(mimeType)
}

// open opens a file's content for reading, without looking up its metadata.
func (p *Plugin) open(username, filePath string) (io.ReadCloser, error) {
	if fs := p.userFS(username); fs != nil {
		rc, err := fs.Get(filePath)
		if err != nil {
			return nil, fmt.Errorf("open file: %w", err)
		}
		return rc, nil
	}
	f, err := os.Open(filepath.Join(p.ctx.UserFilesDir(username), filePath))
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	return f, nil
}
