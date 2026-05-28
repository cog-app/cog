# Range Operator for Ordered Fields (published, duration)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use `- [ ]` checkboxes.

**Goal:** Add `>`, `>=`, `<`, `<=` comparison operators to the cog-grammar for ordered fields. `published` (DateText) accepts `YYYY-MM-DD`, RFC3339, and relative (`-7d`/`-24h`/`-30m`) values; `duration` (Number) accepts numeric seconds. Works across all three evaluators: Go `Match`, Bleve, SQL `Compile`.

**Architecture:** New lexer tokens `< > <= >=` → new AST node `FieldCompareExpr{Field, Op, Value}`. A schema-aware pre-pass `ResolveRelativeDates(expr, schema, now)` rewrites relative date values to absolute RFC3339 *before* evaluation, so evaluators stay clock-free and testable. SQL binds parsed unix-int64 / float; Bleve emits date/numeric range queries; Go `Match` compares heuristically (numeric → RFC3339-time → lexical).

**Tech Stack:** Go, SQLite, bleve v2, `internal/query`, `plugin/feed`.

## Conventions
- TDD each task. Tests in the package under change.
- WE DO NOT SWALLOW ERRORS. Wrap+return or `slog`.
- `go build ./...`, `go vet`, `go test ./internal/query ./internal/search ./plugin/feed` per task; `go tool verify` at end.
- Commit per task. IGNORE stale LSP feed* arg-count diagnostics — trust `go build`.

## Value formats (published / DateText)
- `2026-05-28` — date-only, midnight UTC.
- `2026-05-28T14:30:00Z` — RFC3339.
- `-7d`, `-24h`, `-30m` — relative: now minus duration. Units: `d` (days), `h` (hours), `m` (minutes). Leading `-` = past; `+`/none = future (`7d` = 7 days from now).

All resolve to a unix-second int64 for comparison. `duration` (Number) values are plain numbers (seconds) parsed as float64.

---

## Task 1: Lexer + parser + AST node + Go Match

**Files:** `internal/query/token.go`, `internal/query/parser.go`, `internal/query/ast.go`, `internal/query/query_test.go`, `internal/query/ast_test.go` (new if absent).

### Step 1: Failing parser tests — append to `internal/query/query_test.go`:

```go
func TestParseFieldCompareOps(t *testing.T) {
	cases := []struct {
		in    string
		field string
		op    CompareOp
		val   string
	}{
		{":published>2026-01-01", "published", OpGt, "2026-01-01"},
		{":published>=2026-01-01", "published", OpGe, "2026-01-01"},
		{":published<2026-01-01", "published", OpLt, "2026-01-01"},
		{":published<=2026-01-01", "published", OpLe, "2026-01-01"},
		{":duration>600", "duration", OpGt, "600"},
		{"published>-7d", "published", OpGt, "-7d"}, // shorthand form
	}
	for _, c := range cases {
		expr, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		fc, ok := expr.(*FieldCompareExpr)
		if !ok {
			t.Fatalf("%s: got %T, want *FieldCompareExpr", c.in, expr)
		}
		if fc.Field != c.field || fc.Op != c.op || fc.Value != c.val {
			t.Fatalf("%s: got {%s %v %q}", c.in, fc.Field, fc.Op, fc.Value)
		}
	}
}

func TestParseCompareComposes(t *testing.T) {
	// between: published >= A AND published < B
	expr, err := Parse(":published>=2026-01-01 :published<2026-02-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := expr.(*AndExpr); !ok {
		t.Fatalf("got %T, want *AndExpr", expr)
	}
}
```

### Step 2: Run `go test ./internal/query` → FAIL (undefined CompareOp/FieldCompareExpr; lexer can't make `>`).

### Step 3: Lexer — `internal/query/token.go`.

Add token kinds after `tokStar`:
```go
	tokGt
	tokGe
	tokLt
	tokLe
```
In `lexer.run()`'s switch, add cases (place before the `default` lexWord case):
```go
		case ch == '>':
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '=' {
				l.emit(tokGe, ">=")
			} else {
				l.emit(tokGt, ">")
			}
		case ch == '<':
			if l.pos+1 < len(l.input) && l.input[l.pos+1] == '=' {
				l.emit(tokLe, "<=")
			} else {
				l.emit(tokLt, "<")
			}
```
In `lexWord`'s break-char set (the ASCII `if ch == ... break` list), add `|| ch == '>' || ch == '<'` so a bare word stops at an operator. This lets `published>-7d` lex as [text:published][gt][text:-7d] — note `-7d` is a normal word (the `-` is not a break char).

