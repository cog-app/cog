package search

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
)

func TestSchemaRegistryFanOutsAcrossSources(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed":  {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "x"}}}},
		"other": {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "y"}}}},
	}
	reg := newSchemaRegistry(schemas)
	got := reg.prefixed("is")
	if len(got) != 2 || got[0] != "feed" || got[1] != "other" {
		t.Fatalf("got %v", got)
	}
	if reg.prefixed("ghost") != nil {
		t.Fatal("unknown field must return nil")
	}
}

func TestToBleveQueryWithSchemaFieldEqFanOut(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed":  {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "x"}}}},
		"other": {Fields: map[string]query.FieldSpec{"is": {Type: query.EnumPredicate, Values: map[string]string{"downloaded": "y"}}}},
	}
	reg := newSchemaRegistry(schemas)
	q := toBleveQueryWithSchema(&query.FieldEqExpr{Field: "is", Value: "downloaded"}, reg)
	// Stringified bleve query should reference both prefixed fields.
	s := stringifyBleveQuery(q)
	if !strings.Contains(s, "feed.is") || !strings.Contains(s, "other.is") {
		t.Fatalf("missing per-source fields in %q", s)
	}
}

func TestToBleveQueryWithSchemaSingleSource(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed": {Fields: map[string]query.FieldSpec{"type": {Type: query.Keyword, Expr: "i.type"}}},
	}
	reg := newSchemaRegistry(schemas)
	q := toBleveQueryWithSchema(&query.FieldEqExpr{Field: "type", Value: "audio"}, reg)
	s := stringifyBleveQuery(q)
	if !strings.Contains(s, "feed.type") {
		t.Fatalf("got %q", s)
	}
}

func TestToBleveQueryWithSchemaUnknownFieldReturnsMatchNone(t *testing.T) {
	reg := newSchemaRegistry(map[string]*query.Schema{})
	q := toBleveQueryWithSchema(&query.FieldEqExpr{Field: "ghost", Value: "x"}, reg)
	s := stringifyBleveQuery(q)
	// MatchNone has no field references. Just confirm it doesn't reference
	// the unknown field directly.
	if strings.Contains(s, "ghost") {
		t.Fatalf("unknown field should not appear: %q", s)
	}
}

// stringifyBleveQuery returns a debug-friendly representation using JSON,
// which bleve queries marshal to reliably (field names are explicit JSON keys).
func stringifyBleveQuery(q any) string {
	b, err := json.Marshal(q)
	if err != nil {
		return fmt.Sprintf("%+v", q)
	}
	return string(b)
}
