// Package search provides full-text search indexing via Bleve.
// It uses an async Collector pipeline fed by sqlite3_update_hook
// to keep indices in sync with database changes.
package search

import "git.duckfam.us/jj-software/cog/plugin"

// Op represents a database operation type from the SQLite update hook.
type Op int

const (
	OpInsert Op = iota
	OpUpdate
	OpDelete
)

// Change represents a single database row change captured by the update hook.
type Change struct {
	Op    Op
	Table string
	RowID int64
}

// Document is an indexable unit fed into the Bleve index. Re-exported from
// the public plugin contract so plugin-authored sources can produce values
// without importing internal/search.
type Document = plugin.Document

// Hit is a single search result from the index.
type Hit struct {
	ID     string         `json:"id"`
	Score  float64        `json:"score"`
	Source string         `json:"source"`
	Fields map[string]any `json:"fields"`
}

// Result is the response from a search query.
type Result struct {
	Total int   `json:"total"`
	Hits  []Hit `json:"hits"`
}