### Step 4: AST — `internal/query/ast.go`.

```go
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
```
Add imports `strconv`, `time` to ast.go (`strings`, `fmt` already there).

### Step 5: Parser — `internal/query/parser.go`.

In `parseField()` (the `:field…` path), after reading the field name, the switch currently handles `tokEquals` and `IN`. Add compare-op cases:
```go
	case tokGt, tokGe, tokLt, tokLe:
		op := compareOpFor(p.next().kind)
		val, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		return &FieldCompareExpr{Field: field.val, Op: op, Value: val}, nil
```
Add helper:
```go
func compareOpFor(k tokenKind) CompareOp {
	switch k {
	case tokGt:
		return OpGt
	case tokGe:
		return OpGe
	case tokLt:
		return OpLt
	default:
		return OpLe
	}
}
```

In `parsePrimary()`'s `tokText` branch, the shorthand lookahead currently only triggers on `tokColon`. Add a sibling shorthand: `field>value`. After the existing colon-shorthand check, add:
```go
		if k := p.peekAt(1).kind; k == tokGt || k == tokGe || k == tokLt || k == tokLe {
			return p.parseCompareShorthand()
		}
```
And implement:
```go
// parseCompareShorthand parses "field>value" etc. as a FieldCompareExpr.
func (p *parser) parseCompareShorthand() (Expr, error) {
	field := p.next() // field name
	op := compareOpFor(p.next().kind)
	val, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &FieldCompareExpr{Field: field.val, Op: op, Value: val}, nil
}
```
Also extend `isUnaryStart` / value parsing if needed so `-7d` parses as a value: `parseValue` accepts `tokText`, and `-7d` lexes as `tokText` (the `-` is not special), so it works. Verify `-7d` is one `tokText` token (it is — `-`, digits, `d` are all word chars; `<`/`>` are the only new break chars).

### Step 6: Match test — append to `internal/query/query_test.go`:

```go
func TestFieldCompareMatchNumeric(t *testing.T) {
	r := Record{"duration": {"600"}}
	if !(&FieldCompareExpr{Field: "duration", Op: OpGt, Value: "300"}).Match(r) {
		t.Fatal("600 > 300 should match")
	}
	if (&FieldCompareExpr{Field: "duration", Op: OpLt, Value: "300"}).Match(r) {
		t.Fatal("600 < 300 should not match")
	}
}

func TestFieldCompareMatchTime(t *testing.T) {
	r := Record{"published": {"2026-05-28T00:00:00Z"}}
	if !(&FieldCompareExpr{Field: "published", Op: OpGe, Value: "2026-01-01T00:00:00Z"}).Match(r) {
		t.Fatal("May >= Jan should match")
	}
	if (&FieldCompareExpr{Field: "published", Op: OpLt, Value: "2026-01-01T00:00:00Z"}).Match(r) {
		t.Fatal("May < Jan should not match")
	}
}
```

### Step 7: `go test ./internal/query` → PASS. `go build ./...`, `go vet ./internal/query`.

### Step 8: Commit
```bash
git add internal/query/token.go internal/query/parser.go internal/query/ast.go internal/query/query_test.go
git commit -m "query: add range comparison operators (> >= < <=) to grammar"
```

---

## Task 2: Relative-date resolution pre-pass

**Files:** `internal/query/resolve.go` (new), `internal/query/resolve_test.go` (new).

Resolves relative date values (`-7d` etc.) on DateText-field compare exprs to absolute RFC3339, using an injected `now`. Numbers untouched. Runs before evaluation so evaluators are clock-free.

### Step 1: Failing test — `internal/query/resolve_test.go`:

```go
package query

import (
	"testing"
	"time"
)

func resolveTestSchema() Schema {
	return Schema{Fields: map[string]FieldSpec{
		"published": {Type: DateText, Expr: "i.published_at"},
		"duration":  {Type: Number, Expr: "i.duration_sec"},
	}}
}

func TestResolveRelativeDays(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	expr := &FieldCompareExpr{Field: "published", Op: OpGt, Value: "-7d"}
	out := ResolveRelativeDates(expr, resolveTestSchema(), now)
	fc := out.(*FieldCompareExpr)
	want := now.AddDate(0, 0, -7).UTC().Format(time.RFC3339)
	if fc.Value != want {
		t.Fatalf("got %q want %q", fc.Value, want)
	}
}

func TestResolveLeavesAbsoluteAndNumbers(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	// Absolute date untouched.
	a := ResolveRelativeDates(&FieldCompareExpr{Field: "published", Op: OpGt, Value: "2026-01-01"}, resolveTestSchema(), now).(*FieldCompareExpr)
	if a.Value != "2026-01-01" {
		t.Fatalf("absolute date mutated: %q", a.Value)
	}
	// Number field: -7d is nonsense for a number; leave as-is (SQL will reject).
	n := ResolveRelativeDates(&FieldCompareExpr{Field: "duration", Op: OpGt, Value: "600"}, resolveTestSchema(), now).(*FieldCompareExpr)
	if n.Value != "600" {
		t.Fatalf("number mutated: %q", n.Value)
	}
}

func TestResolveRecursesAndOr(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	expr := &AndExpr{
		Left:  &FieldCompareExpr{Field: "published", Op: OpGe, Value: "-1d"},
		Right: &TextExpr{Value: "x"},
	}
	out := ResolveRelativeDates(expr, resolveTestSchema(), now).(*AndExpr)
	if out.Left.(*FieldCompareExpr).Value == "-1d" {
		t.Fatal("relative not resolved under AndExpr")
	}
}
```

