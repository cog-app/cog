package bookmarks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mattn/go-sqlite3"
)

// Bookmark represents a saved URL with metadata.
type Bookmark struct {
	ID          int64
	URL         string
	Title       string
	Description string
	ClickCount  int
	Available   bool
	CreatedAt   int64
	UpdatedAt   int64
	Tags        []string
	Folders     []int64
}

// Folder represents a hierarchical bookmark container.
type Folder struct {
	ID        int64
	ParentID  *int64
	Title     string
	CreatedAt int64
	UpdatedAt int64
	// SortOrder is the folder's place among its parent's children.
	SortOrder int64
}

// RootFolder is the id of the folder every other folder descends from. It has
// no row: a folder at the top has a NULL parent_id, and a bookmark at the top a
// bookmark_folders row with this folder_id.
const RootFolder int64 = -1

var (
	ErrDuplicateURL   = errors.New("another bookmark has that url")
	ErrFolderNotFound = errors.New("folder not found")
	ErrFolderCycle    = errors.New("a folder cannot move into itself")
)

// nextOrderSQL is the sort_order that places a new child after everything in
// a folder. It takes the folder's parent_id (nil for the root) and its id.
const nextOrderSQL = `SELECT COALESCE(MAX(o), -1) + 1 FROM (
	SELECT sort_order AS o FROM folders WHERE parent_id IS ?
	UNION ALL SELECT sort_order FROM bookmark_folders WHERE folder_id = ?)`

// parentArg is folder as a parent_id: NULL for the root.
func parentArg(folder int64) any {
	if folder == RootFolder {
		return nil
	}
	return folder
}

// --- Bookmark CRUD ---

