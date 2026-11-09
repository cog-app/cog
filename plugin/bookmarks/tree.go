package bookmarks

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ChildType says whether a folder's child is a folder or a bookmark. A
// bookmark in several folders is a child of each.
type ChildType string

const (
	ChildFolder   ChildType = "folder"
	ChildBookmark ChildType = "bookmark"
)

// ChildRef names one child of a folder.
type ChildRef struct {
	Type ChildType
	ID   int64
}

type treeChild struct {
	ChildRef
	order int64
}

// Tree is the folders under one folder, and the bookmarks of as many levels
// of them as were asked for. It is read with a handful of queries however
// large it is, so walking it costs no further round-trips.
type Tree struct {
	Root      int64
	folders   map[int64]*Folder
	children  map[int64][]treeChild
	bookmarks map[int64]*Bookmark
	// loaded is the folders whose bookmarks were read.
	loaded map[int64]bool
}

// LoadTree reads the folder root and everything under it. Bookmarks are read
// for root and the folders fewer than layers levels below it; layers < 0 is
// every level. A root that does not exist is ErrFolderNotFound.
func LoadTree(db *sql.DB, root int64, layers int) (*Tree, error) {
	t := &Tree{
		Root:      root,
		folders:   map[int64]*Folder{},
		children:  map[int64][]treeChild{},
		bookmarks: map[int64]*Bookmark{},
		loaded:    map[int64]bool{},
	}
	rows, err := db.Query(`SELECT id, parent_id, title, created_at, updated_at, sort_order FROM folders`)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Title, &f.CreatedAt, &f.UpdatedAt, &f.SortOrder); err != nil {
			return nil, fmt.Errorf("scan folder: %w", err)
		}
		t.folders[f.ID] = &f
		parent := RootFolder
		if f.ParentID != nil {
			parent = *f.ParentID
		}
		t.children[parent] = append(t.children[parent], treeChild{ChildRef{ChildFolder, f.ID}, f.SortOrder})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	if root != RootFolder && t.folders[root] == nil {
		return nil, ErrFolderNotFound
	}

	var want []int64
	var walk func(id int64, depth int)
	walk = func(id int64, depth int) {
		if layers >= 0 && depth >= layers {
			return
		}
		want = append(want, id)
		t.loaded[id] = true
		for _, c := range t.children[id] {
			if c.Type == ChildFolder {
				walk(c.ID, depth+1)
			}
		}
	}
	walk(root, 0)
	if err := t.loadBookmarks(db, want); err != nil {
		return nil, err
	}

	for id, cs := range t.children {
		slices.SortFunc(cs, func(a, b treeChild) int {
			// Folders first among equals: before child order existed every
			// folder sat at 0, and they were listed above the bookmarks.
			return cmp.Or(cmp.Compare(a.order, b.order), cmp.Compare(b.Type, a.Type), cmp.Compare(a.ID, b.ID))
		})
		t.children[id] = cs
	}
	return t, nil
}

