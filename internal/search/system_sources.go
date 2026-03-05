package search

import (
	"database/sql"
	"fmt"
	"strings"
)

// UserSource indexes users from the system database.
type UserSource struct{}

func (UserSource) Name() string     { return "users" }
func (UserSource) Tables() []string { return []string{"users"} }

func (UserSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	return enrichQuery(db, "users", rowIDs,
		"SELECT rowid, username, display_name FROM users WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var username, displayName string
			if err := rows.Scan(&rowid, &username, &displayName); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("users:%d", rowid),
				Fields: map[string]any{
					"name":    username,
					"subtext": displayName,
					"source":  "users",
				},
			}, nil
		})
}

// GroupSource indexes groups from the system database.
type GroupSource struct{}

func (GroupSource) Name() string     { return "groups" }
func (GroupSource) Tables() []string { return []string{"groups"} }

func (GroupSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	return enrichQuery(db, "groups", rowIDs,
		"SELECT rowid, name FROM groups WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var name string
			if err := rows.Scan(&rowid, &name); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("groups:%d", rowid),
				Fields: map[string]any{
					"name":   name,
					"source": "groups",
				},
			}, nil
		})
}

// VolumeSource indexes volumes from the system database.
type VolumeSource struct{}

func (VolumeSource) Name() string     { return "volumes" }
func (VolumeSource) Tables() []string { return []string{"volumes"} }

func (VolumeSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	return enrichQuery(db, "volumes", rowIDs,
		"SELECT rowid, name, description FROM volumes WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var name, description string
			if err := rows.Scan(&rowid, &name, &description); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("volumes:%d", rowid),
				Fields: map[string]any{
					"name":    name,
					"subtext": description,
					"source":  "volumes",
				},
			}, nil
		})
}

// enrichQuery is a helper that builds an IN-clause query, executes it, and
// collects documents via a scan function.
func enrichQuery(db *sql.DB, source string, rowIDs []int64, queryTmpl string, scan func(*sql.Rows) (Document, error)) ([]Document, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(rowIDs))
	args := make([]any, len(rowIDs))
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(queryTmpl, strings.Join(placeholders, ","))
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("enrich %s: %w", source, err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		doc, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", source, err)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}
