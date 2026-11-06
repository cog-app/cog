package pipeline

import (
	"fmt"
	"path"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Collect removes the media and captions in space that no download names
// any more: what a deleted subscription or item left, since their rows go by
// cascade, and what a worker stopped part way through kept. The files count
// toward the user's quota, so they must go when the feed is done with them.
// Programs' leftovers in the cache's working directories go too.
func Collect(store *feed.Store, space feed.Space) error {
	names, err := store.DownloadFiles()
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	kept := make(map[string]bool, len(names))
	for _, n := range names {
		kept[feed.Name(n)] = true
	}
	for _, dir := range []string{"media", "captions"} {
		entries, err := space.Own.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("collect %s: %w", dir, err)
		}
		for _, e := range entries {
			name := path.Join(dir, e.Name())
			if e.IsDir() || kept[name] {
				continue
			}
			if err := space.Own.Remove(name); err != nil {
				return fmt.Errorf("collect %s: %w", name, err)
			}
		}
	}
	return nil
}
