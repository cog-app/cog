package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"git.duckfam.us/jj-software/cog/vfs"
)

// legacy is where a plugin kept files before it had storage, each moved on
// first use to name within its directory. The paths are the old layout's,
// which nothing but this move should know.
type legacy struct {
	pluginID string
	kind     Kind
	old      func(username string) string // relative to the data dir
	to       string                       // name within the new directory
	drop     string                       // prefix of moved names to delete: cached copies
}

var legacies = []legacy{
	{pluginID: "calendar", kind: Counted, to: "attachments",
		old: func(u string) string { return filepath.Join("calendar", u, "attachments") }},
	// Cached copies of photos given by URL lived among the user's own; they
	// are dropped rather than counted, and fetched again into the cache.
	{pluginID: "contacts", kind: Counted, to: "photos", drop: "u-",
		old: func(u string) string { return filepath.Join("contacts", u, "photos") }},
	{pluginID: "recipes", kind: Counted, to: "images",
		old: func(u string) string { return filepath.Join(u, "app_data", "recipes", "images") }},
	{pluginID: "profanity", kind: Counted, to: "words.txt",
		old: func(u string) string { return filepath.Join(u, "profanity.txt") }},
	// Feed rows recorded absolute paths into these; the feed resolves them
	// by base name in their new directories (feed.Name).
	{pluginID: "feed", kind: Counted, to: "media",
		old: func(u string) string { return filepath.Join(u, "app_data", "feed", "media") }},
	{pluginID: "feed", kind: Counted, to: "captions",
		old: func(u string) string { return filepath.Join(u, "app_data", "feed", "captions") }},
	{pluginID: "feed", kind: Cache, to: "whisper-models",
		old: func(u string) string { return filepath.Join(u, "app_data", "feed", "whisper-models") }},
	{pluginID: "gallery", kind: Global, to: "onnx_models",
		old: func(string) string { return "onnx_models" }},
	{pluginID: "gallery", kind: Cache, to: "video_thumbs",
		old: func(u string) string { return filepath.Join(u, "app_data", "gallery", "video_thumbs") }},
}

// migrate moves what d's plugin kept at an old location into d, once: a
// destination that exists already is left alone, and so is the source.
func (s *Store) migrate(d *Dir) error {
	for _, l := range legacies {
		if l.pluginID != d.pluginID || l.kind != d.kind {
			continue
		}
		if err := s.move(d, l); err != nil {
			return fmt.Errorf("storage: move %s data of %s: %w", d.pluginID, d.username, err)
		}
	}
	return nil
}

func (s *Store) move(d *Dir, l legacy) error {
	data, err := os.OpenRoot(s.dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer data.Close()
	old := l.old(d.username)
	if _, err := data.Lstat(old); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	to := filepath.Join(d.rel, l.to)
	if _, err := data.Lstat(to); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := data.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if err := data.Rename(old, to); err != nil {
		return err
	}
	if l.drop != "" {
		entries, err := fs.ReadDir(data.FS(), filepath.ToSlash(to))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), l.drop) {
				if err := data.Remove(filepath.Join(to, e.Name())); err != nil {
					return err
				}
			}
		}
	}
	// The plugin's directory under the data dir is left once empty.
	if parent := filepath.Dir(old); parent != d.username && parent != "." {
		if err := data.Remove(parent); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
			return err
		}
	}
	return nil
}

func isNotEmpty(err error) bool {
	return errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST)
}

// MoveLegacyTrash moves a user's trash from where the files plugin kept it
// before trash lived in each mount (beside their files directory) into the
// root of filesDir, their root mount's directory, where the plugin now looks
// for what it trashed from the root. Once per user per process.
func (s *Store) MoveLegacyTrash(username, filesDir string) error {
	if !validID(username) {
		return fmt.Errorf("storage: invalid user %q", username)
	}
	key := readyKey{Host, ".trash", username}
	s.mu.Lock()
	done := s.ready[key]
	s.mu.Unlock()
	if done {
		return nil
	}
	old := filepath.Join(s.dataDir, username, "trash")
	to := filepath.Join(filesDir, vfs.TrashDir)
	if _, err := os.Lstat(old); err == nil {
		if _, err := os.Lstat(to); errors.Is(err, fs.ErrNotExist) {
			if err := os.Rename(old, to); err != nil {
				return fmt.Errorf("storage: move trash of %s: %w", username, err)
			}
		} else if err != nil {
			return fmt.Errorf("storage: move trash of %s: %w", username, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("storage: move trash of %s: %w", username, err)
	}
	s.mu.Lock()
	s.ready[key] = true
	s.mu.Unlock()
	return nil
}
