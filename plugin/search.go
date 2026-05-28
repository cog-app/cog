package plugin

import "database/sql"

// Document is an indexable unit produced by a plugin's search source and
// consumed by the host's search index. The shape is stable across plugin
// authors and the host implementation.
type Document struct {
	ID     string         // "{source}:{rowid}"
	Fields map[string]any // field-name -> value(s); shape determined by the source's Schema
}

// SchemaEnricher is implemented by plugin search sources that declare a
// query.Schema. The host calls EnrichSchema with the row IDs that changed;
// the source returns Documents whose Fields populate the Schema's fields.
//
// Sources that only set EnrichSQL on SearchSourceDef (the legacy positional
// path) do not implement this interface.
type SchemaEnricher interface {
	EnrichSchema(db *sql.DB, rowIDs []int64) ([]Document, error)
}
