package query

import (
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
