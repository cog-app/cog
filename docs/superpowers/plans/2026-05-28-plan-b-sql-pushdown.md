# Plan B: SQL Pushdown for Feed (consumes Plan A's `query.Schema`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Prerequisite:** Plan A (`2026-05-28-plan-a-search-schema.md`) must be merged. This plan reuses the same `query.Schema` that Plan A defined for the global bleve index; no new schema types here.

**Goal:** Push the Go-side logic that should live in SQL into SQL: compile cog-grammar queries against `query.Schema` so the feed's search and Playlists run server-side (correctness + perf), and clean up the other smaller Go-implements-what-SQL-can pieces (subscription sort, bulk delete, history mapping, sidebar counts).

**Architecture:** A new `query.Compile(expr, schema) (where, args, joins, err)` walks the AST and emits a parameterized WHERE fragment + bind args + the names of joins to add. `feed.ViewSpec` grows a `Query Expr` field; `Store.ListItemsForView` applies the compiled WHERE + appends the required `Schema.Joins[name]` fragments. `applyQueryFilter` and `sortSubscriptionsForSidebar` are deleted. Other small fixes are independent tasks.

**Tech Stack:** Go, SQLite, `internal/query` (Plan A's Schema), `plugin/feed`.

---

## Conventions

- Compiler tests in `internal/query/sql_test.go` (table-driven per AST node).
- Plugin tests via `cogtest` for the integration path.
- WE DO NOT SWALLOW ERRORS (CLAUDE.md). Wrap+return or `slog`.
- `go generate ./...` after templ edits; `go tool templ fmt .` before commit. IGNORE stale LSP arg-count diagnostics on feed* templates.
- Commit per task.

## File structure (what changes)

- **Create:** `internal/query/sql.go` — `Compile(e Expr, s Schema) (where string, args []any, joins []string, err error)`.
- **Create:** `internal/query/sql_test.go`.
- **Modify:** `plugin/feed/store.go` — `ViewSpec.Query Expr`; `ListItemsForView` compiles and integrates; references the Schema from `feed.SearchSchema()` (a getter the feed plugin exposes — defined in Plan A as the registered Schema; reuse it). `ListSubscriptions` gains `ORDER BY`. `DeleteSubscriptions`/`DeleteFolders` use `WHERE id IN (?,…)`. New `PlaybackHistoryItems`.
- **Modify:** `plugin/feed/pages.go` — `Index`, `PlaylistView`, `resolveViewItems` parse-and-pass; delete `applyQueryFilter` + `sortSubscriptionsForSidebar`. History case loses Go remap.
- **Modify (optional):** `plugin/feed/store.go` — `LoadSidebarCounts` aggregating four counts in one pass.

## Background pain points (recap from the user)

- **`applyQueryFilter` is N+1 and post-LIMIT.** For each candidate item from `ListItemsForView` (up to 500 rows), it issues `GetSubscription` + `GetFolder` + `ItemTags` + `GetDownload` — ~2000 round-trips per page. Worse, filter runs after `LIMIT`, so matches past row 500 are invisible.
- **`sortSubscriptionsForSidebar`** sorts in Go what SQL can ORDER BY.
- **`DeleteSubscriptions`/`DeleteFolders`** loop single-row deletes (one transaction each at minimum).
- **`ListPlaybackHistory` → `resolveViewItems` Go remap** — joined query then field-by-field copy.
- **`buildSidebar` issues four count queries** (`CountUnread`, `CountStarred`, `CountUnreadBySubscription`, `CountUnreadByFolder`) — one aggregated query suffices.

## Compiler IO recap (against Plan A's `Schema`)

```go
// Compile renders Expr into a parameterized WHERE fragment (no leading
// "WHERE"), bind args in order, and the join NAMES to add (caller looks them
// up in schema.Joins to splice into FROM). A nil Expr / MatchAllExpr yields
// ("", nil, nil, nil) — i.e. "no constraint".
func Compile(e Expr, s Schema) (where string, args []any, joins []string, err error)
```

Rules:

- `TextExpr`/`PhraseExpr` → bare-word scan. OR-LIKE across every field whose `Type == Text` (this matches the bleve `_all` behavior already chosen). Each LIKE uses the field's `Expr` (compiler emits `LOWER(<Expr>) LIKE ?` with arg `"%" + lower(value) + "%"`); each field's `Joins` get added.
- `FieldEqExpr` → behavior per `FieldSpec.Type`:
  - `Text`/`Keyword`: `LOWER(<Expr>) = LOWER(?)`.
  - `Bool`: `<Expr> = ?` with arg `0`/`1` (parse the value as bool — accept `0|1|true|false|yes|no`).
  - `Number`: `<Expr> = ?` (arg parsed via strconv; error on bad parse).
  - `DateText`: `strftime('%Y-%m-%dT%H:%M:%SZ', <Expr>, 'unixepoch') = ?`. (Brittle exact-RFC3339-match, matches today's Go semantics.)
  - `ManyToMany`: `EXISTS (SELECT 1 FROM <Join> j JOIN <ValueTable> v ON v.id = j.<ValueFK> WHERE j.<JoinFK> = i.id AND LOWER(v.<ValueColumn>) = LOWER(?))`. Primary alias `i` is fixed by convention (`Schema.PrimaryAlias` is documentary; the compiler hardcodes `i.id` in EXISTS subqueries because every FieldSpec.Expr already references the same alias).
  - `EnumPredicate`: look up `value` in `f.Values` and insert the predicate string verbatim. Unknown value → return an error like `query: field %q has no value %q`.
- `FieldInExpr` → OR-of-`FieldEqExpr`s (delegate per value).
- `NotExpr` → `NOT (<inner>)`; empty inner → `0` (matches the "NOT *" interpretation: never matches).
- `AndExpr`/`OrExpr` → composition. Empty side passes through unchanged.
- `MatchAllExpr` → empty fragment.
- Any unknown field name → `query: unknown field %q` error.

JOINs returned: union of every `FieldSpec.Joins` referenced in the compiled fragment, deduped, sorted for determinism. Caller is responsible for splicing `Schema.Joins[name]` into FROM and avoiding duplicates if the caller has hand-written joins for the same name.

---

## Task 1: SQL compiler

**Files:**
- Create: `internal/query/sql.go`
- Create: `internal/query/sql_test.go`

- [ ] **Step 1: Write the failing tests** — exhaustive per-AST-node table:

```go
package query

import (
	"strings"
	"testing"
)

var compileTestSchema = Schema{
	Fields: map[string]FieldSpec{
		"title":    {Type: Text, Expr: "i.title"},
		"author":   {Type: Text, Expr: "i.author"},
		"type":     {Type: Keyword, Expr: "i.type"},
		"is_read":  {Type: Bool, Expr: "i.is_read"},
		"duration": {Type: Number, Expr: "i.duration_sec"},
		"published": {Type: DateText, Expr: "i.published_at"},
		"folder":   {Type: Keyword, Expr: "f.name", Joins: []string{"folders"}},
		"tag":      {Type: ManyToMany, Join: "item_tags", JoinFK: "item_id", ValueTable: "tags", ValueFK: "tag_id", ValueColumn: "name"},
		"is": {Type: EnumPredicate, Values: map[string]string{
			"unread":     "i.is_read = 0",
			"downloaded": "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')",
		}},
	},
	Joins: map[string]string{"folders": "LEFT JOIN folders f ON f.id = s.folder_id"},
}

func TestCompileTextBareWordOROfLikes(t *testing.T) {
	w, args, _, err := Compile(&TextExpr{Value: "Hello"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(w, "LOWER(i.title) LIKE ?") || !strings.Contains(w, "LOWER(i.author) LIKE ?") {
		t.Fatalf("missing text-field LIKEs: %q", w)
	}
	if len(args) < 2 || args[0] != "%hello%" {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileFieldEqKeyword(t *testing.T) {
	w, args, _, err := Compile(&FieldEqExpr{Field: "type", Value: "Audio"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if w != "LOWER(i.type) = LOWER(?)" { t.Fatalf("%q", w) }
	if len(args) != 1 || args[0] != "Audio" { t.Fatalf("%v", args) }
}

func TestCompileFieldEqFolderReturnsJoin(t *testing.T) {
	_, _, joins, err := Compile(&FieldEqExpr{Field: "folder", Value: "Dev"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if len(joins) != 1 || joins[0] != "folders" {
		t.Fatalf("joins=%v", joins)
	}
}

func TestCompileFieldEqTagEXISTS(t *testing.T) {
	w, _, joins, err := Compile(&FieldEqExpr{Field: "tag", Value: "weekly"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(w, "EXISTS") {
		t.Fatalf("expected EXISTS: %q", w)
	}
	if len(joins) != 0 {
		t.Fatalf("tag must not need caller joins: %v", joins)
	}
}

func TestCompileFieldEqIsDownloaded(t *testing.T) {
	w, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "downloaded"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(w, "EXISTS (SELECT 1 FROM downloads") {
		t.Fatalf("%q", w)
	}
}

func TestCompileFieldEqIsUnknownValue(t *testing.T) {
	_, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: "weird"}, compileTestSchema)
	if err == nil { t.Fatal("expected error for unknown enum value") }
}

func TestCompileBoolField(t *testing.T) {
	w, args, _, err := Compile(&FieldEqExpr{Field: "is_read", Value: "true"}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if w != "i.is_read = ?" || len(args) != 1 || args[0] != 1 {
		t.Fatalf("w=%q args=%v", w, args)
	}
}

func TestCompileAndOrNot(t *testing.T) {
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "type", Value: "audio"},
		Right: &NotExpr{Inner: &FieldEqExpr{Field: "is", Value: "unread"}},
	}
	w, _, _, err := Compile(e, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(w, " AND ") || !strings.Contains(w, "NOT (") {
		t.Fatalf("%q", w)
	}
}

func TestCompileFieldInExpansion(t *testing.T) {
	w, args, _, err := Compile(&FieldInExpr{Field: "type", Values: []string{"audio", "video"}}, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if !strings.Contains(w, " OR ") || len(args) != 2 {
		t.Fatalf("w=%q args=%v", w, args)
	}
}

func TestCompileMatchAllReturnsEmpty(t *testing.T) {
	w, args, joins, err := Compile(&MatchAllExpr{}, compileTestSchema)
	if err != nil || w != "" || args != nil || joins != nil {
		t.Fatalf("got w=%q args=%v joins=%v err=%v", w, args, joins, err)
	}
}

func TestCompileUnknownField(t *testing.T) {
	_, _, _, err := Compile(&FieldEqExpr{Field: "ghost", Value: "x"}, compileTestSchema)
	if err == nil { t.Fatal("expected unknown-field error") }
}

func TestCompileJoinsDedupedAndSorted(t *testing.T) {
	// Two field references to "folder" should yield one "folders" entry.
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "folder", Value: "A"},
		Right: &FieldEqExpr{Field: "folder", Value: "B"},
	}
	_, _, joins, err := Compile(e, compileTestSchema)
	if err != nil { t.Fatal(err) }
	if len(joins) != 1 || joins[0] != "folders" {
		t.Fatalf("joins=%v", joins)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`Compile` undefined).

- [ ] **Step 3: Implement `internal/query/sql.go`**

```go
package query

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func Compile(e Expr, s Schema) (string, []any, []string, error) {
	if e == nil {
		return "", nil, nil, nil
	}
	c := &sqlCompiler{schema: s, joins: map[string]struct{}{}}
	w, err := c.expr(e)
	if err != nil {
		return "", nil, nil, err
	}
	joins := make([]string, 0, len(c.joins))
	for j := range c.joins {
		joins = append(joins, j)
	}
	sort.Strings(joins)
	if len(joins) == 0 {
		joins = nil
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
			// Schema.Validate caught this at registration; defensive only.
			continue
		}
		c.joins[name] = struct{}{}
	}
}

func (c *sqlCompiler) fieldEq(field, value string) (string, error) {
	f, ok := c.schema.Fields[field]
	if !ok {
		return "", fmt.Errorf("query: unknown field %q", field)
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
			return "", fmt.Errorf("query: field %q has no value %q", field, value)
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
```

- [ ] **Step 4: Run — expect PASS**

`go test ./internal/query`.

- [ ] **Step 5: Commit**

```bash
git add internal/query/sql.go internal/query/sql_test.go
git commit -m "query: SQL compiler against Schema"
```

---

## Task 2: Integrate compiled query into `ListItemsForView`

**Files:**
- Modify: `plugin/feed/store.go` — `ViewSpec` gains `Query query.Expr` and (optional) `Schema *query.Schema`; `ListItemsForView` compiles and integrates. To avoid Store↔plugin import cycles, the caller passes the Schema via ViewSpec; the Store stays plugin-agnostic on that point.
- Test: `plugin/feed/store_test.go`.

- [ ] **Step 1: Write failing test**

```go
func TestListItemsForViewWithQuery(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	_, _, _ = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio", Title: "alpha keep me"})
	_, _, _ = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "text", Title: "beta skip me"})

	expr, err := query.Parse("type:audio")
	require.NoError(t, err)
	got, err := s.ListItemsForView(feed.ViewSpec{All: true, Query: expr, Schema: feed.SearchSchema()}, 100)
	require.NoError(t, err)
	if len(got) != 1 || got[0].Title != "alpha keep me" {
		t.Fatalf("got %+v", got)
	}
}
```

(`feed.SearchSchema()` is the exported getter for the Schema registered in Plan A. Add it as `func SearchSchema() *query.Schema` if it does not yet exist.)

- [ ] **Step 2: Run — expect FAIL**.

- [ ] **Step 3: Add fields and integrate in `ListItemsForView`**

```go
type ViewSpec struct {
	All            bool
	Starred        bool
	SubscriptionID sql.NullInt64
	FolderID       sql.NullInt64
	TagName        string
	HideRead       bool
	Query          query.Expr   // optional cog-grammar filter
	Schema         *query.Schema // required when Query != nil
}
```

In `ListItemsForView`, after the existing per-kind WHERE is built:

```go
if spec.Query != nil {
	if spec.Schema == nil {
		return nil, fmt.Errorf("list items for view: Query set but Schema is nil")
	}
	w, qArgs, joinNames, err := query.Compile(spec.Query, *spec.Schema)
	if err != nil {
		return nil, fmt.Errorf("list items for view: compile: %w", err)
	}
	for _, name := range joinNames {
		base += " " + spec.Schema.Joins[name]
	}
	if w != "" {
		where = append(where, w)
		args = append(args, qArgs...)
	}
}
```

(`base` is the existing FROM/JOIN clause built earlier in the function — verify the local variable name; rename in the patch if it's different.)

- [ ] **Step 4: Add `feed.SearchSchema()`**

In `plugin/feed/feed.go` (Plan A registered the Schema; expose it):

```go
var searchSchema = &query.Schema{ /* same literal as in RegisterSearchSource */ }

// SearchSchema returns the feed plugin's search schema (used by the global
// bleve index and the SQL compiler in this plugin).
func SearchSchema() *query.Schema { return searchSchema }
```

Then reuse `searchSchema` in the `RegisterSearchSource` call.

- [ ] **Step 5: Run + commit**

```bash
go test ./plugin/feed
git add plugin/feed/store.go plugin/feed/store_test.go plugin/feed/feed.go
git commit -m "feed: ViewSpec accepts query.Expr + Schema; compile to SQL"
```

---

## Task 3: Switch callers; delete `applyQueryFilter`

**Files:**
- Modify: `plugin/feed/pages.go`.
- Test: `plugin/feed/pages_index_test.go`.

- [ ] **Step 1: Write failing test exercising past-LIMIT correctness**

```go
func TestIndexSearchFindsItemsPastLimit(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	for i := 0; i < 600; i++ {
		_, _, _ = s.UpsertItem(feed.Item{
			SubscriptionID: subID, GUID: "n" + strconv.Itoa(i),
			Type: "text", Title: "noise " + strconv.Itoa(i), PublishedAt: int64(1000 + i),
		})
	}
	_, _, _ = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "needle",
		Type: "text", Title: "UNIQUE-NEEDLE-XYZ", PublishedAt: 1,
	})
	resp := env.Get(t, "/apps/feed/?q=UNIQUE-NEEDLE-XYZ", u.Auth)
	resp.Status(200)
	resp.Contains("UNIQUE-NEEDLE-XYZ")
}
```

- [ ] **Step 2: Run — expect FAIL** (today's Go filter cuts at 500).

- [ ] **Step 3: Switch `Index` handler**

Replace:

```go
if q != "" { items = applyQueryFilter(store, items, q) }
```

with parse-on-the-handler-side and pass to ViewSpec:

```go
var expr query.Expr
if q != "" {
	e, perr := query.Parse(q)
	if perr == nil {
		expr = e
	}
}
items, err := store.ListItemsForView(ViewSpec{
	All:      true,
	HideRead: prefs.HideEmpty,
	Query:    expr,
	Schema:   SearchSchema(),
}, feedViewLimit)
```

(Move the `LoadPlayerPrefs` call above so `prefs` is in scope before the `ListItemsForView` call.)

- [ ] **Step 4: Switch `PlaylistView` handler**

```go
expr, _ := query.Parse(pl.Query)
items, _ := store.ListItemsForView(ViewSpec{
	All: true, HideRead: prefs.HideEmpty, Query: expr, Schema: SearchSchema(),
}, feedViewLimit)
```

- [ ] **Step 5: Switch `resolveViewItems`**

In the playlist case:

```go
expr, perr := query.Parse(pl.Query)
if perr != nil {
	return nil, fmt.Errorf("resolve playlist query: %w", perr)
}
items, err = store.ListItemsForView(ViewSpec{
	All: true, HideRead: hide, Query: expr, Schema: SearchSchema(),
}, limit)
```

In the default (all) case:

```go
var expr query.Expr
if c.Q != "" {
	if e, perr := query.Parse(c.Q); perr == nil {
		expr = e
	}
}
items, err = store.ListItemsForView(ViewSpec{
	All: true, HideRead: hide, Query: expr, Schema: SearchSchema(),
}, limit)
```

- [ ] **Step 6: Delete `applyQueryFilter`**

Remove from `pages.go`. `BuildRecord` stays (still used by `filter.go` at ingest time).

- [ ] **Step 7: Run target test + full package**

`go test -run TestIndexSearchFindsItemsPastLimit ./plugin/feed`, then `go test ./plugin/feed`.

- [ ] **Step 8: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/pages_index_test.go
git commit -m "feed: push search/playlist queries into SQL via compiled WHERE"
```

