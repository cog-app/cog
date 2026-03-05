package search

import (
	"fmt"
	"os"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
)

// Index wraps a Bleve index with a standard mapping.
type Index struct {
	idx bleve.Index
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

// Search performs a match query and returns scored hits with stored fields.
func (i *Index) Search(query string, limit, offset int) (*Result, error) {
	if limit <= 0 {
		limit = 20
	}
	matchQ := bleve.NewMatchQuery(query)
	prefixQ := bleve.NewPrefixQuery(strings.ToLower(query))
	q := bleve.NewDisjunctionQuery(matchQ, prefixQ)
	req := bleve.NewSearchRequestOptions(q, limit, offset, false)
	req.Fields = []string{"name", "subtext", "content", "source"}
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
	docMapping.AddFieldMappingsAt("name", textField)
	docMapping.AddFieldMappingsAt("subtext", textField)
	docMapping.AddFieldMappingsAt("content", textField)
	docMapping.AddFieldMappingsAt("source", keywordField)

	m := bleve.NewIndexMapping()
	m.DefaultMapping = docMapping
	return m
}
