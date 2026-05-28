package query

import (
	"regexp"
	"strconv"
	"time"
)

var relativeRE = regexp.MustCompile(`^([+-]?\d+)([dhm])$`)

// ParseTimeValue converts a date value (date-only "2006-01-02", RFC3339, or
// relative "-7d"/"24h"/"-30m") to unix seconds, using now for relative values.
// Returns ok=false if the string is not a recognized date form.
func ParseTimeValue(s string, now time.Time) (int64, bool) {
	if m := relativeRE.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return 0, false
		}
		var d time.Duration
		switch m[2] {
		case "d":
			d = time.Duration(n) * 24 * time.Hour
		case "h":
			d = time.Duration(n) * time.Hour
		case "m":
			d = time.Duration(n) * time.Minute
		}
		return now.Add(d).Unix(), true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), true
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC().Unix(), true
	}
	return 0, false
}

// ResolveRelativeDates rewrites relative date values on DateText-field compare
// expressions to absolute RFC3339, using now. Absolute dates, numbers, and
// non-compare expressions pass through unchanged. Returns a new tree; inputs
// are not mutated in place beyond the rewritten node values.
func ResolveRelativeDates(e Expr, s Schema, now time.Time) Expr {
	switch x := e.(type) {
	case *FieldCompareExpr:
		f, ok := s.Fields[x.Field]
		if !ok || f.Type != DateText {
			return x
		}
		if relativeRE.MatchString(x.Value) {
			if unix, ok := ParseTimeValue(x.Value, now); ok {
				return &FieldCompareExpr{Field: x.Field, Op: x.Op, Value: time.Unix(unix, 0).UTC().Format(time.RFC3339)}
			}
		}
		return x
	case *NotExpr:
		return &NotExpr{Inner: ResolveRelativeDates(x.Inner, s, now)}
	case *AndExpr:
		return &AndExpr{Left: ResolveRelativeDates(x.Left, s, now), Right: ResolveRelativeDates(x.Right, s, now)}
	case *OrExpr:
		return &OrExpr{Left: ResolveRelativeDates(x.Left, s, now), Right: ResolveRelativeDates(x.Right, s, now)}
	default:
		return e
	}
}