### Step 2: Run `go test ./internal/query -run TestResolve` → FAIL.

### Step 3: Implement `internal/query/resolve.go`:

```go
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
		if m := relativeRE.MatchString(x.Value); m {
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
```

### Step 4: `go test ./internal/query` → PASS. Build + vet.

### Step 5: Commit
```bash
git add internal/query/resolve.go internal/query/resolve_test.go
git commit -m "query: relative-date resolution pre-pass (ParseTimeValue, ResolveRelativeDates)"
```

---

## Task 3: SQL compiler — FieldCompareExpr

**Files:** `internal/query/sql.go`, `internal/query/sql_test.go`.

### Step 1: Failing tests — append to `internal/query/sql_test.go`. Add `published`+`duration` to `compileTestSchema` if absent (it has `published` DateText and `duration` Number already per the Task-1 schema in this plan; verify the actual `compileTestSchema` in the file and add fields if missing):

```go
func TestCompileDateRange(t *testing.T) {
	w, args, _, err := Compile(&FieldCompareExpr{Field: "published", Op: OpGe, Value: "2026-01-01T00:00:00Z"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "i.published_at >= ?" {
		t.Fatalf("where=%q", w)
	}
	// Bound arg is unix seconds.
	want := int64(1767225600) // 2026-01-01T00:00:00Z
	if len(args) != 1 || args[0] != want {
		t.Fatalf("args=%v want [%d]", args, want)
	}
}

func TestCompileNumberRange(t *testing.T) {
	w, args, _, err := Compile(&FieldCompareExpr{Field: "duration", Op: OpLt, Value: "600"}, compileTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	if w != "i.duration_sec < ?" {
		t.Fatalf("where=%q", w)
	}
	if len(args) != 1 || args[0] != float64(600) {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileRangeUnknownFieldMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldCompareExpr{Field: "ghost", Op: OpGt, Value: "1"}, compileTestSchema)
	if err != nil {
		t.Fatalf("unknown field should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing, got %q", w)
	}
}

func TestCompileRangeOnTextFieldErrors(t *testing.T) {
	// Range on a non-ordered field (title is Text) is a user error worth surfacing.
	_, _, _, err := Compile(&FieldCompareExpr{Field: "title", Op: OpGt, Value: "x"}, compileTestSchema)
	if err == nil {
		t.Fatal("range on Text field should error")
	}
}

func TestCompileDateRangeBadValueMatchNothing(t *testing.T) {
	w, _, _, err := Compile(&FieldCompareExpr{Field: "published", Op: OpGt, Value: "not-a-date"}, compileTestSchema)
	if err != nil {
		t.Fatalf("bad date should not error: %v", err)
	}
	if w != "0" {
		t.Fatalf("want match-nothing, got %q", w)
	}
}
```

Confirm `compileTestSchema` has `published` (DateText, `i.published_at`) and `duration` (Number, `i.duration_sec`). If it lacks `duration`, add it.

### Step 2: Run `go test ./internal/query -run TestCompile` → FAIL.

### Step 3: Implement in `internal/query/sql.go`.

In `(*sqlCompiler).expr`, add a case:
```go
	case *FieldCompareExpr:
		return c.fieldCompare(x.Field, x.Op, x.Value)
```
Add the method:
```go
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
```
Add imports `strconv`, `time` to sql.go if not present (`fmt`, `sort`, `strings` already there).

Note: `op.String()` returns `>`/`>=`/`<`/`<=` — directly usable in SQL.

### Step 4: `go test ./internal/query` → PASS. Build + vet.

### Step 5: Commit
```bash
git add internal/query/sql.go internal/query/sql_test.go
git commit -m "query: SQL compile for range comparisons (date unix + numeric)"
```

