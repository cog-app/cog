package search

import "database/sql"

// SearchSource provides documents from a particular data source (e.g. users, files).
// Implementations watch specific database tables and enrich row IDs into indexable documents.
type SearchSource interface {
	// Name returns a unique identifier for this source (e.g. "users", "files").
	Name() string
	// Tables returns the primary table names this source watches via update hook.
	Tables() []string
	// Enrich queries the database for the given row IDs and returns documents.
	// Row IDs not found in the database are considered deleted (omitted from results).
	Enrich(db *sql.DB, rowIDs []int64) ([]Document, error)
}
