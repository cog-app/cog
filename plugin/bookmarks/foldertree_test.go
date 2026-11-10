package bookmarks

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestFolderTree(t *testing.T) {
	id := func(n int64) *int64 { return &n }
	// Siblings in their synced order, whatever their names.
	tree := folderTree([]Folder{
		{ID: 1, Title: "B top", SortOrder: 0},
		{ID: 2, Title: "A top", SortOrder: 1},
		{ID: 3, Title: "Child", ParentID: id(1)},
		{ID: 4, Title: "Grandchild", ParentID: id(3)},
		{ID: 5, Title: "Under A", ParentID: id(2)},
	})
	var paths []string
	for _, n := range tree {
		paths = append(paths, n.Path)
	}
	require.Equal(t, []string{"B top", "B top / Child", "B top / Child / Grandchild", "A top", "A top / Under A"}, paths)
	require.Equal(t, 2, tree[2].Depth)
	require.True(t, tree[1].HasChildren)
	require.False(t, tree[2].HasChildren)

	var crumbs []int64
	for _, a := range folderAncestors(tree, 4) {
		crumbs = append(crumbs, a.ID)
	}
	require.Equal(t, []int64{1, 3}, crumbs)
	require.Equal(t, 0, len(folderAncestors(tree, 1)))

	// The sidebar opens the way down to the active folder, and what it holds.
	var shown []int64
	for _, n := range sidebarFolders(tree, 3) {
		shown = append(shown, n.ID)
	}
	require.Equal(t, []int64{1, 3, 4, 2}, shown)
	shown = nil
	for _, n := range sidebarFolders(tree, 0) {
		shown = append(shown, n.ID)
	}
	require.Equal(t, []int64{1, 2}, shown)
}
