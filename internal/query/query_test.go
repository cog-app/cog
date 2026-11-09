package query

import (
	"errors"
	"testing"
)

func TestTextMatch(t *testing.T) {
	e, err := Parse("hello")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"Hello World"}}) {
		t.Error("should match case-insensitive")
	}
	if e.Match(Record{"title": {"Goodbye"}}) {
		t.Error("should not match")
	}
}

func TestPhraseMatch(t *testing.T) {
	e, err := Parse(`"hello world"`)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"say Hello World now"}}) {
		t.Error("should match phrase case-insensitive")
	}
	if e.Match(Record{"title": {"hello big world"}}) {
		t.Error("should not match non-contiguous")
	}
}

func TestNotOperator(t *testing.T) {
	e, err := Parse("NOT hello")
	if err != nil {
		t.Fatal(err)
	}
	if e.Match(Record{"title": {"Hello"}}) {
		t.Error("NOT hello should not match Hello")
	}
	if !e.Match(Record{"title": {"Goodbye"}}) {
		t.Error("NOT hello should match Goodbye")
	}
}

func TestBangOperator(t *testing.T) {
	e, err := Parse("!hello")
	if err != nil {
		t.Fatal(err)
	}
	if e.Match(Record{"title": {"Hello"}}) {
		t.Error("!hello should not match Hello")
	}
}

func TestAndExplicit(t *testing.T) {
	e, err := Parse("hello AND world")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"Hello World"}}) {
		t.Error("should match both terms")
	}
	if e.Match(Record{"title": {"Hello"}}) {
		t.Error("should not match only one term")
	}
}

func TestAndImplicit(t *testing.T) {
	e, err := Parse("hello world")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"Hello World"}}) {
		t.Error("implicit AND should match both terms")
	}
	if e.Match(Record{"title": {"Hello"}}) {
		t.Error("implicit AND should not match only one term")
	}
}

func TestOrOperator(t *testing.T) {
	e, err := Parse("hello OR goodbye")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"Hello"}}) {
		t.Error("OR should match first")
	}
	if !e.Match(Record{"title": {"Goodbye"}}) {
		t.Error("OR should match second")
	}
	if e.Match(Record{"title": {"Nothing"}}) {
		t.Error("OR should not match neither")
	}
}

func TestGrouping(t *testing.T) {
	e, err := Parse("(hello OR goodbye) AND world")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"title": {"Hello World"}}) {
		t.Error("should match hello + world")
	}
	if !e.Match(Record{"title": {"Goodbye World"}}) {
		t.Error("should match goodbye + world")
	}
	if e.Match(Record{"title": {"Hello"}}) {
		t.Error("should not match without world")
	}
}

func TestFieldEq(t *testing.T) {
	e, err := Parse(":type=video")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"type": {"video"}}) {
		t.Error("should match field value")
	}
	if !e.Match(Record{"type": {"Video"}}) {
		t.Error("should match case-insensitive")
	}
	if e.Match(Record{"type": {"audio"}}) {
		t.Error("should not match different value")
	}
}

func TestFieldEqQuoted(t *testing.T) {
	e, err := Parse(`:source="Go Blog"`)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"source": {"Go Blog"}}) {
		t.Error("should match quoted field value")
	}
	if e.Match(Record{"source": {"Go"}}) {
		t.Error("should not match partial")
	}
}

func TestFieldIn(t *testing.T) {
	e, err := Parse(":tag IN (audio, video, podcast)")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"tag": {"audio"}}) {
		t.Error("should match first value")
	}
	if !e.Match(Record{"tag": {"Video"}}) {
		t.Error("should match case-insensitive")
	}
	if e.Match(Record{"tag": {"document"}}) {
		t.Error("should not match outside set")
	}
}

