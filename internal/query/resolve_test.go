package query

import (
	"testing"
	"time"
)

func resolveTestSchema() Schema {
	return Schema{Fields: map[string]FieldSpec{
		"published": {Type: DateText, Expr: "i.published_at"},
		"duration":  {Type: Number, Expr: "i.duration_sec"},
	}}
}

func TestResolveRelativeDays(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	expr := &FieldCompareExpr{Field: "published", Op: OpGt, Value: "-7d"}
	out := ResolveRelativeDates(expr, resolveTestSchema(), now)
	fc := out.(*FieldCompareExpr)
	want := now.AddDate(0, 0, -7).UTC().Format(time.RFC3339)
	if fc.Value != want {
		t.Fatalf("got %q want %q", fc.Value, want)
	}
}

func TestResolveLeavesAbsoluteAndNumbers(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// Absolute date untouched.
	a := ResolveRelativeDates(&FieldCompareExpr{Field: "published", Op: OpGt, Value: "2026-01-01"}, resolveTestSchema(), now).(*FieldCompareExpr)
	if a.Value != "2026-01-01" {
		t.Fatalf("absolute date mutated: %q", a.Value)
	}
	// Number field: -7d is nonsense for a number; leave as-is (SQL will reject).
	n := ResolveRelativeDates(&FieldCompareExpr{Field: "duration", Op: OpGt, Value: "600"}, resolveTestSchema(), now).(*FieldCompareExpr)
	if n.Value != "600" {
		t.Fatalf("number mutated: %q", n.Value)
	}
}

func TestResolveRecursesAndOr(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	expr := &AndExpr{
		Left:  &FieldCompareExpr{Field: "published", Op: OpGe, Value: "-1d"},
		Right: &TextExpr{Value: "x"},
	}
	out := ResolveRelativeDates(expr, resolveTestSchema(), now).(*AndExpr)
	if out.Left.(*FieldCompareExpr).Value == "-1d" {
		t.Fatal("relative not resolved under AndExpr")
	}
}
