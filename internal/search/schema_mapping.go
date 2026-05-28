package search

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

// buildSchemaMapping turns one or more plugin Schemas into a single bleve
// IndexMapping. Field names are prefixed by source name (e.g. "feed.title")
// to namespace across plugins. A keyword "source" field is added for scoping.
func buildSchemaMapping(schemas map[string]*query.Schema) (mapping.IndexMapping, error) {
	doc := bleve.NewDocumentMapping()
	doc.AddFieldMappingsAt("_id", bleve.NewKeywordFieldMapping())
	doc.AddFieldMappingsAt("source", bleve.NewKeywordFieldMapping())
	for src, sc := range schemas {
		if sc == nil {
			continue
		}
		for name, f := range sc.Fields {
			fm, err := bleveMappingFor(f)
			if err != nil {
				return nil, fmt.Errorf("schema %q field %q: %w", src, name, err)
			}
			doc.AddFieldMappingsAt(src+"."+name, fm)
		}
	}
	m := bleve.NewIndexMapping()
	m.DefaultMapping = doc
	return m, nil
}

// bleveMappingFor returns the bleve field mapping for one FieldSpec. Schemas
// reserved for SQL-only semantics (ManyToMany, EnumPredicate) index as
// multi-valued keyword fields; the source supplies a []string at index time
// listing the active values.
func bleveMappingFor(f query.FieldSpec) (*mapping.FieldMapping, error) {
	switch f.Type {
	case query.Text:
		fm := bleve.NewTextFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Keyword, query.ManyToMany, query.EnumPredicate:
		fm := bleve.NewKeywordFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Bool:
		fm := bleve.NewBooleanFieldMapping()
		fm.Store = true
		return fm, nil
	case query.Number:
		fm := bleve.NewNumericFieldMapping()
		fm.Store = true
		return fm, nil
	case query.DateText:
		fm := bleve.NewDateTimeFieldMapping()
		fm.Store = true
		return fm, nil
	default:
		return nil, fmt.Errorf("unknown field type %d", f.Type)
	}
}