// loadBookmarks reads the bookmarks in folders, each with its tags and all of
// its folders.
func (t *Tree) loadBookmarks(db *sql.DB, folders []int64) error {
	ids, err := json.Marshal(folders)
	if err != nil {
		return fmt.Errorf("encode folder ids: %w", err)
	}
	rows, err := db.Query(`SELECT bf.folder_id, bf.sort_order, b.id, b.url, b.title, b.description, b.click_count, b.available, b.created_at, b.updated_at
		FROM bookmark_folders bf JOIN bookmarks b ON b.id = bf.bookmark_id
		WHERE bf.folder_id IN (SELECT value FROM json_each(?))`, string(ids))
	if err != nil {
		return fmt.Errorf("list folder bookmarks: %w", err)
	}
	defer rows.Close()
	var bms []Bookmark
	for rows.Next() {
		var folder, order int64
		var bm Bookmark
		if err := rows.Scan(&folder, &order, &bm.ID, &bm.URL, &bm.Title, &bm.Description, &bm.ClickCount, &bm.Available, &bm.CreatedAt, &bm.UpdatedAt); err != nil {
			return fmt.Errorf("scan bookmark: %w", err)
		}
		t.children[folder] = append(t.children[folder], treeChild{ChildRef{ChildBookmark, bm.ID}, order})
		if _, seen := t.bookmarks[bm.ID]; !seen {
			t.bookmarks[bm.ID] = nil
			bms = append(bms, bm)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list folder bookmarks: %w", err)
	}
	if err := loadRelationsBatch(db, bms, true); err != nil {
		return err
	}
	for i := range bms {
		t.bookmarks[bms[i].ID] = &bms[i]
	}
	return nil
}

// Children lists a folder's children in order. A folder whose bookmarks were
// not read lists only its subfolders.
func (t *Tree) Children(folder int64) []ChildRef {
	cs := t.children[folder]
	out := make([]ChildRef, len(cs))
	for i, c := range cs {
		out[i] = c.ChildRef
	}
	return out
}

// Loaded reports whether a folder's bookmarks were read.
func (t *Tree) Loaded(folder int64) bool { return t.loaded[folder] }

// Folder returns a folder in the tree, or nil.
func (t *Tree) Folder(id int64) *Folder { return t.folders[id] }

// Bookmark returns a bookmark in the tree, or nil.
func (t *Tree) Bookmark(id int64) *Bookmark { return t.bookmarks[id] }

// ErrChildOrder is a new order that does not list exactly a folder's children.
var ErrChildOrder = errors.New("the order must list each child of the folder once")

// SetChildOrder puts a folder's children in the given order, which must name
// each of them exactly once.
func SetChildOrder(db *sql.DB, folder int64, order []ChildRef) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin child order: %w", err)
	}
	defer tx.Rollback()
	if err := folderExists(tx, folder); err != nil {
		return err
	}

	rows, err := tx.Query(`SELECT 'folder', id FROM folders WHERE parent_id IS ?
		UNION ALL SELECT 'bookmark', bookmark_id FROM bookmark_folders WHERE folder_id = ?`, parentArg(folder), folder)
	if err != nil {
		return fmt.Errorf("list children: %w", err)
	}
	have := map[ChildRef]bool{}
	for rows.Next() {
		var c ChildRef
		if err := rows.Scan(&c.Type, &c.ID); err != nil {
			rows.Close()
			return fmt.Errorf("scan child: %w", err)
		}
		have[c] = false
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list children: %w", err)
	}
	if len(order) != len(have) {
		return ErrChildOrder
	}
	for _, c := range order {
		if seen, ok := have[c]; !ok || seen {
			return ErrChildOrder
		}
		have[c] = true
	}

	for i, c := range order {
		var err error
		if c.Type == ChildFolder {
			_, err = tx.Exec(`UPDATE folders SET sort_order = ? WHERE id = ?`, i, c.ID)
		} else {
			_, err = tx.Exec(`UPDATE bookmark_folders SET sort_order = ? WHERE bookmark_id = ? AND folder_id = ?`, i, c.ID, folder)
		}
		if err != nil {
			return fmt.Errorf("order %s %d: %w", c.Type, c.ID, err)
		}
	}
	return tx.Commit()
}

// AddToFolder puts a bookmark at the end of a folder. It is sql.ErrNoRows if
// the bookmark does not exist and ErrFolderNotFound if the folder does not.
func AddToFolder(db *sql.DB, bookmarkID, folder int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin add to folder: %w", err)
	}
	defer tx.Rollback()
	var ok bool
	if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM bookmarks WHERE id = ?)`, bookmarkID).Scan(&ok); err != nil {
		return fmt.Errorf("find bookmark: %w", err)
	}
	if !ok {
		return sql.ErrNoRows
	}
	if err := linkBookmark(tx, bookmarkID, folder); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveFromFolder takes a bookmark out of a folder, deleting it when that was
// the last folder it was in. It is sql.ErrNoRows if the bookmark was not in
// the folder.
func RemoveFromFolder(db *sql.DB, bookmarkID, folder int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin remove from folder: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM bookmark_folders WHERE bookmark_id = ? AND folder_id = ?`, bookmarkID, folder)
	if err != nil {
		return fmt.Errorf("remove from folder: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("remove from folder: %w", err)
	} else if n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM bookmarks WHERE id = ? AND NOT EXISTS (SELECT 1 FROM bookmark_folders WHERE bookmark_id = ?)`, bookmarkID, bookmarkID); err != nil {
		return fmt.Errorf("delete unfiled bookmark: %w", err)
	}
	return tx.Commit()
}

// SyncLockTTL is how long a sync client holds the lock without renewing it.
// Floccus renews every two minutes, so a client that crashed mid-sync leaves
// it held no longer than that.
const SyncLockTTL = 2 * time.Minute

// AcquireSyncLock takes the user's sync lock until now+SyncLockTTL, reporting
// false if another sync holds it. The lock lives in the user's database so
// that every replica sees it.
func AcquireSyncLock(db *sql.DB, now time.Time) (bool, error) {
	res, err := db.Exec(`INSERT INTO sync_lock (id, expires_at) VALUES (1, ?)
		ON CONFLICT (id) DO UPDATE SET expires_at = excluded.expires_at WHERE sync_lock.expires_at <= ?`,
		now.Add(SyncLockTTL).Unix(), now.Unix())
	if err != nil {
		return false, fmt.Errorf("acquire sync lock: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire sync lock: %w", err)
	}
	return n > 0, nil
}

// ReleaseSyncLock frees the user's sync lock, held or not.
func ReleaseSyncLock(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM sync_lock`); err != nil {
		return fmt.Errorf("release sync lock: %w", err)
	}
	return nil
}