func ListBookmarks(db *sql.DB, folderID *int64, tags []string, search string, sortBy string, page, limit int) ([]Bookmark, int, error) {
	if limit <= 0 {
		limit = 10
	}
	// Page -1 is every match, as in NextCloud Bookmarks.
	offset := page * limit
	if page < 0 {
		limit, offset = -1, 0
	}

	where := []string{"1=1"}
	args := []any{}

	if folderID != nil {
		where = append(where, "b.id IN (SELECT bookmark_id FROM bookmark_folders WHERE folder_id = ?)")
		args = append(args, *folderID)
	}
	for _, t := range tags {
		where = append(where, "b.id IN (SELECT bookmark_id FROM tags WHERE tag = ?)")
		args = append(args, t)
	}
	if search != "" {
		where = append(where, "(b.title LIKE ? OR b.url LIKE ? OR b.description LIKE ?)")
		pat := "%" + search + "%"
		args = append(args, pat, pat, pat)
	}

	whereClause := strings.Join(where, " AND ")

	// Count total.
	var total int
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	if err := db.QueryRow("SELECT COUNT(*) FROM bookmarks b WHERE "+whereClause, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Every order ends on the id, so rows that tie (created in the same
	// second, say) still come back in the same order every time.
	order := "b.updated_at DESC, b.id DESC"
	switch sortBy {
	case "title":
		order = "b.title COLLATE NOCASE ASC, b.id ASC"
	case "clickcount":
		order = "b.click_count DESC, b.id DESC"
	case "url":
		order = "b.url ASC, b.id ASC"
	case "added":
		order = "b.created_at DESC, b.id DESC"
	}

	query := fmt.Sprintf("SELECT b.id, b.url, b.title, b.description, b.click_count, b.available, b.created_at, b.updated_at FROM bookmarks b WHERE %s ORDER BY %s LIMIT ? OFFSET ?", whereClause, order)
	args = append(args, limit, offset)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var bookmarks []Bookmark
	for rows.Next() {
		var bm Bookmark
		if err := rows.Scan(&bm.ID, &bm.URL, &bm.Title, &bm.Description, &bm.ClickCount, &bm.Available, &bm.CreatedAt, &bm.UpdatedAt); err != nil {
			return nil, 0, err
		}
		bookmarks = append(bookmarks, bm)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	if err := loadRelationsBatch(db, bookmarks, true); err != nil {
		return nil, 0, err
	}
	return bookmarks, total, nil
}

func GetBookmark(db *sql.DB, id int64) (*Bookmark, error) {
	var bm Bookmark
	err := db.QueryRow(
		`SELECT id, url, title, description, click_count, available, created_at, updated_at FROM bookmarks WHERE id = ?`, id,
	).Scan(&bm.ID, &bm.URL, &bm.Title, &bm.Description, &bm.ClickCount, &bm.Available, &bm.CreatedAt, &bm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := loadRelations(db, &bm); err != nil {
		return nil, err
	}
	return &bm, nil
}

func GetBookmarkByURL(db *sql.DB, url string) (*Bookmark, error) {
	var bm Bookmark
	err := db.QueryRow(
		`SELECT id, url, title, description, click_count, available, created_at, updated_at FROM bookmarks WHERE url = ?`, url,
	).Scan(&bm.ID, &bm.URL, &bm.Title, &bm.Description, &bm.ClickCount, &bm.Available, &bm.CreatedAt, &bm.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := loadRelations(db, &bm); err != nil {
		return nil, err
	}
	return &bm, nil
}

// CreateBookmark upserts a bookmark. If a bookmark with the same URL exists, it updates it.
func CreateBookmark(db *sql.DB, url, title, description string, tags []string, folders []int64) (*Bookmark, error) {
	existing, err := GetBookmarkByURL(db, url)
	if err == nil {
		// Upsert: update existing.
		return UpdateBookmark(db, existing.ID, nil, &title, &description, tags, folders)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find bookmark by url: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin create bookmark: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO bookmarks (url, title, description) VALUES (?, ?, ?)`, url, title, description)
	if err != nil {
		return nil, fmt.Errorf("insert bookmark: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("insert bookmark id: %w", err)
	}
	if err := setTags(tx, id, tags); err != nil {
		return nil, err
	}
	if err := setFolders(tx, id, folders); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bookmark: %w", err)
	}
	return GetBookmark(db, id)
}

// UpdateBookmark changes the fields that are not nil. A URL another bookmark
// already has is ErrDuplicateURL.
func UpdateBookmark(db *sql.DB, id int64, url, title, description *string, tags []string, folders []int64) (*Bookmark, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin update bookmark: %w", err)
	}
	defer tx.Rollback()
	if url != nil {
		if _, err := tx.Exec(`UPDATE bookmarks SET url = ?, updated_at = unixepoch() WHERE id = ?`, *url, id); err != nil {
			var se sqlite3.Error
			if errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique {
				return nil, ErrDuplicateURL
			}
			return nil, fmt.Errorf("update bookmark url: %w", err)
		}
	}
	if title != nil {
		if _, err := tx.Exec(`UPDATE bookmarks SET title = ?, updated_at = unixepoch() WHERE id = ?`, *title, id); err != nil {
			return nil, fmt.Errorf("update bookmark title: %w", err)
		}
	}
	if description != nil {
		if _, err := tx.Exec(`UPDATE bookmarks SET description = ?, updated_at = unixepoch() WHERE id = ?`, *description, id); err != nil {
			return nil, fmt.Errorf("update bookmark description: %w", err)
		}
	}
	if tags != nil {
		if err := setTags(tx, id, tags); err != nil {
			return nil, err
		}
	}
	if folders != nil {
		if err := setFolders(tx, id, folders); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bookmark: %w", err)
	}
	return GetBookmark(db, id)
}

func DeleteBookmark(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM bookmarks WHERE id = ?`, id)
	return err
}

func RecordClick(db *sql.DB, url string) error {
	_, err := db.Exec(`UPDATE bookmarks SET click_count = click_count + 1, updated_at = unixepoch() WHERE url = ?`, url)
	return err
}

func RecentBookmarks(db *sql.DB, limit int) ([]Bookmark, error) {
	rows, err := db.Query(
		`SELECT id, url, title, description, click_count, available, created_at, updated_at FROM bookmarks ORDER BY updated_at DESC, id DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bookmarks []Bookmark
	for rows.Next() {
		var bm Bookmark
		if err := rows.Scan(&bm.ID, &bm.URL, &bm.Title, &bm.Description, &bm.ClickCount, &bm.Available, &bm.CreatedAt, &bm.UpdatedAt); err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, bm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := loadRelationsBatch(db, bookmarks, false); err != nil {
		return nil, err
	}
	return bookmarks, nil
}

// --- Folder CRUD ---

func ListFolders(db *sql.DB) ([]Folder, error) {
	rows, err := db.Query(`SELECT id, parent_id, title, created_at, updated_at, sort_order FROM folders ORDER BY title COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folders []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Title, &f.CreatedAt, &f.UpdatedAt, &f.SortOrder); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func GetFolder(db *sql.DB, id int64) (*Folder, error) {
	var f Folder
	err := db.QueryRow(
		`SELECT id, parent_id, title, created_at, updated_at, sort_order FROM folders WHERE id = ?`, id,
	).Scan(&f.ID, &f.ParentID, &f.Title, &f.CreatedAt, &f.UpdatedAt, &f.SortOrder)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// CreateFolder adds a folder after everything already in its parent, or at
// the top when parentID is nil. A parent that does not exist is
// ErrFolderNotFound.
func CreateFolder(db *sql.DB, title string, parentID *int64) (*Folder, error) {
	parent := RootFolder
	if parentID != nil {
		parent = *parentID
	}
	if err := folderExists(db, parent); err != nil {
		return nil, err
	}
	var id int64
	err := db.QueryRow(`INSERT INTO folders (title, parent_id, sort_order) VALUES (?, ?, (`+nextOrderSQL+`)) RETURNING id`,
		title, parentArg(parent), parentArg(parent), parent).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("insert folder: %w", err)
	}
	return GetFolder(db, id)
}

// UpdateFolder renames a folder and puts it under parentID (nil for the top).
// A folder that changes parent goes after everything in its new one; moving
// it into itself or one of its own subfolders is ErrFolderCycle.
func UpdateFolder(db *sql.DB, id int64, title string, parentID *int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin update folder: %w", err)
	}
	defer tx.Rollback()

	var cur sql.NullInt64
	if err := tx.QueryRow(`SELECT parent_id FROM folders WHERE id = ?`, id).Scan(&cur); err != nil {
		return err
	}
	parent := RootFolder
	if parentID != nil {
		parent = *parentID
	}
	was := RootFolder
	if cur.Valid {
		was = cur.Int64
	}
	if parent == was {
		if _, err := tx.Exec(`UPDATE folders SET title = ?, updated_at = unixepoch() WHERE id = ?`, title, id); err != nil {
			return fmt.Errorf("rename folder: %w", err)
		}
		return tx.Commit()
	}

	if err := folderExists(tx, parent); err != nil {
		return err
	}
	if parent != RootFolder {
		var inside bool
		if err := tx.QueryRow(`WITH RECURSIVE sub(id) AS (SELECT ? UNION ALL SELECT f.id FROM folders f JOIN sub ON f.parent_id = sub.id)
			SELECT EXISTS (SELECT 1 FROM sub WHERE id = ?)`, id, parent).Scan(&inside); err != nil {
			return fmt.Errorf("check folder move: %w", err)
		}
		if inside {
			return ErrFolderCycle
		}
	}
	if _, err := tx.Exec(`UPDATE folders SET title = ?, parent_id = ?, sort_order = (`+nextOrderSQL+`), updated_at = unixepoch() WHERE id = ?`,
		title, parentArg(parent), parentArg(parent), parent, id); err != nil {
		return fmt.Errorf("move folder: %w", err)
	}
	return tx.Commit()
}

// DeleteFolder removes a folder and its subfolders. A bookmark that was only
// in those folders moves to the root folder.
func DeleteFolder(db *sql.DB, id int64) error {
	return deleteFolder(db, id, true)
}

// DeleteFolderTree removes a folder, its subfolders, and the bookmarks that
// were in no other folder, as NextCloud Bookmarks does. A sync client that
// deletes a folder expects its contents to go with it; kept at the root, they
// would sync back as new bookmarks.
func DeleteFolderTree(db *sql.DB, id int64) error {
	return deleteFolder(db, id, false)
}

func deleteFolder(db *sql.DB, id int64, keepBookmarks bool) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin delete folder: %w", err)
	}
	defer tx.Rollback()

	var sub string
	if err := tx.QueryRow(`WITH RECURSIVE sub(id) AS (SELECT ? UNION ALL SELECT f.id FROM folders f JOIN sub ON f.parent_id = sub.id)
		SELECT json_group_array(id) FROM sub`, id).Scan(&sub); err != nil {
		return fmt.Errorf("find subfolders: %w", err)
	}
	rows, err := tx.Query(`DELETE FROM bookmark_folders WHERE folder_id IN (SELECT value FROM json_each(?)) RETURNING bookmark_id`, sub)
	if err != nil {
		return fmt.Errorf("unlink bookmarks: %w", err)
	}
	var unlinked []int64
	for rows.Next() {
		var b int64
		if err := rows.Scan(&b); err != nil {
			rows.Close()
			return fmt.Errorf("unlink bookmarks: %w", err)
		}
		unlinked = append(unlinked, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("unlink bookmarks: %w", err)
	}

	for _, b := range unlinked {
		var linked bool
		if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM bookmark_folders WHERE bookmark_id = ?)`, b).Scan(&linked); err != nil {
			return fmt.Errorf("check bookmark folders: %w", err)
		}
		switch {
		case linked:
		case keepBookmarks:
			if err := linkBookmark(tx, b, RootFolder); err != nil {
				return err
			}
		default:
			if _, err := tx.Exec(`DELETE FROM bookmarks WHERE id = ?`, b); err != nil {
				return fmt.Errorf("delete bookmark: %w", err)
			}
		}
	}
	if _, err := tx.Exec(`DELETE FROM folders WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	return tx.Commit()
}

// folderExists is nil for the root and any folder with a row, and
// ErrFolderNotFound otherwise.
func folderExists(q queryer, id int64) error {
	if id == RootFolder {
		return nil
	}
	var ok bool
	if err := q.QueryRow(`SELECT EXISTS (SELECT 1 FROM folders WHERE id = ?)`, id).Scan(&ok); err != nil {
		return fmt.Errorf("find folder: %w", err)
	}
	if !ok {
		return ErrFolderNotFound
	}
	return nil
}

// queryer is a *sql.DB or a *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// FolderCounts returns how many bookmarks each folder holds, by folder id.
func FolderCounts(db *sql.DB) (map[int64]int, error) {
	rows, err := db.Query(`SELECT folder_id, COUNT(*) FROM bookmark_folders GROUP BY folder_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

// --- Tag Operations ---

func ListAllTags(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT tag FROM tags ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// TagCount is a tag and the number of bookmarks that carry it.
type TagCount struct {
	Tag   string
	Count int
}

// ListTagCounts returns every tag with its number of bookmarks, by name.
func ListTagCounts(db *sql.DB) ([]TagCount, error) {
	rows, err := db.Query(`SELECT tag, COUNT(*) FROM tags GROUP BY tag ORDER BY tag COLLATE NOCASE, tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []TagCount
	for rows.Next() {
		var t TagCount
		if err := rows.Scan(&t.Tag, &t.Count); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// RenameTag renames a tag on every bookmark. A bookmark that already has the
// new tag keeps one copy of it.
func RenameTag(db *sql.DB, oldTag, newTag string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO tags (bookmark_id, tag) SELECT bookmark_id, ? FROM tags WHERE tag = ?`, newTag, oldTag); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE tag = ?`, oldTag); err != nil {
		return err
	}
	return tx.Commit()
}

func DeleteTag(db *sql.DB, tag string) error {
	_, err := db.Exec(`DELETE FROM tags WHERE tag = ?`, tag)
	return err
}

// --- Helpers ---

func loadRelations(db *sql.DB, bm *Bookmark) error {
	var err error
	if bm.Tags, err = loadTags(db, bm.ID); err != nil {
		return err
	}
	bm.Folders, err = loadFolders(db, bm.ID)
	return err
}

// loadRelationsBatch fills in the tags, and with folders the folders, of
// every bookmark in bms with one query each, rather than one per bookmark.
// The ids go in as one JSON array, so there is no limit on how many.
func loadRelationsBatch(db *sql.DB, bms []Bookmark, folders bool) error {
	if len(bms) == 0 {
		return nil
	}
	byID := make(map[int64]*Bookmark, len(bms))
	ids := make([]int64, len(bms))
	for i := range bms {
		byID[bms[i].ID] = &bms[i]
		ids[i] = bms[i].ID
	}
	idJSON, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("encode bookmark ids: %w", err)
	}
	args := []any{string(idJSON)}
	const in = "(SELECT value FROM json_each(?))"

	rows, err := db.Query(`SELECT bookmark_id, tag FROM tags WHERE bookmark_id IN `+in+` ORDER BY bookmark_id, tag`, args...)
	if err != nil {
		return fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			return fmt.Errorf("scan tag: %w", err)
		}
		byID[id].Tags = append(byID[id].Tags, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load tags: %w", err)
	}
	if !folders {
		return nil
	}

	frows, err := db.Query(`SELECT bookmark_id, folder_id FROM bookmark_folders WHERE bookmark_id IN `+in+` ORDER BY bookmark_id, rowid`, args...)
	if err != nil {
		return fmt.Errorf("load folders: %w", err)
	}
	defer frows.Close()
	for frows.Next() {
		var id, f int64
		if err := frows.Scan(&id, &f); err != nil {
			return fmt.Errorf("scan folder: %w", err)
		}
		byID[id].Folders = append(byID[id].Folders, f)
	}
	if err := frows.Err(); err != nil {
		return fmt.Errorf("load folders: %w", err)
	}
	return nil
}

func loadTags(db *sql.DB, bookmarkID int64) ([]string, error) {
	rows, err := db.Query(`SELECT tag FROM tags WHERE bookmark_id = ? ORDER BY tag`, bookmarkID)
	if err != nil {
		return nil, fmt.Errorf("load tags: %w", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func loadFolders(db *sql.DB, bookmarkID int64) ([]int64, error) {
	rows, err := db.Query(`SELECT folder_id FROM bookmark_folders WHERE bookmark_id = ? ORDER BY rowid`, bookmarkID)
	if err != nil {
		return nil, fmt.Errorf("load folders: %w", err)
	}
	defer rows.Close()
	var folders []int64
	for rows.Next() {
		var f int64
		if err := rows.Scan(&f); err != nil {
			return nil, fmt.Errorf("scan folder: %w", err)
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func setTags(tx *sql.Tx, bookmarkID int64, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM tags WHERE bookmark_id = ?`, bookmarkID); err != nil {
		return fmt.Errorf("clear tags: %w", err)
	}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags (bookmark_id, tag) VALUES (?, ?)`, bookmarkID, t); err != nil {
			return fmt.Errorf("add tag %q: %w", t, err)
		}
	}
	return nil
}

// setFolders puts a bookmark in exactly folders, or the root when there are
// none. It stays where it was in a folder it was already in, and goes last in
// one it joins.
func setFolders(tx *sql.Tx, bookmarkID int64, folders []int64) error {
	if len(folders) == 0 {
		folders = []int64{RootFolder}
	}
	keep, err := json.Marshal(folders)
	if err != nil {
		return fmt.Errorf("encode folders: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM bookmark_folders WHERE bookmark_id = ? AND folder_id NOT IN (SELECT value FROM json_each(?))`, bookmarkID, string(keep)); err != nil {
		return fmt.Errorf("clear folders: %w", err)
	}
	for _, f := range folders {
		if err := linkBookmark(tx, bookmarkID, f); err != nil {
			return err
		}
	}
	return nil
}

// linkBookmark adds a bookmark to the end of a folder it is not already in.
// A folder that does not exist is ErrFolderNotFound.
func linkBookmark(tx *sql.Tx, bookmarkID, folder int64) error {
	if err := folderExists(tx, folder); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO bookmark_folders (bookmark_id, folder_id, sort_order) VALUES (?, ?, (`+nextOrderSQL+`))`,
		bookmarkID, folder, parentArg(folder), folder); err != nil {
		return fmt.Errorf("add folder %d: %w", folder, err)
	}
	return nil
}

func BookmarkCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM bookmarks`).Scan(&n)
	return n, err
}
