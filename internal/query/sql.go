package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Compile renders Expr into a parameterized WHERE fragment (no leading
// "WHERE"), bind args in order, and the join names to add. A nil Expr or
// MatchAllExpr yields ("", nil, nil, nil).
func Compile(e Expr, s Schema) (string, []any, []string, error) {
	if e == nil {
		return "", nil, nil, nil
	}
	alias := s.PrimaryAlias
	if alias == "" {
		alias = "i"
	}
	c := &sqlCompiler{schema: s, alias: alias, joins: map[string]struct{}{}}
	w, err := c.expr(e)
	if err != nil {
		return "", nil, nil, err
	}
	var joins []string
	if len(c.joins) > 0 {
		joins = make([]string, 0, len(c.joins))
		for j := range c.joins {
			joins = append(joins, j)
		}
		sort.Strings(joins)
	}
	return w, c.args, joins, nil
}

type sqlCompiler struct {
	schema Schema
	alias  string
	args   []any
	joins  map[string]struct{}
}

func (c *sqlCompiler) expr(e Expr) (string, error) {
	switch x := e.(type) {
	case nil, *MatchAllExpr:
		return "", nil
	case *TextExpr:
		return c.likeTerm(x.Value)
	case *PhraseExpr:
		return c.likeAllWords(x.Value)
	case *NotExpr:
		inner, err := c.expr(x.Inner)
		if err != nil {
			return "", err
		}
		if inner == "" {
			return "0", nil
		}
		// A field from an outer join is NULL where the row has no value
		// (an item whose feed is in no folder), and so is a comparison with
		// it; NOT NULL is NULL, which would leave out the very rows that
		// lack the value. Not matching is false, as ExactMatch has it.
		return "NOT COALESCE(" + inner + ", 0)", nil
	case *AndExpr:
		return c.binary(x.Left, x.Right, " AND ")
	case *OrExpr:
		return c.binary(x.Left, x.Right, " OR ")
	case *FieldEqExpr:
		return c.fieldEq(x.Field, x.Value)
	case *FieldInExpr:
		return c.fieldIn(x.Field, x.Values)
	case *FieldCompareExpr:
		return c.fieldCompare(x.Field, x.Op, x.Value)
	default:
		return "", fmt.Errorf("query: unsupported expression %T", e)
	}
}

func (c *sqlCompiler) binary(l, r Expr, op string) (string, error) {
	ls, err := c.expr(l)
	if err != nil {
		return "", err
	}
	rs, err := c.expr(r)
	if err != nil {
		return "", err
	}
	switch {
	case ls == "" && rs == "":
		return "", nil
	case ls == "":
		return rs, nil
	case rs == "":
		return ls, nil
	}
	return "(" + ls + op + rs + ")", nil
}

func (c *sqlCompiler) requireJoins(j []string) {
	for _, name := range j {
		if _, ok := c.schema.Joins[name]; !ok {
			continue // defensive; Schema.Validate catches at registration
		}
		c.joins[name] = struct{}{}
	}
}

func (c *sqlCompiler) fieldEq(field, value string) (string, error) {
	f, ok := c.schema.Fields[field]
	if !ok {
		return "0", nil // match nothing, like bleve MatchNone for unknown fields
	}
	c.requireJoins(f.Joins)
	switch f.Type {
	case Text, Keyword:
		c.args = append(c.args, value)
		return fmt.Sprintf("LOWER(%s) = LOWER(?)", f.Expr), nil
	case Bool:
		v, err := parseBool(value)
		if err != nil {
			return "", fmt.Errorf("query: field %q: %w", field, err)
		}
		c.args = append(c.args, v)
		return fmt.Sprintf("%s = ?", f.Expr), nil
	case Number:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", fmt.Errorf("query: field %q: %w", field, err)
		}
		c.args = append(c.args, n)
		return fmt.Sprintf("%s = ?", f.Expr), nil
	case DateText:
		c.args = append(c.args, value)
		return fmt.Sprintf("strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', %s, 'unixepoch') = ?", f.Expr), nil
	case ManyToMany:
		c.args = append(c.args, value)
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM %s j JOIN %s v ON v.id = j.%s WHERE j.%s = %s.id AND LOWER(v.%s) = LOWER(?))",
			f.Join, f.ValueTable, f.ValueFK, f.JoinFK, c.alias, f.ValueColumn,
		), nil
	case EnumPredicate:
		pred, ok := f.Values[value]
		if !ok {
			return "0", nil // unknown enum value -> match nothing
		}
		return pred, nil
	default:
		return "", fmt.Errorf("query: field %q has unsupported type %d", field, f.Type)
	}
}

