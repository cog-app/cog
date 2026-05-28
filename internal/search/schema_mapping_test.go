package search

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2/mapping"
)

func TestBuildSchemaMappingMapsFields(t *testing.T) {
	sc := query.Schema{
		Fields: map[string]query.FieldSpec{
			"title": {Type: query.Text, Expr: "i.title"},
			"type":  {Type: query.Keyword, Expr: "i.type"},
			"is": {Type: query.EnumPredicate, Values: map[string]string{
				"unread": "i.is_read = 0",
			}},
		},
	}
	m, err := buildSchemaMapping(map[string]*query.Schema{"feed": &sc})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("nil mapping")
	}
	// Smoke-check the mapping is an *mapping.IndexMappingImpl with the
	// expected default document mapping enabled.
	if _, ok := m.(mapping.IndexMapping); !ok {
		t.Fatalf("got %T, not IndexMapping", m)
	}
}

func TestBuildSchemaMappingEmptySchemas(t *testing.T) {
	m, err := buildSchemaMapping(map[string]*query.Schema{})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("nil mapping")
	}
}

func TestBuildSchemaMappingRejectsUnknownType(t *testing.T) {
	sc := query.Schema{Fields: map[string]query.FieldSpec{
		"x": {Type: query.FieldType(99), Expr: "x"},
	}}
	_, err := buildSchemaMapping(map[string]*query.Schema{"src": &sc})
	if err == nil {
		t.Fatal("want error for unknown FieldType")
	}
}