---

## Task 4: Bleve — FieldCompareExpr range queries

**Files:** `internal/search/bleve_query.go`, `internal/search/bleve_query_schema_test.go`.

### Step 1: Failing test — append to `internal/search/bleve_query_schema_test.go`:

```go
func TestToBleveQueryCompareNumeric(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed": {Fields: map[string]query.FieldSpec{"duration": {Type: query.Number, Expr: "i.duration_sec"}}},
	}
	reg := newSchemaRegistry(schemas)
	q := toBleveQueryWithSchema(&query.FieldCompareExpr{Field: "duration", Op: query.OpGt, Value: "600"}, reg)
	s := stringifyBleveQuery(q)
	if !strings.Contains(s, "feed.duration") {
		t.Fatalf("expected feed.duration numeric range, got %q", s)
	}
}

func TestToBleveQueryCompareDate(t *testing.T) {
	schemas := map[string]*query.Schema{
		"feed": {Fields: map[string]query.FieldSpec{"published": {Type: query.DateText, Expr: "i.published_at"}}},
	}
	reg := newSchemaRegistry(schemas)
	q := toBleveQueryWithSchema(&query.FieldCompareExpr{Field: "published", Op: query.OpGe, Value: "2026-01-01T00:00:00Z"}, reg)
	if q == nil {
		t.Fatal("nil query")
	}
	s := stringifyBleveQuery(q)
	if !strings.Contains(s, "feed.published") {
		t.Fatalf("expected feed.published date range, got %q", s)
	}
}

func TestToBleveQueryCompareUnknownFieldMatchNone(t *testing.T) {
	reg := newSchemaRegistry(map[string]*query.Schema{})
	q := toBleveQueryWithSchema(&query.FieldCompareExpr{Field: "ghost", Op: query.OpGt, Value: "1"}, reg)
	s := stringifyBleveQuery(q)
	if strings.Contains(s, "ghost") {
		t.Fatalf("unknown field should not appear: %q", s)
	}
}
```

`stringifyBleveQuery` already exists in that test file (uses json.Marshal). The schemaRegistry needs the FieldType to choose numeric vs date — currently `schemaRegistry` only tracks `fieldSources map[string][]string`. EXTEND it to also remember the FieldType per (source, field) so the compare branch can pick the right range query. See Step 3.

### Step 2: Run `go test ./internal/search -run TestToBleveQueryCompare` → FAIL.

### Step 3: Extend `schemaRegistry` (`internal/search/schema_registry.go`) to carry field types.

```go
type schemaRegistry struct {
	fieldSources map[string][]string             // field -> sorted source names
	fieldType    map[string]query.FieldType      // field -> type (first declarer wins; types should agree)
}

func newSchemaRegistry(schemas map[string]*query.Schema) *schemaRegistry {
	r := &schemaRegistry{fieldSources: map[string][]string{}, fieldType: map[string]query.FieldType{}}
	for src, sc := range schemas {
		if sc == nil {
			continue
		}
		for name, f := range sc.Fields {
			r.fieldSources[name] = append(r.fieldSources[name], src)
			if _, ok := r.fieldType[name]; !ok {
				r.fieldType[name] = f.Type
			}
		}
	}
	for k := range r.fieldSources {
		sort.Strings(r.fieldSources[k])
	}
	return r
}
```

### Step 4: Add the `*query.FieldCompareExpr` case to `toBleveQueryWithSchema` (`internal/search/bleve_query.go`):

```go
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
```

Add the helper (same file):
```go
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
```

VERIFY the exact bleve v2 signatures: `bleve.NewNumericRangeInclusiveQuery(min, max *float64, minInclusive, maxInclusive *bool)` and `bleve.NewDateRangeInclusiveQuery(start, end time.Time, startInclusive, endInclusive *bool)`. Adjust the helper return types/usage to match the actual signatures in the vendored bleve version (`grep -rn "func NewNumericRangeInclusiveQuery\|func NewDateRangeInclusiveQuery" $(go env GOMODCACHE)/github.com/blevesearch/bleve/v2@*/`). A zero `time.Time` for the unbounded side may be rejected by bleve — if so, use a very large/small sentinel time or the non-inclusive single-bound constructor. Make the tests pass against the real API; the helper shapes above are a guide, not gospel.

Add imports `strconv`, `time` to bleve_query.go.

### Step 5: `go test ./internal/search` → PASS. Build + vet.

### Step 6: Commit
```bash
git add internal/search/bleve_query.go internal/search/schema_registry.go internal/search/bleve_query_schema_test.go
git commit -m "search: bleve range queries for FieldCompareExpr (numeric + date)"
```

