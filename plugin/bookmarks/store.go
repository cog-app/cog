package bookmarks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
}

// --- Bookmark CRUD ---

func ListBookmarks(db *sql.DB, folderID *int64, tags []string, search string, sortBy string, page, limit int) ([]Bookmark, int, error) {
	if limit <= 0 || limit > 300 {
		limit = 10
	}
	if page < 0 {
		page = 0
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
	args = append(args, limit, page*limit)

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

	// Load tags and folders for each bookmark.
	for i := range bookmarks {
		if err := loadRelations(db, &bookmarks[i]); err != nil {
			return nil, 0, err
		}
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
		return UpdateBookmark(db, existing.ID, &title, &description, tags, folders)
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

func UpdateBookmark(db *sql.DB, id int64, title, description *string, tags []string, folders []int64) (*Bookmark, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin update bookmark: %w", err)
	}
	defer tx.Rollback()
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
		if bm.Tags, err = loadTags(db, bm.ID); err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, bm)
	}
	return bookmarks, rows.Err()
}

// --- Folder CRUD ---

func ListFolders(db *sql.DB) ([]Folder, error) {
	rows, err := db.Query(`SELECT id, parent_id, title, created_at, updated_at FROM folders ORDER BY title COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folders []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Title, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func GetFolder(db *sql.DB, id int64) (*Folder, error) {
	var f Folder
	err := db.QueryRow(
		`SELECT id, parent_id, title, created_at, updated_at FROM folders WHERE id = ?`, id,
	).Scan(&f.ID, &f.ParentID, &f.Title, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func CreateFolder(db *sql.DB, title string, parentID *int64) (*Folder, error) {
	res, err := db.Exec(`INSERT INTO folders (title, parent_id) VALUES (?, ?)`, title, parentID)
	if err != nil {
		return nil, fmt.Errorf("insert folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("insert folder id: %w", err)
	}
	return GetFolder(db, id)
}

func UpdateFolder(db *sql.DB, id int64, title string, parentID *int64) error {
	_, err := db.Exec(`UPDATE folders SET title = ?, parent_id = ?, updated_at = unixepoch() WHERE id = ?`, title, parentID, id)
	return err
}

func DeleteFolder(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	return err
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
	rows, err := db.Query(`SELECT folder_id FROM bookmark_folders WHERE bookmark_id = ? ORDER BY sort_order`, bookmarkID)
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

func setFolders(tx *sql.Tx, bookmarkID int64, folders []int64) error {
	if _, err := tx.Exec(`DELETE FROM bookmark_folders WHERE bookmark_id = ?`, bookmarkID); err != nil {
		return fmt.Errorf("clear folders: %w", err)
	}
	if len(folders) == 0 {
		folders = []int64{-1} // root folder
	}
	for i, f := range folders {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO bookmark_folders (bookmark_id, folder_id, sort_order) VALUES (?, ?, ?)`, bookmarkID, f, i); err != nil {
			return fmt.Errorf("add folder %d: %w", f, err)
		}
	}
	return nil
}

func BookmarkCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM bookmarks`).Scan(&n)
	return n, err
}