func parseBool(s string) (int, error) {
	switch strings.ToLower(s) {
	case "1", "true", "yes", "y":
		return 1, nil
	case "0", "false", "no", "n":
		return 0, nil
	}
	return 0, fmt.Errorf("not a boolean: %q", s)
}

func (c *sqlCompiler) fieldIn(field string, values []string) (string, error) {
	if len(values) == 0 {
		return "0", nil
	}
	if _, ok := c.schema.Fields[field]; !ok {
		return "0", nil // unknown field -> match nothing
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		p, err := c.fieldEq(field, v)
		if err != nil {
			return "", err
		}
		parts = append(parts, p)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

func (c *sqlCompiler) fieldCompare(field string, op CompareOp, value string) (string, error) {
	f, ok := c.schema.Fields[field]
	if !ok {
		return "0", nil // unknown field -> match nothing
	}
	c.requireJoins(f.Joins)
	switch f.Type {
	case Number:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "0", nil // unparseable number -> match nothing
		}
		c.args = append(c.args, n)
		return fmt.Sprintf("%s %s ?", f.Expr, op.String()), nil
	case DateText:
		// published_at is stored as unix seconds; bind the resolved unix value.
		// Relative values are already resolved to RFC3339 by ResolveRelativeDates.
		unix, ok := parseDateToUnix(value)
		if !ok {
			return "0", nil
		}
		c.args = append(c.args, unix)
		return fmt.Sprintf("%s %s ?", f.Expr, op.String()), nil
	default:
		return "", fmt.Errorf("query: field %q (type %d) does not support range comparison", field, f.Type)
	}
}

// parseDateToUnix accepts RFC3339 or date-only and returns unix seconds.
func parseDateToUnix(s string) (int64, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Unix(), true
	}
	return 0, false
}

// orLikeAcrossText ORs LOWER(<Expr>) LIKE ? over all Text-typed fields
// (DefaultTextFields). Returns "0" if there are no text fields.
// Single field → no wrapping parens.
func (c *sqlCompiler) orLikeAcrossText(value string) (string, error) {
	names := c.schema.DefaultTextFields()
	if len(names) == 0 {
		return "0", nil
	}
	like := "%" + strings.ToLower(value) + "%"
	parts := make([]string, 0, len(names))
	for _, name := range names {
		f := c.schema.Fields[name]
		c.requireJoins(f.Joins)
		parts = append(parts, fmt.Sprintf("LOWER(%s) LIKE ?", f.Expr))
		c.args = append(c.args, like)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " OR ") + ")", nil
}

// likeTerm handles a bare-word TextExpr: OR-LIKE across Text fields only.
func (c *sqlCompiler) likeTerm(value string) (string, error) {
	return c.orLikeAcrossText(value)
}

// likeAllWords handles a PhraseExpr: AND of per-word OR-LIKE scans across
// Text fields. Every word must match somewhere in the text fields (superset
// of exact phrase match).
func (c *sqlCompiler) likeAllWords(phrase string) (string, error) {
	words := strings.Fields(phrase)
	if len(words) == 0 {
		return "0", nil
	}
	parts := make([]string, 0, len(words))
	for _, word := range words {
		frag, err := c.orLikeAcrossText(word)
		if err != nil {
			return "", err
		}
		parts = append(parts, frag)
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", nil
}
