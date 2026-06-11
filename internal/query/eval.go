package query

import "slices"

import "strings"

// Tokenizer splits a string into normalized tokens. Injected so internal/query
// stays free of any search-engine dependency; internal/search supplies one
// backed by bleve's analyzer for exact parity.
type Tokenizer func(string) []string

// Eval reports whether e matches record r under schema s, using the given
// tokenizer for text/phrase matching. Semantics mirror the bleve query built
// in internal/search: a bare word matches a Type=Text field whose tokens
// contain the analyzed term OR a token prefixed by the raw-lowercased term;
// a phrase matches a consecutive analyzed-token subsequence of a Type=Text
// field. All non-text nodes evaluate identically to (Expr).Match.
func Eval(e Expr, r Record, s Schema, tok Tokenizer) bool {
	switch x := e.(type) {
	case *TextExpr:
		return matchTerm(x.Value, r, s, tok)
	case *PhraseExpr:
		return matchPhrase(x.Value, r, s, tok)
	case *NotExpr:
		return !Eval(x.Inner, r, s, tok)
	case *AndExpr:
		return Eval(x.Left, r, s, tok) && Eval(x.Right, r, s, tok)
	case *OrExpr:
		return Eval(x.Left, r, s, tok) || Eval(x.Right, r, s, tok)
	case *MatchAllExpr:
		return true
	default: // FieldEqExpr, FieldInExpr, FieldCompareExpr
		return e.Match(r)
	}
}

func matchTerm(term string, r Record, s Schema, tok Tokenizer) bool {
	terms := tok(term)
	prefix := strings.ToLower(term)
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			toks := tok(v)
			// match: any analyzed term token present
			for _, qt := range terms {
				if slices.Contains(toks, qt) {
					return true
				}
			}
			// prefix: any doc token starts with the raw lowercased term
			for _, dt := range toks {
				if strings.HasPrefix(dt, prefix) {
					return true
				}
			}
		}
	}
	return false
}

func matchPhrase(phrase string, r Record, s Schema, tok Tokenizer) bool {
	want := tok(phrase)
	if len(want) == 0 {
		return false
	}
	for _, field := range s.DefaultTextFields() {
		for _, v := range r[field] {
			if containsSubsequence(tok(v), want) {
				return true
			}
		}
	}
	return false
}

func containsSubsequence(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for i := 0; i+len(want) <= len(have); i++ {
		ok := true
		for j := range want {
			if have[i+j] != want[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