func TestPrecedence(t *testing.T) {
	// NOT binds tighter than AND, AND tighter than OR
	// "a OR NOT b AND c" = "a OR ((NOT b) AND c)"
	e, err := Parse("a OR NOT b AND c")
	if err != nil {
		t.Fatal(err)
	}
	// Should match when a is present (regardless of b, c)
	if !e.Match(Record{"x": {"a"}}) {
		t.Error("should match via OR left")
	}
	// Should match when b is absent and c is present
	if !e.Match(Record{"x": {"c"}}) {
		t.Error("should match NOT b AND c")
	}
	// Should not match when only b is present
	if e.Match(Record{"x": {"b"}}) {
		t.Error("should not match b alone")
	}
}

func TestNotFieldEq(t *testing.T) {
	e, err := Parse("NOT :type=video")
	if err != nil {
		t.Fatal(err)
	}
	if e.Match(Record{"type": {"video"}}) {
		t.Error("should not match")
	}
	if !e.Match(Record{"type": {"audio"}}) {
		t.Error("should match")
	}
}

func TestComplex(t *testing.T) {
	e, err := Parse(`(:type=video OR :type=audio) AND "go lang" AND NOT :source="Spam Feed"`)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Match(Record{"type": {"video"}, "title": {"Learn Go Lang"}, "source": {"Good Feed"}}) {
		t.Error("should match")
	}
	if e.Match(Record{"type": {"video"}, "title": {"Learn Go Lang"}, "source": {"Spam Feed"}}) {
		t.Error("should not match spam source")
	}
	if e.Match(Record{"type": {"document"}, "title": {"Learn Go Lang"}, "source": {"Good Feed"}}) {
		t.Error("should not match document type")
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{`"hello world"`, `"hello world"`},
		{"NOT hello", "NOT hello"},
		{"a AND b", "a AND b"},
		{"a OR b", "(a OR b)"},
		{":type=video", ":type=video"},
		{`:name="foo bar"`, `:name="foo bar"`},
		{":tag IN (a, b, c)", ":tag IN (a, b, c)"},
	}
	for _, tt := range tests {
		e, err := Parse(tt.input)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.input, err)
			continue
		}
		got := e.String()
		if got != tt.want {
			t.Errorf("Parse(%q).String() = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"",
		"(hello",
		":=value",
		"AND",
		"OR",
		":field IN hello",
		":field IN ()",
	}
	for _, input := range bad {
		_, err := Parse(input)
		if err == nil {
			t.Errorf("Parse(%q) should return error", input)
		}
	}
}

func TestMatchAnyField(t *testing.T) {
	e, err := Parse("hello")
	if err != nil {
		t.Fatal(err)
	}
	// Should match if ANY field contains the term.
	if !e.Match(Record{"title": {"Goodbye"}, "description": {"Hello there"}}) {
		t.Error("should match description field")
	}
}

func TestEmptyRecord(t *testing.T) {
	e, err := Parse("hello")
	if err != nil {
		t.Fatal(err)
	}
	if e.Match(Record{}) {
		t.Error("should not match empty record")
	}
}

func TestFieldEqMultiValue(t *testing.T) {
	e := &FieldEqExpr{Field: "tag", Value: "sunday"}
	if !e.Match(Record{"tag": {"weekly", "sunday", "news"}}) {
		t.Error("expected match on second value")
	}
	if e.Match(Record{"tag": {"weekly", "news"}}) {
		t.Error("expected no match")
	}
}

func TestFieldInMultiValue(t *testing.T) {
	e := &FieldInExpr{Field: "is", Values: []string{"unread", "starred"}}
	if !e.Match(Record{"is": {"downloaded", "starred"}}) {
		t.Error("expected match: starred is in record and in expected set")
	}
	if e.Match(Record{"is": {"downloaded"}}) {
		t.Error("expected no match")
	}
}

func TestTextMultiValue(t *testing.T) {
	e := &TextExpr{Value: "hello"}
	if !e.Match(Record{"x": {"nope"}, "y": {"say HELLO world"}}) {
		t.Error("expected match across multi-value fields")
	}
}

