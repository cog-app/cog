package pipeline

import (
	"fmt"
	"path"
	"strconv"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Collect removes the media and captions in space that no download names
// any more: what a deleted subscription or item left, since their rows go by
// cascade, and what a worker stopped part way through kept. The files count
// toward the user's quota, so they must go when the feed is done with them.
// Programs' leftovers in the cache's working directories go too, and so do
// edges no item uses.
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
	return collectEdges(store, space)
}

// collectEdges removes the edges in the cache of items that are no longer
// packaged, and of every edit but the one an item's edges are recorded for.
// It runs between the edges worker's runs, so no edit is being made.
func collectEdges(store *feed.Store, space feed.Space) error {
	kept, err := store.PackagedEdges()
	if err != nil {
		return fmt.Errorf("collect edges: %w", err)
	}
	items, err := space.Cache.ReadDir("edges")
	if err != nil {
		return fmt.Errorf("collect edges: %w", err)
	}
	for _, item := range items {
		dir := path.Join("edges", item.Name())
		id, err := strconv.ParseInt(item.Name(), 10, 64)
		edit, packaged := kept[id]
		if err != nil || !packaged || edit == "" || !item.IsDir() {
			if err := space.Cache.RemoveAll(dir); err != nil {
				return fmt.Errorf("collect %s: %w", dir, err)
			}
			continue
		}
		edits, err := space.Cache.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("collect %s: %w", dir, err)
		}
		for _, e := range edits {
			if e.Name() == edit {
				continue
			}
			if err := space.Cache.RemoveAll(path.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("collect %s: %w", path.Join(dir, e.Name()), err)
			}
		}
	}
	return nil
}
