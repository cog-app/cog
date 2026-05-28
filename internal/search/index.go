package search

import (
	"fmt"
	"os"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	bleveQuery "github.com/blevesearch/bleve/v2/search/query"
)

// Index wraps a Bleve index with a standard mapping.
type Index struct {
	idx        bleve.Index
	reg        *schemaRegistry          // nil when opened via legacy Open()
	regSchemas map[string]*query.Schema // kept alongside reg for field enumeration
}

// Open opens or creates a Bleve index at the given path.
// The index uses a standard mapping with text fields for name, subtext, content
// and a keyword field for source.
func Open(path string) (*Index, error) {
	idx, err := bleve.Open(path)
	if err == bleve.ErrorIndexPathDoesNotExist {
		idx, err = bleve.New(path, buildMapping())
	}
	if err != nil {
		return nil, fmt.Errorf("open search index at %s: %w", path, err)
	}
	return &Index{idx: idx}, nil
}

// OpenWithSchemas opens or creates a bleve index whose mapping derives from
// the given per-source schemas. Falls back to legacy Open(path) when schemas
// is empty (preserves backward-compat for plugins not yet migrated).
func OpenWithSchemas(path string, schemas map[string]*query.Schema) (*Index, error) {
	if len(schemas) == 0 {
		return Open(path)
	}
	m, err := buildSchemaMapping(schemas)
	if err != nil {
		return nil, fmt.Errorf("schema mapping: %w", err)
	}
	idx, err := bleve.New(path, m)
	if err == bleve.ErrorIndexPathExists {
		idx, err = bleve.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}
	return &Index{idx: idx, reg: newSchemaRegistry(schemas), regSchemas: schemas}, nil
}

// OpenMemory creates an in-memory Bleve index for testing.
func OpenMemory() (*Index, error) {
	idx, err := bleve.NewMemOnly(buildMapping())
	if err != nil {
		return nil, fmt.Errorf("open memory index: %w", err)
	}
	return &Index{idx: idx}, nil
}

// Close closes the underlying Bleve index.
func (i *Index) Close() error {
	return i.idx.Close()
}

// Batch applies a set of document upserts and deletes atomically.
func (i *Index) Batch(docs []Document, deleteIDs []string) error {
	b := i.idx.NewBatch()
	for _, doc := range docs {
		b.Index(doc.ID, doc.Fields)
	}
	for _, id := range deleteIDs {
		b.Delete(id)
	}
	return i.idx.Batch(b)
}

// Search performs a query and returns scored hits with stored fields.
// The query string supports the syntax defined in internal/query:
// text terms, "phrases", NOT/AND/OR operators, (grouping),
// :field=value, and :field IN (a, b, c).
func (i *Index) Search(input string, limit, offset int) (*Result, error) {
	if limit <= 0 {
		limit = 20
	}

	var q bleveQuery.Query
	if expr, err := query.Parse(input); err == nil {
		if i.reg != nil {
			q = toBleveQueryWithSchema(expr, i.reg)
		} else {
			q = toBleveQuery(expr)
		}
	} else {
		// Fall back to simple match+prefix for unparseable input.
		matchQ := bleve.NewMatchQuery(input)
		prefixQ := bleve.NewPrefixQuery(strings.ToLower(input))
		q = bleve.NewDisjunctionQuery(matchQ, prefixQ)
	}

	return i.executeSearch(q, limit, offset)
}

func (i *Index) executeSearch(q bleveQuery.Query, limit, offset int) (*Result, error) {
	req := bleve.NewSearchRequestOptions(q, limit, offset, false)
	if i.regSchemas != nil {
		// Schema path: request the known per-source-prefixed fields explicitly.
		fields := []string{"_id", "source"}
		for src, sc := range i.regSchemas {
			for name := range sc.Fields {
				fields = append(fields, src+"."+name)
			}
		}
		req.Fields = fields
	} else {
		req.Fields = []string{"_id", "name", "subtext", "content", "source"}
	}
	resp, err := i.idx.Search(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	hits := make([]Hit, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		hit := Hit{
			ID:     h.ID,
			Score:  h.Score,
			Fields: h.Fields,
		}
		if src, ok := h.Fields["source"].(string); ok {
			hit.Source = src
		}
		hits = append(hits, hit)
	}
	return &Result{
		Total: int(resp.Total),
		Hits:  hits,
	}, nil
}

// GetDocument retrieves a document by ID from the index.
func (i *Index) GetDocument(id string) (*Document, error) {
	doc, err := i.idx.Document(id)
	if err != nil {
		return nil, fmt.Errorf("get document %s: %w", id, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("document %s not found", id)
	}
	// Re-index via search to get stored fields.
	q := bleve.NewDocIDQuery([]string{id})
	req := bleve.NewSearchRequestOptions(q, 1, 0, false)
	req.Fields = []string{"name", "subtext", "content", "source"}
	resp, err := i.idx.Search(req)
	if err != nil || resp.Total == 0 {
		return nil, fmt.Errorf("document %s not searchable", id)
	}
	return &Document{ID: id, Fields: resp.Hits[0].Fields}, nil
}

// Delete removes the index files from disk after closing.
func Delete(path string) error {
	return os.RemoveAll(path)
}

func buildMapping() mapping.IndexMapping {
	textField := bleve.NewTextFieldMapping()
	textField.Store = true

	keywordField := bleve.NewKeywordFieldMapping()
	keywordField.Store = true

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("_id", keywordField)
	docMapping.AddFieldMappingsAt("name", textField)
	docMapping.AddFieldMappingsAt("subtext", textField)
	docMapping.AddFieldMappingsAt("content", textField)
	docMapping.AddFieldMappingsAt("source", keywordField)

	m := bleve.NewIndexMapping()
	m.DefaultMapping = docMapping
	return m
}
