package feed

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
)

// ImportFormat names a subscription export Store.Import reads.
type ImportFormat string

const (
	ImportOPML       ImportFormat = "opml"
	ImportAntennaPod ImportFormat = "antennapod"  // AntennaPod's SQLite database
	ImportNewPipe    ImportFormat = "newpipe"     // NewPipe's ZIP export
	ImportYouTubeCSV ImportFormat = "youtube-csv" // Google Takeout's subscriptions.csv
)

// DetectImportFormat tells the formats apart by their first bytes: a ZIP is
// NewPipe's, a SQLite database AntennaPod's, XML is OPML, and anything else
// is taken for Takeout's CSV, which is the one without a signature.
func DetectImportFormat(data []byte) ImportFormat {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return ImportNewPipe
	case bytes.HasPrefix(data, []byte("SQLite format 3\x00")):
		return ImportAntennaPod
	case bytes.HasPrefix(bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n"), []byte("<")):
		return ImportOPML
	default:
		return ImportYouTubeCSV
	}
}

// ErrUnreadableImport is the error Import wraps when the export itself is at
// fault — not in the format, or damaged — rather than the server.
var ErrUnreadableImport = errors.New("unreadable export")

// Imported is what an import did: subscriptions added, and those skipped
// because one with the same URL was already there.
type Imported struct {
	Added, Skipped int
}

// importEntry is a subscription an export lists, and the name of the folder
// it lists it under, if any.
type importEntry struct {
	Subscription
	Folder string
}

// Import adds the subscriptions an export lists, skipping any already
// subscribed to. An entry the export puts in a folder goes in the folder of
// that name, created if need be; any other goes in folder. format "" detects
// it. Nothing is added unless everything is: the import is one transaction.
func (s *Store) Import(format ImportFormat, data []byte, folder sql.NullInt64) (Imported, error) {
	if format == "" {
		format = DetectImportFormat(data)
	}
	var entries []importEntry
	var err error
	switch format {
	case ImportOPML:
		entries, err = parseOPML(data)
	case ImportAntennaPod:
		entries, err = parseAntennaPod(data)
	case ImportNewPipe:
		entries, err = parseNewPipe(data)
	case ImportYouTubeCSV:
		entries, err = parseYouTubeTakeout(data)
	default:
		return Imported{}, fmt.Errorf("%w: unknown format %q", ErrUnreadableImport, format)
	}
	if err != nil {
		return Imported{}, fmt.Errorf("import %s: %w: %w", format, ErrUnreadableImport, err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Imported{}, fmt.Errorf("import %s: begin: %w", format, err)
	}
	res, err := importEntries(tx, entries, folder)
	if err != nil {
		return Imported{}, fmt.Errorf("import %s: %w", format, errors.Join(err, tx.Rollback()))
	}
	if err := tx.Commit(); err != nil {
		return Imported{}, fmt.Errorf("import %s: commit: %w", format, err)
	}
	return res, nil
}

func importEntries(tx *sql.Tx, entries []importEntry, folder sql.NullInt64) (Imported, error) {
	var res Imported
	folders := map[string]sql.NullInt64{}
	for _, e := range entries {
		e.FolderID = folder
		if e.Folder != "" {
			id, ok := folders[e.Folder]
			if !ok {
				var err error
				if id, err = importFolder(tx, e.Folder); err != nil {
					return res, err
				}
				folders[e.Folder] = id
			}
			e.FolderID = id
		}
		r, err := tx.Exec(`
			INSERT INTO subscriptions
				(folder_id, kind, url, title, site_url, image_url, poll_interval_sec, next_check_at)
			VALUES (?, ?, ?, ?, ?, ?, 1800, 1)
			ON CONFLICT(url) DO NOTHING`,
			e.FolderID, e.Kind, e.URL, e.Title, e.SiteURL, e.ImageURL,
		)
		if err != nil {
			return res, fmt.Errorf("subscription %q: %w", e.URL, err)
		}
		n, err := r.RowsAffected()
		if err != nil {
			return res, fmt.Errorf("subscription %q rows affected: %w", e.URL, err)
		}
		if n > 0 {
			res.Added++
		} else {
			res.Skipped++
		}
	}
	return res, nil
}

// importFolder returns the folder named name, creating it if there is none.
func importFolder(tx *sql.Tx, name string) (sql.NullInt64, error) {
	var id int64
	err := tx.QueryRow(`SELECT id FROM folders WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRow(`INSERT INTO folders (name) VALUES (?) RETURNING id`, name).Scan(&id)
	}
	if err != nil {
		return sql.NullInt64{}, fmt.Errorf("folder %q: %w", name, err)
	}
	return sql.NullInt64{Int64: id, Valid: true}, nil
}
