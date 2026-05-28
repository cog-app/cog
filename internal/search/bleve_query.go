package search

import (
	"strings"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2"
	bleveQuery "github.com/blevesearch/bleve/v2/search/query"
)

// toBleveQuery converts a parsed query Expr into a Bleve query.
func toBleveQuery(expr query.Expr) bleveQuery.Query {
	switch e := expr.(type) {
	case *query.TextExpr:
		// Match query (tokenized) OR prefix query for partial matches.
		matchQ := bleve.NewMatchQuery(e.Value)
		prefixQ := bleve.NewPrefixQuery(strings.ToLower(e.Value))
		return bleve.NewDisjunctionQuery(matchQ, prefixQ)

	case *query.PhraseExpr:
		return bleve.NewMatchPhraseQuery(e.Value)

	case *query.NotExpr:
		inner := toBleveQuery(e.Inner)
		matchAll := bleve.NewMatchAllQuery()
		boolQ := bleve.NewBooleanQuery()
		boolQ.AddMust(matchAll)
		boolQ.AddMustNot(inner)
		return boolQ

	case *query.AndExpr:
		left := toBleveQuery(e.Left)
		right := toBleveQuery(e.Right)
		return bleve.NewConjunctionQuery(left, right)

	case *query.OrExpr:
		left := toBleveQuery(e.Left)
		right := toBleveQuery(e.Right)
		return bleve.NewDisjunctionQuery(left, right)

	case *query.FieldEqExpr:
		q := bleve.NewTermQuery(strings.ToLower(e.Value))
		q.SetField(e.Field)
		return q

	case *query.FieldInExpr:
		queries := make([]bleveQuery.Query, len(e.Values))
		for i, v := range e.Values {
			q := bleve.NewTermQuery(strings.ToLower(v))
			q.SetField(e.Field)
			queries[i] = q
		}
		return bleve.NewDisjunctionQuery(queries...)

	default:
		return bleve.NewMatchAllQuery()
	}
}

// toBleveQueryWithSchema is the schema-aware variant of toBleveQuery. Field
// references (FieldEqExpr / FieldInExpr) get fanned out across every source
// that declares the field, since the bleve index uses <source>.<field>
// keys. Bare-word and phrase queries continue to match against bleve's
// default text fields (no SetField).
func toBleveQueryWithSchema(expr query.Expr, reg *schemaRegistry) bleveQuery.Query {
	switch e := expr.(type) {
	case *query.TextExpr:
		matchQ := bleve.NewMatchQuery(e.Value)
		prefixQ := bleve.NewPrefixQuery(strings.ToLower(e.Value))
		return bleve.NewDisjunctionQuery(matchQ, prefixQ)

	case *query.PhraseExpr:
		return bleve.NewMatchPhraseQuery(e.Value)

	case *query.NotExpr:
		inner := toBleveQueryWithSchema(e.Inner, reg)
		matchAll := bleve.NewMatchAllQuery()
		boolQ := bleve.NewBooleanQuery()
		boolQ.AddMust(matchAll)
		boolQ.AddMustNot(inner)
		return boolQ

	case *query.AndExpr:
		return bleve.NewConjunctionQuery(toBleveQueryWithSchema(e.Left, reg), toBleveQueryWithSchema(e.Right, reg))

	case *query.OrExpr:
		return bleve.NewDisjunctionQuery(toBleveQueryWithSchema(e.Left, reg), toBleveQueryWithSchema(e.Right, reg))

	case *query.FieldEqExpr:
		srcs := reg.prefixed(e.Field)
		if len(srcs) == 0 {
			return bleve.NewMatchNoneQuery()
		}
		if len(srcs) == 1 {
			q := bleve.NewTermQuery(strings.ToLower(e.Value))
			q.SetField(srcs[0] + "." + e.Field)
			return q
		}
		qs := make([]bleveQuery.Query, len(srcs))
		for i, src := range srcs {
			q := bleve.NewTermQuery(strings.ToLower(e.Value))
			q.SetField(src + "." + e.Field)
			qs[i] = q
		}
		return bleve.NewDisjunctionQuery(qs...)

	case *query.FieldInExpr:
		srcs := reg.prefixed(e.Field)
		if len(srcs) == 0 {
			return bleve.NewMatchNoneQuery()
		}
		var qs []bleveQuery.Query
		for _, v := range e.Values {
			for _, src := range srcs {
				q := bleve.NewTermQuery(strings.ToLower(v))
				q.SetField(src + "." + e.Field)
				qs = append(qs, q)
			}
		}
		return bleve.NewDisjunctionQuery(qs...)

	default:
		return bleve.NewMatchAllQuery()
	}
}
