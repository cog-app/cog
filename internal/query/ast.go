package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Record is a multi-valued key-value map that expressions are matched against.
type Record map[string][]string

// Expr is a parsed query expression that can match records and serialize to a string.
type Expr interface {
	Match(r Record) bool
	String() string
}

// TextExpr matches if any record value contains the term (case-insensitive).
type TextExpr struct {
	Value string
}

func (e *TextExpr) Match(r Record) bool {
	lower := strings.ToLower(e.Value)
	for _, vs := range r {
		for _, v := range vs {
			if strings.Contains(strings.ToLower(v), lower) {
				return true
			}
		}
	}
	return false
}

func (e *TextExpr) String() string { return e.Value }

// PhraseExpr matches if any record value contains the exact phrase (case-insensitive).
type PhraseExpr struct {
	Value string
}

func (e *PhraseExpr) Match(r Record) bool {
	lower := strings.ToLower(e.Value)
	for _, vs := range r {
		for _, v := range vs {
			if strings.Contains(strings.ToLower(v), lower) {
				return true
			}
		}
	}
	return false
}

func (e *PhraseExpr) String() string { return fmt.Sprintf("%q", e.Value) }

// NotExpr negates its inner expression.
type NotExpr struct {
	Inner Expr
}

func (e *NotExpr) Match(r Record) bool { return !e.Inner.Match(r) }
func (e *NotExpr) String() string      { return "NOT " + e.Inner.String() }

// AndExpr requires both sides to match.
type AndExpr struct {
	Left, Right Expr
}

func (e *AndExpr) Match(r Record) bool { return e.Left.Match(r) && e.Right.Match(r) }
func (e *AndExpr) String() string      { return e.Left.String() + " AND " + e.Right.String() }

// OrExpr requires either side to match.
type OrExpr struct {
	Left, Right Expr
}

func (e *OrExpr) Match(r Record) bool { return e.Left.Match(r) || e.Right.Match(r) }
func (e *OrExpr) String() string {
	return "(" + e.Left.String() + " OR " + e.Right.String() + ")"
}

// FieldEqExpr matches record[Field] == Value (case-insensitive).
type FieldEqExpr struct {
	Field string
	Value string
}

func (e *FieldEqExpr) Match(r Record) bool {
	for _, v := range r[e.Field] {
		if strings.EqualFold(v, e.Value) {
			return true
		}
	}
	return false
}

func (e *FieldEqExpr) String() string {
	if strings.ContainsAny(e.Value, " \t\"") {
		return fmt.Sprintf(":%s=%q", e.Field, e.Value)
	}
	return fmt.Sprintf(":%s=%s", e.Field, e.Value)
}

// MatchAllExpr matches every record unconditionally. It is produced by the
// parser when the query consists of a bare '*' token, and is useful as a
// catch-all filter rule ("apply this action to every item").
type MatchAllExpr struct{}

func (*MatchAllExpr) Match(_ Record) bool { return true }
func (*MatchAllExpr) String() string      { return "*" }

// FieldInExpr matches if record[Field] is in the set (case-insensitive).
type FieldInExpr struct {
	Field  string
	Values []string
}

func (e *FieldInExpr) Match(r Record) bool {
	for _, have := range r[e.Field] {
		haveL := strings.ToLower(have)
		for _, want := range e.Values {
			if strings.ToLower(want) == haveL {
				return true
			}
		}
	}
	return false
}

func (e *FieldInExpr) String() string {
	return fmt.Sprintf(":%s IN (%s)", e.Field, strings.Join(e.Values, ", "))
}

// CompareOp is a range comparison operator.
type CompareOp int

const (
	OpGt CompareOp = iota // >
	OpGe                  // >=
	OpLt                  // <
	OpLe                  // <=
)

func (o CompareOp) String() string {
	switch o {
	case OpGt:
		return ">"
	case OpGe:
		return ">="
	case OpLt:
		return "<"
	case OpLe:
		return "<="
	default:
		return "?"
	}
}

// FieldCompareExpr matches when record[Field] compares against Value under Op.
// Comparison is heuristic in the schemaless Go matcher: numeric first, then
// RFC3339 time, then lexical string. The SQL and Bleve evaluators are
// schema-typed and exact.
type FieldCompareExpr struct {
	Field string
	Op    CompareOp
	Value string
}

func (e *FieldCompareExpr) Match(r Record) bool {
	for _, have := range r[e.Field] {
		if compareValues(have, e.Value, e.Op) {
			return true
		}
	}
	return false
}

func (e *FieldCompareExpr) String() string {
	return fmt.Sprintf(":%s%s%s", e.Field, e.Op, e.Value)
}

// compareValues compares two string values heuristically: numeric if both
// parse as floats, else time if both parse as RFC3339, else lexical.
func compareValues(have, want string, op CompareOp) bool {
	if hf, err1 := strconv.ParseFloat(have, 64); err1 == nil {
		if wf, err2 := strconv.ParseFloat(want, 64); err2 == nil {
			return applyOrder(cmpFloat(hf, wf), op)
		}
	}
	if ht, err1 := time.Parse(time.RFC3339, have); err1 == nil {
		if wt, err2 := time.Parse(time.RFC3339, want); err2 == nil {
			return applyOrder(ht.Compare(wt), op)
		}
	}
	return applyOrder(strings.Compare(have, want), op)
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// applyOrder maps a -1/0/1 comparison result to the operator's truth.
func applyOrder(cmp int, op CompareOp) bool {
	switch op {
	case OpGt:
		return cmp > 0
	case OpGe:
		return cmp >= 0
	case OpLt:
		return cmp < 0
	case OpLe:
		return cmp <= 0
	}
	return false
}