---

## Task 5: Feed wiring — schema duration field, enricher, resolve pre-pass, e2e

**Files:** `plugin/feed/search_schema.go`, `plugin/feed/search_enricher.go`, `plugin/feed/record.go`, `plugin/feed/pages.go`, tests.

### Step 1: Add `duration` to the feed search schema.

In `plugin/feed/search_schema.go` add to `Fields`:
```go
		"duration": {Type: query.Number, Expr: "i.duration_sec"},
```
(`published` already present as DateText.)

### Step 2: Enricher populates `duration`.

In `plugin/feed/search_enricher.go`, the SELECT and the row struct must include `duration_sec`. Add `i.duration_sec` to the query, scan into an int64, and set `fields["duration"] = r.Duration` (as a number — bleve Number field). Read the file to match its row-struct + scan style.

### Step 3: BuildRecord parity (Go matcher / ingest filters).

In `plugin/feed/record.go` `BuildRecord`, add a `duration` entry so Go-side filter rules can range on it:
```go
		"duration": {strconv.FormatInt(it.DurationSec, 10)},
```
Add `strconv` import if needed. (Verify the Item field name for duration — likely `DurationSec int64`.)

### Step 4: Resolve relative dates in the feed query handlers.

Wherever feed parses a user query into an Expr and passes it to `ListItemsForView` (the `Index` handler and `resolveViewItems` playlist/all cases, plus `PlaylistView`), insert a resolve pass right after `query.Parse`:
```go
expr = query.ResolveRelativeDates(expr, *SearchSchema(), time.Now())
```
Only call when `expr != nil`. `SearchSchema()` returns `*query.Schema`; deref for the value param. Add `time` import to pages.go if needed.

Grep the call sites: `grep -n 'query.Parse' plugin/feed/pages.go`. Apply to each that builds an expr for ListItemsForView.

### Step 5: Failing e2e test — `plugin/feed/search_range_test.go` (package feed_test):

```go
package feed_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestFeedSearchPublishedRange(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)
	// old item (2025) and new item (2026)
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	recent := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "old", Type: "text", Title: "OLD-ARTICLE", PublishedAt: old})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "new", Type: "text", Title: "NEW-ARTICLE", PublishedAt: recent})
	require.NoError(t, err)

	// published > 2026-01-01 -> only NEW
	resp := env.Get(t, "/apps/feed/?q="+urlq(":published>2026-01-01"), u.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	if !contains(body, ">NEW-ARTICLE<") {
		t.Errorf("range query missing NEW-ARTICLE")
	}
	if contains(body, ">OLD-ARTICLE<") {
		t.Errorf("range query should exclude OLD-ARTICLE")
	}
	_ = fmt.Sprint
}
```

Provide small helpers `urlq` (url.QueryEscape) and `contains` (strings.Contains) or inline `url.QueryEscape`/`strings.Contains` with proper imports. Match the row-render assertion style used by `TestIndexSearchFindsItemsPastLimit` (`>TITLE<`).

### Step 6: Run + full verify

```
go generate ./...    # if any templ touched (none expected)
go build ./... && go vet ./plugin/feed ./internal/query ./internal/search
go test ./plugin/feed ./internal/query ./internal/search
go tool verify
```
All PASS.

### Step 7: Commit
```bash
git add plugin/feed/search_schema.go plugin/feed/search_enricher.go plugin/feed/record.go plugin/feed/pages.go plugin/feed/search_range_test.go
git commit -m "feed: duration search field + resolve relative dates; published/duration ranges end-to-end"
```

---

## Final verify

- [ ] `go tool verify` → "All checks passed."
- [ ] Manual: `/apps/feed/?q=:published>-7d` shows only recent items; `:duration>600` (if any media) filters; `:published>=2026-01-01 :published<2026-02-01` (January window) works.

## Self-review map
- grammar (lexer/parser/AST/Match) → Task 1
- relative-date resolution → Task 2
- SQL evaluator → Task 3
- Bleve evaluator → Task 4
- feed schema/enricher/record/handlers + e2e → Task 5

## Open verification points
- Exact bleve v2 range-query constructor signatures (Task 4) — grep the module cache; adapt helper shapes.
- `Item` duration field name (`DurationSec`?) for BuildRecord + enricher (Tasks 3/5).
- `compileTestSchema` must include `published` (DateText) + `duration` (Number) (Task 3).
- Confirm `-7d` lexes as a single `tokText` after adding `<`/`>` break chars (Task 1) — it must, since `-` and digits are word chars.
