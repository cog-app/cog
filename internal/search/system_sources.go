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
		"SELECT rowid, id, username, display_name FROM users WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var id, username, displayName string
			if err := rows.Scan(&rowid, &id, &username, &displayName); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("users:%d", rowid),
				Fields: map[string]any{
					"_id":     id,
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
		"SELECT rowid, id, name FROM groups WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var id, name string
			if err := rows.Scan(&rowid, &id, &name); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("groups:%d", rowid),
				Fields: map[string]any{
					"_id":    id,
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
		"SELECT rowid, id, name, description FROM volumes WHERE rowid IN (%s)",
		func(rows *sql.Rows) (Document, error) {
			var rowid int64
			var id, name, description string
			if err := rows.Scan(&rowid, &id, &name, &description); err != nil {
				return Document{}, err
			}
			return Document{
				ID: fmt.Sprintf("volumes:%d", rowid),
				Fields: map[string]any{
					"_id":     id,
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