---

## Task 4: Sort subscriptions in SQL

**Files:**
- Modify: `plugin/feed/store.go` (`ListSubscriptions`)
- Modify: `plugin/feed/pages.go` (delete `sortSubscriptionsForSidebar` + its call)

- [ ] **Step 1: Read `sortSubscriptionsForSidebar`** — confirm whether it sorts errors-first then alpha, or just alpha. (`grep -n 'func sortSubscriptionsForSidebar' plugin/feed/pages.go` then read.) Replicate whatever it does in SQL via `ORDER BY (CASE WHEN last_error <> '' THEN 0 ELSE 1 END) ASC, LOWER(COALESCE(NULLIF(title, ''), url)) ASC` (or just the simpler `LOWER(COALESCE(...))` if it's alpha-only).

- [ ] **Step 2: Write failing test asserting the resulting order** in `store_test.go`. Order depends on what the Go sort actually produced.

```go
func TestListSubscriptionsOrdered(t *testing.T) {
	s := newStore(t)
	_, _ = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://z.test/f", Title: "Zeta"})
	_, _ = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://a.test/f", Title: "alpha"})
	_, _ = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://m.test/f", Title: ""})
	subs, _ := s.ListSubscriptions()
	if len(subs) != 3 || subs[0].Title != "alpha" || subs[1].URL != "https://m.test/f" || subs[2].Title != "Zeta" {
		t.Fatalf("got %+v", subs)
	}
}
```

(Adjust expectations if `sortSubscriptionsForSidebar` floats errored entries to the top; then seed an errored sub and assert it's first.)

- [ ] **Step 3: Add the ORDER BY to `ListSubscriptions`** and delete `sortSubscriptionsForSidebar` + its call in `buildSidebar`.

- [ ] **Step 4: Run + commit**

```bash
git add plugin/feed/store.go plugin/feed/pages.go plugin/feed/store_test.go
git commit -m "feed: sort subscriptions in SQL, drop Go sort"
```

---

## Task 5: Bulk delete in one statement each

**Files:**
- Modify: `plugin/feed/store.go` (`DeleteSubscriptions`, `DeleteFolders`).
- Read first: the singular `DeleteSubscription`/`DeleteFolder` bodies to learn cascade order (do they manually delete child rows first?).

- [ ] **Step 1: Read singular cascade**

`grep -n 'func (s \*Store) DeleteSubscription\b\|func (s \*Store) DeleteFolder\b' plugin/feed/store.go` then read the bodies. List the dependent tables touched in order (e.g. playback → downloads → item_tags → items → subscriptions).

- [ ] **Step 2: Write failing test (one statement path)**

`store_test.go`:

```go
func TestDeleteSubscriptionsBulkOneStatement(t *testing.T) {
	s := newStore(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		id, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: fmt.Sprintf("https://%d.test/f", i)})
		ids = append(ids, id)
	}
	require.NoError(t, s.DeleteSubscriptions(ids))
	subs, _ := s.ListSubscriptions()
	if len(subs) != 0 { t.Fatalf("%+v", subs) }
}
```

(The existing `TestBulkDelete` covers the loop path; this lock-in test is for the rewrite. Keep both.)

- [ ] **Step 3: Implement transactional bulk**

```go
func (s *Store) DeleteSubscriptions(ids []int64) error {
	if len(ids) == 0 { return nil }
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	in := strings.Join(placeholders, ",")
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("bulk delete subscriptions: begin: %w", err)
	}
	defer tx.Rollback()
	// Mirror DeleteSubscription's cascade order — adjust per Step 1 reading.
	stmts := []string{
		`DELETE FROM playback   WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM downloads  WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM item_tags  WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM items      WHERE subscription_id IN (` + in + `)`,
		`DELETE FROM subscriptions WHERE id IN (` + in + `)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt, args...); err != nil {
			return fmt.Errorf("bulk delete subscriptions: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("bulk delete subscriptions: commit: %w", err)
	}
	return nil
}
```

Do the equivalent for `DeleteFolders` (read `DeleteFolder` first to confirm whether it nulls `subscriptions.folder_id` or deletes contents; replicate exactly).

- [ ] **Step 4: Run + commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: bulk delete in one statement per dependent table"
```

---

## Task 6: `PlaybackHistoryItems` joined view

**Files:**
- Modify: `plugin/feed/store.go`.
- Modify: `plugin/feed/pages.go` (`resolveViewItems` history case).

- [ ] **Step 1: Write failing test**

```go
func TestPlaybackHistoryItemsJoined(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "SubT"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio", Title: "T"})
	require.NoError(t, s.SetPlayback(id, 5.0, true))
	got, err := s.PlaybackHistoryItems(10)
	require.NoError(t, err)
	if len(got) != 1 || got[0].SubscriptionTitle != "SubT" || got[0].Title != "T" {
		t.Fatalf("%+v", got)
	}
}
```

- [ ] **Step 2: Implement**

```go
func (s *Store) PlaybackHistoryItems(limit int) ([]ItemWithSubscription, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
		       i.content_html, i.media_url, i.media_mime, i.duration_sec,
		       i.thumbnail_url, i.published_at, i.fetched_at, i.is_read, i.is_starred,
		       i.content_hash,
		       COALESCE(s.title, s.url) AS subscription_title
		FROM playback p
		JOIN items i ON i.id = p.item_id
		JOIN subscriptions s ON s.id = i.subscription_id
		ORDER BY p.last_played_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("playback history items: %w", err)
	}
	defer rows.Close()
	var out []ItemWithSubscription
	for rows.Next() {
		var iws ItemWithSubscription
		var isRead, isStarred int
		if err := rows.Scan(
			&iws.ID, &iws.SubscriptionID, &iws.GUID, &iws.Type, &iws.Title, &iws.Author,
			&iws.URL, &iws.ContentHTML, &iws.MediaURL, &iws.MediaMIME, &iws.DurationSec,
			&iws.ThumbnailURL, &iws.PublishedAt, &iws.FetchedAt, &isRead, &isStarred,
			&iws.ContentHash, &iws.SubscriptionTitle,
		); err != nil {
			return nil, fmt.Errorf("playback history items scan: %w", err)
		}
		iws.IsRead = isRead != 0
		iws.IsStarred = isStarred != 0
		out = append(out, iws)
	}
	return out, rows.Err()
}
```

- [ ] **Step 3: Swap `resolveViewItems` history case**

```go
case "history":
	items, err = store.PlaybackHistoryItems(limit)
	if err != nil { return nil, fmt.Errorf("resolve history: %w", err) }
```

(Drop the temporary `played` slice + per-item field copy.)

- [ ] **Step 4: Run + commit**

```bash
git add plugin/feed/store.go plugin/feed/pages.go plugin/feed/store_test.go
git commit -m "feed: PlaybackHistoryItems joined query, drop Go remap"
```

---

## Task 7 (optional polish): consolidate sidebar count queries

**Files:**
- Modify: `plugin/feed/store.go` — add `LoadSidebarCounts`.
- Modify: `plugin/feed/pages.go` — `buildSidebar` uses it.

```go
type SidebarCounts struct {
	TotalUnread    int
	StarredCount   int
	BySubscription map[int64]int
	ByFolder       map[int64]int
}

func (s *Store) LoadSidebarCounts() (SidebarCounts, error) {
	sc := SidebarCounts{BySubscription: map[int64]int{}, ByFolder: map[int64]int{}}
	rows, err := s.db.Query(`
		SELECT s.id, s.folder_id,
		       COALESCE(SUM(CASE WHEN i.is_read = 0 THEN 1 ELSE 0 END), 0) AS unread
		FROM subscriptions s
		LEFT JOIN items i ON i.subscription_id = s.id
		GROUP BY s.id, s.folder_id`)
	if err != nil { return sc, fmt.Errorf("sidebar counts subs: %w", err) }
	defer rows.Close()
	for rows.Next() {
		var sid int64
		var fid sql.NullInt64
		var unread int
		if err := rows.Scan(&sid, &fid, &unread); err != nil {
			return sc, fmt.Errorf("sidebar counts scan: %w", err)
		}
		sc.BySubscription[sid] = unread
		sc.TotalUnread += unread
		if fid.Valid { sc.ByFolder[fid.Int64] += unread }
	}
	if err := rows.Err(); err != nil {
		return sc, fmt.Errorf("sidebar counts rows: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_starred = 1`).Scan(&sc.StarredCount); err != nil {
		return sc, fmt.Errorf("sidebar starred count: %w", err)
	}
	return sc, nil
}
```

In `buildSidebar`, replace the four count calls with one `LoadSidebarCounts`. Delete `CountUnread`/`CountStarred`/`CountUnreadBySubscription`/`CountUnreadByFolder` IF nothing else references them (`grep -rn` first).

- [ ] **Step 1: Test asserting all four counts via `LoadSidebarCounts` after seeding.**
- [ ] **Step 2: Implement + swap.**
- [ ] **Step 3: Commit.**

```bash
git commit -m "feed: consolidate sidebar count queries into one pass"
```

---

## Final: full verify

- [ ] `go tool verify` — expect "All checks passed."
- [ ] Manual: `/apps/feed/?q=` with a query whose matches are older than the newest 500 items (confirm visible). A Playlist whose query selects older items. Sidebar load is one count query (verify via SQLite trace or log).

## Self-review notes (coverage map)

- `applyQueryFilter` (N+1 + post-LIMIT) → Tasks 1, 2, 3.
- `sortSubscriptionsForSidebar` → Task 4.
- `DeleteSubscriptions`/`DeleteFolders` loops → Task 5.
- `ListPlaybackHistory` Go remap → Task 6.
- Sidebar count round-trips → Task 7 (optional).

## Open verification points the executor MUST confirm before relying on them

- `sql/schema.sql` cascade behavior on subscription/folder deletion (Task 5): replicate the singular method's order exactly.
- `sortSubscriptionsForSidebar`'s exact tie-breaking (errors-first vs alpha-only) before deleting it (Task 4).
- Whether `CountUnread`/`CountStarred`/etc. have callers outside `buildSidebar` (Task 7) — grep before deleting.
- `feed.SearchSchema()` exposure: Plan A registered the Schema in a literal; expose it via the package-level getter so the store-side (this plan) can pass `*query.Schema` on `ViewSpec`.
