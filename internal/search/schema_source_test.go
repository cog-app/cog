package search

import (
	"database/sql"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin"
)

type fakeEnricher struct{ docs []plugin.Document }

func (f *fakeEnricher) EnrichSchema(_ *sql.DB, _ []int64) ([]plugin.Document, error) {
	return f.docs, nil
}

func TestPluginSearchSourceUsesSchemaPath(t *testing.T) {
	sc := query.Schema{Fields: map[string]query.FieldSpec{"title": {Type: query.Text, Expr: "i.title"}}}
	en := &fakeEnricher{docs: []plugin.Document{{ID: "feed:1", Fields: map[string]any{"title": "X"}}}}
	s := NewPluginSearchSource("feed", plugin.SearchSourceDef{Schema: &sc, Enricher: en})
	got, err := s.Enrich(nil, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d docs", len(got))
	}
	if got[0].Fields["source"] != "feed" {
		t.Fatalf("source = %v", got[0].Fields["source"])
	}
	if got[0].Fields["feed.title"] != "X" {
		t.Fatalf("feed.title = %v (fields=%v)", got[0].Fields["feed.title"], got[0].Fields)
	}
}

func TestPluginSearchSourceLegacyPathStillWorks(t *testing.T) {
	// When Schema/Enricher unset, Enrich falls back to EnrichSQL. We can't
	// fully exercise that without a DB; the test just verifies that an empty
	// def with no schema returns gracefully when given no rowIDs.
	s := NewPluginSearchSource("legacy", plugin.SearchSourceDef{PrimaryTable: "t"})
	got, err := s.Enrich(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d docs", len(got))
	}
}
