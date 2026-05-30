package query

import (
	"strings"
	"testing"
)

// testTokenize is a simple whitespace+lowercase tokenizer used in tests.
// The real bleve-backed tokenizer lives in internal/search and is tested there.
func testTokenize(s string) []string {
	return strings.Fields(strings.ToLower(s))
}

// evalSchema returns a test schema with title/author/content as Text,
// type as Keyword, and is as EnumPredicate.
func evalSchema() Schema {
	return Schema{
		PrimaryAlias: "i",
		Fields: map[string]FieldSpec{
			"title":   {Type: Text, Expr: "i.title"},
			"author":  {Type: Text, Expr: "i.author"},
			"content": {Type: Text, Expr: "i.content"},
			"type":    {Type: Keyword, Expr: "i.type"},
			"is": {
				Type: EnumPredicate,
				Values: map[string]string{
					"unread":  "i.is_read = 0",
					"starred": "i.is_starred = 1",
				},
			},
		},
	}
}

func TestEvalBareWordTokenExact(t *testing.T) {
	// "cat" should match title "the cat" via exact token match.
	e := &TextExpr{Value: "cat"}
	s := evalSchema()
	r := Record{"title": {"the cat"}}
	if !Eval(e, r, s, testTokenize) {
		t.Error("bare word 'cat' should match title 'the cat' (exact token)")
	}
}

func TestEvalBareWordPrefix(t *testing.T) {
	// "cat" should match title "Category list" because token "category" is
	// prefixed by "cat".
	e := &TextExpr{Value: "cat"}
	s := evalSchema()
	r := Record{"title": {"Category list"}}
	if !Eval(e, r, s, testTokenize) {
		t.Error("bare word 'cat' should match title 'Category list' (prefix)")
	}
}

func TestEvalBareWordTextOnlyScope(t *testing.T) {
	// bare word "audio" should NOT match when only the Keyword field "type"
	// holds "audio" — Eval restricts bare-word matching to Type=Text fields.
	e := &TextExpr{Value: "audio"}
	s := evalSchema()
	r := Record{"type": {"audio"}}
	if Eval(e, r, s, testTokenize) {
		t.Error("bare word 'audio' must NOT match a Keyword field; use :type=audio")
	}
}

func TestEvalPhraseAdjacencyMatch(t *testing.T) {
	// "hello world" should match "say hello world" (consecutive tokens).
	e := &PhraseExpr{Value: "hello world"}
	s := evalSchema()
	r := Record{"content": {"say hello world"}}
	if !Eval(e, r, s, testTokenize) {
		t.Error(`phrase "hello world" should match "say hello world"`)
	}
}

func TestEvalPhraseReversedNoMatch(t *testing.T) {
	// "hello world" must NOT match "world hello" (wrong order).
	e := &PhraseExpr{Value: "hello world"}
	s := evalSchema()
	r := Record{"content": {"world hello"}}
	if Eval(e, r, s, testTokenize) {
		t.Error(`phrase "hello world" must NOT match "world hello"`)
	}
}

func TestEvalPhraseNonAdjacentNoMatch(t *testing.T) {
	// "hello world" must NOT match "hello big world" (not consecutive).
	e := &PhraseExpr{Value: "hello world"}
	s := evalSchema()
	r := Record{"content": {"hello big world"}}
	if Eval(e, r, s, testTokenize) {
		t.Error(`phrase "hello world" must NOT match "hello big world" (non-adjacent)`)
	}
}

func TestEvalCompoundAnd(t *testing.T) {
	// :type=audio AND "hello world" — both must hold.
	s := evalSchema()
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "type", Value: "audio"},
		Right: &PhraseExpr{Value: "hello world"},
	}
	// Both match.
	rBoth := Record{"type": {"audio"}, "content": {"say hello world"}}
	if !Eval(e, rBoth, s, testTokenize) {
		t.Error("And: both conditions met, should match")
	}
	// Only type matches.
	rTypeOnly := Record{"type": {"audio"}, "content": {"goodbye"}}
	if Eval(e, rTypeOnly, s, testTokenize) {
		t.Error("And: only type matches, should not match")
	}
	// Only phrase matches.
	rPhraseOnly := Record{"type": {"video"}, "content": {"say hello world"}}
	if Eval(e, rPhraseOnly, s, testTokenize) {
		t.Error("And: only phrase matches, should not match")
	}
}

func TestEvalCompoundOr(t *testing.T) {
	// :type=audio OR "hello world" — either suffices.
	s := evalSchema()
	e := &OrExpr{
		Left:  &FieldEqExpr{Field: "type", Value: "audio"},
		Right: &PhraseExpr{Value: "hello world"},
	}
	// Only type matches.
	if !Eval(e, Record{"type": {"audio"}}, s, testTokenize) {
		t.Error("Or: type match should satisfy")
	}
	// Only phrase matches.
	if !Eval(e, Record{"content": {"say hello world"}}, s, testTokenize) {
		t.Error("Or: phrase match should satisfy")
	}
	// Neither matches.
	if Eval(e, Record{"type": {"video"}, "content": {"goodbye"}}, s, testTokenize) {
		t.Error("Or: neither matches, should not satisfy")
	}
}

func TestEvalNot(t *testing.T) {
	// NOT "hello world" should be false when phrase is present, true otherwise.
	s := evalSchema()
	e := &NotExpr{Inner: &PhraseExpr{Value: "hello world"}}
	if Eval(e, Record{"content": {"say hello world"}}, s, testTokenize) {
		t.Error(`NOT "hello world": phrase present, should be false`)
	}
	if !Eval(e, Record{"content": {"goodbye"}}, s, testTokenize) {
		t.Error(`NOT "hello world": phrase absent, should be true`)
	}
}
