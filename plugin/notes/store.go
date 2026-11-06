package notes

import (
	"database/sql"
	"fmt"
	"strings"
)

// Note is a markdown note with metadata stored in SQLite.
type Note struct {
	ID        int64
	Filename  string
	Category  string
	Favorite  bool
	ETag      string
	Modified  int64
	CreatedAt int64
}

// Title returns the display title (filename without .md extension).
func (n *Note) Title() string {
	return TitleFromFilename(n.Filename)
}

// --- Metadata CRUD ---

func ListNotes(db *sql.DB, limit int) ([]Note, error) {
	return queryNotes(db, `SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes ORDER BY modified DESC, id DESC LIMIT ?`, limit)
}

func NotesByCategory(db *sql.DB, category string, limit int) ([]Note, error) {
	return queryNotes(db, `SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes WHERE category = ? ORDER BY modified DESC, id DESC LIMIT ?`, category, limit)
}

func StarredNotes(db *sql.DB, limit int) ([]Note, error) {
	return queryNotes(db, `SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes WHERE favorite = 1 ORDER BY modified DESC, id DESC LIMIT ?`, limit)
}

func RecentNotes(db *sql.DB, limit int) ([]Note, error) {
	return queryNotes(db, `SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes ORDER BY modified DESC, id DESC LIMIT ?`, limit)
}

func GetNote(db *sql.DB, id int64) (*Note, error) {
	var n Note
	err := db.QueryRow(`SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes WHERE id = ?`, id).Scan(&n.ID, &n.Filename, &n.Category, &n.Favorite, &n.ETag, &n.Modified, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func GetNoteByFilename(db *sql.DB, filename string) (*Note, error) {
	var n Note
	err := db.QueryRow(`SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes WHERE filename = ?`, filename).Scan(&n.ID, &n.Filename, &n.Category, &n.Favorite, &n.ETag, &n.Modified, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func CreateNoteMeta(db *sql.DB, filename, category, etag string) (*Note, error) {
	res, err := db.Exec(`INSERT INTO notes (filename, category, etag) VALUES (?, ?, ?)`, filename, category, etag)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetNote(db, id)
}

func UpdateNoteMeta(db *sql.DB, id int64, filename, category, etag string) error {
	_, err := db.Exec(`UPDATE notes SET filename = ?, category = ?, etag = ?, modified = unixepoch() WHERE id = ?`,
		filename, category, etag, id)
	return err
}

func DeleteNoteMeta(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM notes WHERE id = ?`, id)
	return err
}

func ToggleFavorite(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE notes SET favorite = 1 - favorite WHERE id = ?`, id)
	return err
}

func ListCategories(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT DISTINCT category FROM notes WHERE category != '' ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

func NoteCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&n)
	return n, err
}

func StarredCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE favorite = 1`).Scan(&n)
	return n, err
}

func queryNotes(db *sql.DB, query string, args ...any) ([]Note, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Filename, &n.Category, &n.Favorite, &n.ETag, &n.Modified, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

// TitleFromFilename strips the .md extension to get the display title.
func TitleFromFilename(filename string) string {
	return strings.TrimSuffix(filename, ".md")
}

// FilenameFromTitle sanitizes a title and adds the .md extension.
func FilenameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	// Remove characters that are problematic in filenames.
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "")
	title = replacer.Replace(title)
	if title == "" {
		title = "Untitled"
	}
	return title + ".md"
}

// CategoryCount returns the number of notes in a given category.
func CategoryCount(db *sql.DB, category string) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM notes WHERE category = ?`, category).Scan(&n)
	return n, err
}

// SearchNotes returns notes matching a query string in filename.
func SearchNotes(db *sql.DB, query string, limit int) ([]Note, error) {
	return queryNotes(db, `SELECT id, filename, category, favorite, etag, modified, created_at
		FROM notes WHERE filename LIKE ? ORDER BY modified DESC, id DESC LIMIT ?`,
		fmt.Sprintf("%%%s%%", query), limit)
}