func TestMatchAllExpr(t *testing.T) {
	e, err := Parse("*")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := e.(*MatchAllExpr); !ok {
		t.Fatalf("expected *MatchAllExpr, got %T", e)
	}
	if !e.Match(Record{}) {
		t.Error("MatchAllExpr should match empty record")
	}
	if !e.Match(Record{"title": {"anything"}}) {
		t.Error("MatchAllExpr should match any record")
	}
	if e.String() != "*" {
		t.Errorf("String() = %q, want %q", e.String(), "*")
	}
}

func TestMatchAllExprInString(t *testing.T) {
	// Ensure the String round-trip test covers *
	e, err := Parse("*")
	if err != nil {
		t.Fatal(err)
	}
	if e.String() != "*" {
		t.Errorf("Parse(%q).String() = %q, want %q", "*", e.String(), "*")
	}
}

func TestParseFieldCompareOps(t *testing.T) {
	cases := []struct {
		in    string
		field string
		op    CompareOp
		val   string
	}{
		{":published>2026-01-01", "published", OpGt, "2026-01-01"},
		{":published>=2026-01-01", "published", OpGe, "2026-01-01"},
		{":published<2026-01-01", "published", OpLt, "2026-01-01"},
		{":published<=2026-01-01", "published", OpLe, "2026-01-01"},
		{":duration>600", "duration", OpGt, "600"},
		{"published>-7d", "published", OpGt, "-7d"}, // shorthand form
	}
	for _, c := range cases {
		expr, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		fc, ok := expr.(*FieldCompareExpr)
		if !ok {
			t.Fatalf("%s: got %T, want *FieldCompareExpr", c.in, expr)
		}
		if fc.Field != c.field || fc.Op != c.op || fc.Value != c.val {
			t.Fatalf("%s: got {%s %v %q}", c.in, fc.Field, fc.Op, fc.Value)
		}
	}
}

func TestParseCompareComposes(t *testing.T) {
	// between: published >= A AND published < B
	expr, err := Parse(":published>=2026-01-01 :published<2026-02-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := expr.(*AndExpr); !ok {
		t.Fatalf("got %T, want *AndExpr", expr)
	}
}

func TestFieldCompareMatchNumeric(t *testing.T) {
	r := Record{"duration": {"600"}}
	if !(&FieldCompareExpr{Field: "duration", Op: OpGt, Value: "300"}).Match(r) {
		t.Fatal("600 > 300 should match")
	}
	if (&FieldCompareExpr{Field: "duration", Op: OpLt, Value: "300"}).Match(r) {
		t.Fatal("600 < 300 should not match")
	}
}

func TestFieldCompareMatchTime(t *testing.T) {
	r := Record{"published": {"2026-05-28T00:00:00Z"}}
	if !(&FieldCompareExpr{Field: "published", Op: OpGe, Value: "2026-01-01T00:00:00Z"}).Match(r) {
		t.Fatal("May >= Jan should match")
	}
	if (&FieldCompareExpr{Field: "published", Op: OpLt, Value: "2026-01-01T00:00:00Z"}).Match(r) {
		t.Fatal("May < Jan should not match")
	}
}

func TestParseErrorPosition(t *testing.T) {
	for _, c := range []struct {
		in  string
		pos int
	}{
		{"(type:audio", 11}, // the end, where ')' was wanted
		{"a :", 3},
		{"a )", 2},
	} {
		_, err := Parse(c.in)
		se, ok := errors.AsType[*SyntaxError](err)
		if !ok {
			t.Errorf("Parse(%q) = %v, want a *SyntaxError", c.in, err)
			continue
		}
		if se.Pos != c.pos {
			t.Errorf("Parse(%q): position %d, want %d (%v)", c.in, se.Pos, c.pos, err)
		}
	}
}
