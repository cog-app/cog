package search

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/registry"
)

var stdAnalyzer analysis.Analyzer

func init() {
	a, err := registry.NewCache().AnalyzerNamed(standard.Name)
	if err != nil {
		panic(fmt.Sprintf("search: standard analyzer unavailable: %v", err))
	}
	stdAnalyzer = a
}

// StandardTokenize runs bleve's standard analyzer (UAX#29 segmentation +
// lowercase + English stop-word removal) and returns the token terms in order.
// This is the SAME analysis the bleve index applies to Type=Text fields, so a
// Go match using these tokens agrees with global search exactly.
func StandardTokenize(s string) []string {
	stream := stdAnalyzer.Analyze([]byte(s))
	out := make([]string, len(stream))
	for i, t := range stream {
		out[i] = string(t.Term)
	}
	return out
}

// ExactMatch evaluates expr against rec with bleve-exact text semantics.
func ExactMatch(e query.Expr, rec query.Record, s query.Schema) bool {
	return query.Eval(e, rec, s, StandardTokenize)
}
