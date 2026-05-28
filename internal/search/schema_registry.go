package search

import (
	"sort"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// schemaRegistry maps cog-grammar field names to the source names that
// declare them, used to fan a query like "is:downloaded" out to
// "feed.is:downloaded OR other.is:downloaded" across the bleve index.
type schemaRegistry struct {
	fieldSources map[string][]string // field name -> sorted source names
}

func newSchemaRegistry(schemas map[string]*query.Schema) *schemaRegistry {
	r := &schemaRegistry{fieldSources: map[string][]string{}}
	for src, sc := range schemas {
		if sc == nil {
			continue
		}
		for name := range sc.Fields {
			r.fieldSources[name] = append(r.fieldSources[name], src)
		}
	}
	for k := range r.fieldSources {
		sort.Strings(r.fieldSources[k])
	}
	return r
}

// prefixed returns the sorted list of source names that declare the given
// field, or nil if no source declares it.
func (r *schemaRegistry) prefixed(field string) []string {
	return r.fieldSources[field]
}
