package search

import (
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/query"
	"github.com/blevesearch/bleve/v2"
	bleveQuery "github.com/blevesearch/bleve/v2/search/query"
)

// nameBoost weighs a term found in a document's name against one found
// anywhere in it.
const nameBoost = 5

// toBleveQuery converts a parsed query Expr into a Bleve query.
func toBleveQuery(expr query.Expr) bleveQuery.Query {
	switch e := expr.(type) {
	case *query.TextExpr:
		// Match query (tokenized) OR prefix query for partial matches,
		// anywhere, and again on the name: a document named for a word
		// outranks one that only mentions it.
		matchQ := bleve.NewMatchQuery(e.Value)
		prefixQ := bleve.NewPrefixQuery(strings.ToLower(e.Value))
		nameMatch := bleve.NewMatchQuery(e.Value)
		nameMatch.SetField("name")
		nameMatch.SetBoost(nameBoost)
		namePrefix := bleve.NewPrefixQuery(strings.ToLower(e.Value))
		namePrefix.SetField("name")
		namePrefix.SetBoost(nameBoost)
		return bleve.NewDisjunctionQuery(matchQ, prefixQ, nameMatch, namePrefix)

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

	case *query.FieldCompareExpr:
		srcs := reg.prefixed(e.Field)
		if len(srcs) == 0 {
			return bleve.NewMatchNoneQuery()
		}
		ft := reg.fieldType[e.Field]
		qs := make([]bleveQuery.Query, 0, len(srcs))
		for _, src := range srcs {
			field := src + "." + e.Field
			rq := compareRangeQuery(ft, e.Op, e.Value, field)
			if rq == nil {
				continue
			}
			qs = append(qs, rq)
		}
		if len(qs) == 0 {
			return bleve.NewMatchNoneQuery()
		}
		if len(qs) == 1 {
			return qs[0]
		}
		return bleve.NewDisjunctionQuery(qs...)

	default:
		return bleve.NewMatchAllQuery()
	}
}

// compareRangeQuery builds a bleve range query for a compare expression on a
// given prefixed field. Number -> NumericRangeQuery; DateText -> DateRangeQuery.
// Returns nil for unsupported types or unparseable values (caller treats as
// match-none).
func compareRangeQuery(ft query.FieldType, op query.CompareOp, value, field string) bleveQuery.Query {
	switch ft {
	case query.Number:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil
		}
		min, max, minInc, maxInc := numericBounds(n, op)
		q := bleve.NewNumericRangeInclusiveQuery(min, max, minInc, maxInc)
		q.SetField(field)
		return q
	case query.DateText:
		// value is RFC3339 (relative already resolved upstream) or date-only.
		ts, ok := parseDateForBleve(value)
		if !ok {
			return nil
		}
		min, max, minInc, maxInc := dateBounds(ts, op)
		q := bleve.NewDateRangeInclusiveQuery(min, max, minInc, maxInc)
		q.SetField(field)
		return q
	default:
		return nil
	}
}

func parseDateForBleve(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// numericBounds maps an operator+value to (min,max,minInclusive,maxInclusive)
// for a NumericRangeInclusiveQuery. Unbounded sides use nil.
func numericBounds(n float64, op query.CompareOp) (*float64, *float64, *bool, *bool) {
	tru, fals := true, false
	switch op {
	case query.OpGt:
		return &n, nil, &fals, nil
	case query.OpGe:
		return &n, nil, &tru, nil
	case query.OpLt:
		return nil, &n, nil, &fals
	case query.OpLe:
		return nil, &n, nil, &tru
	}
	return nil, nil, nil, nil
}

// dateBounds maps an operator+time to (start,end,startInclusive,endInclusive)
// for a DateRangeInclusiveQuery. The unbounded side uses a zero time.Time,
// which bleve interprets as open (no bound) via IsZero() checks in parseEndpoints.
func dateBounds(t time.Time, op query.CompareOp) (time.Time, time.Time, *bool, *bool) {
	tru, fals := true, false
	var zero time.Time
	switch op {
	case query.OpGt:
		return t, zero, &fals, nil
	case query.OpGe:
		return t, zero, &tru, nil
	case query.OpLt:
		return zero, t, nil, &fals
	case query.OpLe:
		return zero, t, nil, &tru
	}
	return zero, zero, nil, nil
}
