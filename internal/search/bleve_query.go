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
