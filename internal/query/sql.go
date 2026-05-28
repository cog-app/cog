package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Compile renders Expr into a parameterized WHERE fragment (no leading
// "WHERE"), bind args in order, and the join names to add. A nil Expr or
// MatchAllExpr yields ("", nil, nil, nil).
func Compile(e Expr, s Schema) (string, []any, []string, error) {
	if e == nil {
		return "", nil, nil, nil
	}
	c := &sqlCompiler{schema: s, joins: map[string]struct{}{}}
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
	args   []any
	joins  map[string]struct{}
}

func (c *sqlCompiler) expr(e Expr) (string, error) {
	switch x := e.(type) {
	case nil, *MatchAllExpr:
		return "", nil
	case *TextExpr:
		return c.likeAcrossText(x.Value)
	case *PhraseExpr:
		return c.likeAcrossText(x.Value)
	case *NotExpr:
		inner, err := c.expr(x.Inner)
		if err != nil {
			return "", err
		}
		if inner == "" {
			return "0", nil
		}
		return "NOT (" + inner + ")", nil
	case *AndExpr:
		return c.binary(x.Left, x.Right, " AND ")
	case *OrExpr:
		return c.binary(x.Left, x.Right, " OR ")
	case *FieldEqExpr:
		return c.fieldEq(x.Field, x.Value)
	case *FieldInExpr:
		return c.fieldIn(x.Field, x.Values)
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
			"EXISTS (SELECT 1 FROM %s j JOIN %s v ON v.id = j.%s WHERE j.%s = i.id AND LOWER(v.%s) = LOWER(?))",
			f.Join, f.ValueTable, f.ValueFK, f.JoinFK, f.ValueColumn,
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

func (c *sqlCompiler) likeAcrossText(value string) (string, error) {
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
