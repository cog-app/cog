package bookmarks

import (
	"cmp"
	"slices"
	"strings"
)

// folderNode is a folder in the order the UI shows the tree: each folder is
// followed by its subfolders, and siblings come in their synced order.
type folderNode struct {
	Folder
	// Depth is 0 for a folder at the top.
	Depth int
	// Path is the folder's name after its parents', to tell apart folders
	// of the same name in a flat list.
	Path        string
	HasChildren bool
}

// folderTree orders folders as a tree, depth first.
func folderTree(folders []Folder) []folderNode {
	byParent := map[int64][]Folder{}
	for _, f := range folders {
		parent := RootFolder
		if f.ParentID != nil {
			parent = *f.ParentID
		}
		byParent[parent] = append(byParent[parent], f)
	}
	for _, fs := range byParent {
		slices.SortFunc(fs, func(a, b Folder) int {
			return cmp.Or(cmp.Compare(a.SortOrder, b.SortOrder), cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)), cmp.Compare(a.ID, b.ID))
		})
	}
	out := make([]folderNode, 0, len(folders))
	var walk func(parent int64, depth int, path string)
	walk = func(parent int64, depth int, path string) {
		for _, f := range byParent[parent] {
			p := f.Title
			if path != "" {
				p = path + " / " + f.Title
			}
			out = append(out, folderNode{Folder: f, Depth: depth, Path: p, HasChildren: len(byParent[f.ID]) > 0})
			walk(f.ID, depth+1, p)
		}
	}
	walk(RootFolder, 0, "")
	return out
}

// folderAncestors returns the folders above id, the top one first.
func folderAncestors(tree []folderNode, id int64) []folderNode {
	byID := make(map[int64]folderNode, len(tree))
	for _, n := range tree {
		byID[n.ID] = n
	}
	var out []folderNode
	for n, ok := byID[id]; ok && n.ParentID != nil; {
		if n, ok = byID[*n.ParentID]; ok {
			out = append(out, n)
		}
	}
	slices.Reverse(out)
	return out
}

// subfolders returns the folders directly inside id, in order.
func subfolders(tree []folderNode, id int64) []folderNode {
	var out []folderNode
	for _, n := range tree {
		if n.ParentID != nil && *n.ParentID == id {
			out = append(out, n)
		}
	}
	return out
}

// sidebarFolders is what of the tree the sidebar shows: the folders at the
// top, and opened below them the way down to the active folder and what that
// holds. A whole synced tree would bury the sidebar's other links.
func sidebarFolders(tree []folderNode, active int64) []folderNode {
	open := map[int64]bool{active: true}
	for _, a := range folderAncestors(tree, active) {
		open[a.ID] = true
	}
	var out []folderNode
	for _, n := range tree {
		if n.ParentID == nil || open[*n.ParentID] {
			out = append(out, n)
		}
	}
	return out
}

// depthClass indents a sidebar folder by its depth, up to a limit past which
// the sidebar would be too narrow for the names.
func depthClass(depth int) string {
	switch {
	case depth <= 0:
		return "sidebar-item"
	case depth > 4:
		depth = 4
	}
	return "sidebar-item bm-depth-" + string(rune('0'+depth))
}
