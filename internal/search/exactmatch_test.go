package search

import (
	"slices"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	iassert "git.duckfam.us/jj-software/cog/internal/testing/assert"
	irequire "git.duckfam.us/jj-software/cog/internal/testing/require"
)

// feedSchema returns a minimal feed-like schema with title and content as Text
// fields, mirroring the bleve index mapping used by global search.
func feedSchema() query.Schema {
	return query.Schema{
		Fields: map[string]query.FieldSpec{
			"title":   {Type: query.Text, Expr: "i.title"},
			"content": {Type: query.Text, Expr: "i.content"},
		},
	}
}

func TestStandardTokenize_Basic(t *testing.T) {
	got := StandardTokenize("Hello, World!")
	irequire.Equal(t, []string{"hello", "world"}, got)
}

func TestStandardTokenize_StopWords(t *testing.T) {
	// "the" is an English stop word and should be removed.
	got := StandardTokenize("the cat")
	irequire.Equal(t, []string{"cat"}, got)
}

func TestStandardTokenize_Digits(t *testing.T) {
	got := StandardTokenize("rss2 feed")
	found := slices.Contains(got, "rss2")
	iassert.True(t, found, "expected token 'rss2' in %v", got)
}

func TestExactMatch_PhraseTrue(t *testing.T) {
	s := feedSchema()
	rec := query.Record{
		"content": {"say hello world"},
	}
	e := &query.PhraseExpr{Value: "hello world"}
	iassert.True(t, ExactMatch(e, rec, s), "phrase 'hello world' should match 'say hello world'")
}

func TestExactMatch_PhraseFalse_WrongOrder(t *testing.T) {
	s := feedSchema()
	rec := query.Record{
		"content": {"world hello"},
	}
	e := &query.PhraseExpr{Value: "hello world"}
	iassert.True(t, !ExactMatch(e, rec, s), "phrase 'hello world' should NOT match 'world hello'")
}

func TestExactMatch_BareWordPrefix(t *testing.T) {
	s := feedSchema()
	// "Category" tokenizes to "categori" (porter stemming) in some analyzers,
	// but bleve's standard analyzer only does lowercase, not stemming.
	// "category" starts with prefix "cat" so prefix match should fire.
	rec := query.Record{
		"title": {"Category"},
	}
	e := &query.TextExpr{Value: "cat"}
	iassert.True(t, ExactMatch(e, rec, s), "bare word 'cat' should prefix-match 'category'")
}

func TestExactMatch_BareWordNoSubstringMatch(t *testing.T) {
	// "scatter" contains "cat" as substring but "scatter" token does NOT have "cat" as prefix.
	// Token is "scatter"; prefix of "scatter" is "s", "sc", "sca"... not "cat".
	s := feedSchema()
	rec := query.Record{
		"title": {"scatter"},
	}
	e := &query.TextExpr{Value: "cat"}
	iassert.True(t, !ExactMatch(e, rec, s), "bare word 'cat' should NOT match 'scatter' (not a prefix)")
}
