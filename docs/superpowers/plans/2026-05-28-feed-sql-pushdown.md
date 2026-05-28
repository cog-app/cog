# Feed Plugin: Push Logic from Go into SQL — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the Go-side logic that ought to be in SQL — closing correctness gaps (search/filter matches past LIMIT are invisible), N+1 query patterns, and redundant round-trips.

**Architecture:** A small SQL compiler for the cog-grammar AST (in `internal/query`) emits a WHERE clause + bind args + a join-requirement set. `feed.ViewSpec` gains a `Query Expr` field; `Store.ListItemsForView` integrates the compiled WHERE and adds the LEFT JOINs the query needs. `applyQueryFilter` is deleted. Other small fixes: subscription sort in SQL, bulk-delete via `WHERE id IN (?,…)`, `ListPlaybackHistory` returns the joined shape directly, and (optionally) the sidebar's N count-queries collapse into one.

**Tech Stack:** Go 1.22+, SQLite (single writer, WAL), templ, `internal/query` AST.

---

## Conventions for every task

- Tests use `internal/testing/cogtest`/`require`. White-box tests for unexported helpers go in `package feed` (or `package query`) files with `_internal_test.go` suffix.
- After editing `*.templ`: `go generate ./...` then `go build ./...`. Tabs in `.templ`. Format with `go tool templ fmt .` before commit. IGNORE stale LSP arg-count diagnostics on feed* templates — trust `go build ./...`.
- Verify with `go test ./plugin/feed ./internal/query` per task; `go tool verify` at the end.
- WE DO NOT SWALLOW ERRORS (CLAUDE.md). Wrap+return or `slog`.
- Commit after each task; reference the file/symbol changed in the message.

## Background (current pain points)

- **`applyQueryFilter`** (`plugin/feed/pages.go`) runs in Go over the list returned by `ListItemsForView`. For each candidate item it issues `GetSubscription` + `GetFolder` + `ItemTags` + `GetDownload` (4 queries per item, up to ~2000 per page) then calls `BuildRecord` + `expr.Match`. Two problems:
  1. **Correctness:** filter runs AFTER `LIMIT`, so matches past row 500 are silently invisible.
  2. **Performance:** N+1 store queries per page load.
- **`sortSubscriptionsForSidebar`** (`pages.go:416`) re-sorts in Go what could come ordered from SQL.
- **`DeleteSubscriptions`/`DeleteFolders`** (`store.go`) loop single-row DELETEs.
- **`ListPlaybackHistory`** returns `[]PlayedItem`; `resolveViewItems`'s history case remaps to `[]ItemWithSubscription` field-by-field.
- **`buildSidebar`** issues `CountUnread` + `CountStarred` + `CountUnreadBySubscription` + `CountUnreadByFolder` + `ListFolders` + `ListSubscriptions` + `ListTags` + `ListPlaylists` + `LoadPlayerPrefs` — at least 9 round-trips per sidebar render. The four count queries can collapse.

## AST recap (what we need to compile)

`internal/query/ast.go` defines: `TextExpr`, `PhraseExpr`, `NotExpr`, `AndExpr`, `OrExpr`, `FieldEqExpr{Field,Value}`, `FieldInExpr{Field,Values}`, `MatchAllExpr`. The feed Record fields (from `plugin/feed/record.go`): `title`, `author`, `content`, `type`, `kind`, `subscription`, `url`, `published`, `folder`, `tag`, `is` ∈ {`unread`, `starred`, `downloaded`}.

## File structure

- **Create:** `internal/query/sql.go` — `Compile(e Expr) (where string, args []any, joins JoinSet, err error)`; `JoinSet` flags (folders, downloads).
- **Create:** `internal/query/sql_test.go` — table tests for each AST node + the field map.
- **Modify:** `plugin/feed/store.go` — `ViewSpec` gains `Query query.Expr`; `ListItemsForView` integrates compiled WHERE + joins. `ListSubscriptions` gains `ORDER BY`. `DeleteSubscriptions`/`DeleteFolders` use `WHERE id IN (?,…)`. `ListPlaybackHistory` returns `[]ItemWithSubscription`.
- **Modify:** `plugin/feed/pages.go` — `Index`/`PlaylistView`/`resolveViewItems` set `ViewSpec.Query` instead of calling `applyQueryFilter`. Delete `applyQueryFilter` and `sortSubscriptionsForSidebar`. Remove the Go remap in the history case.
- **Modify:** `plugin/feed/store.go` — (optional) `LoadSidebarCounts` aggregating all four count queries.

---

## Task 1: SQL compiler for the cog-grammar AST

**Files:**
- Create: `internal/query/sql.go`
- Create: `internal/query/sql_test.go`

The compiler walks the AST and produces a parameterized WHERE fragment that operates against the column aliases `i.*` (items) and `s.*` (subscriptions). For folder/download membership the compiler signals the caller to add the corresponding LEFT JOIN via `JoinSet`. Tag membership is encoded as a correlated `EXISTS` subquery, so no caller-side JOIN is needed (and rows are not duplicated).

### Column map (constant)

```go
// itemColumn maps a Record field to the SQL column the compiler should
// reference. Only fields representable directly as a column live here; fields
// requiring joins or subqueries (folder, tag, is) are handled by dedicated
// builders below.
var itemColumn = map[string]string{
	"title":        "i.title",
	"author":       "i.author",
	"content":      "i.content_html",
	"type":         "i.type",
	"url":          "i.url",
	"published":    "i.published_at",
	"kind":         "s.kind",
	"subscription": "s.title",
}
```

### Free-text fields (TextExpr / PhraseExpr)

Searched columns: `i.title`, `i.author`, `i.content_html`, `i.url`, `s.title`, `s.kind`, plus `COALESCE(f.name,'')` (LEFT JOIN folders, added when `JoinSet.Folders` is set), plus tag-name match via EXISTS. Comparison is `LOWER(col) LIKE ?` with the bound arg `"%" + lower + "%"`.

### Field semantics

- `title|author|content|url|type|kind|subscription` (`FieldEqExpr`/`FieldInExpr`): `LOWER(col) = LOWER(?)` (or `IN` for the multi-value case). Matches the Go `EqualFold` semantics.
- `published`: `i.published_at = strftime('%s', ?)` … skip exact-equality on a free-form timestamp string is rarely useful — the Go side already requires an exact RFC3339 match, so we keep the same brittle equality on the formatted text (`strftime(..., 'unixepoch')`-formatted column ≈ RFC3339). To avoid date-format friction, accept the existing semantics: `strftime('%Y-%m-%dT%H:%M:%SZ', i.published_at, 'unixepoch') = ?`.
- `folder`: requires `JoinSet.Folders = true`; emits `LOWER(f.name) = LOWER(?)`.
- `tag`: emits `EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_id = i.id AND LOWER(t.name) = LOWER(?))`. No caller join required.
- `is`: per-value mapping — `unread → i.is_read = 0`, `starred → i.is_starred = 1`, `downloaded → EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')` (sets `JoinSet.Downloads = false` because EXISTS is self-contained; no caller join needed).

### `Compile` signature

```go
// JoinSet flags optional joins the caller should add to compose with the
// compiled WHERE fragment. Tag and is:downloaded use correlated EXISTS
// subqueries and require no caller-side join.
type JoinSet struct {
	Folders bool
}

// Compile renders an Expr into a SQL WHERE fragment (without the leading
// "WHERE"), the bind args in order, and the join requirements. The fragment
// references the alias 'i' for items and 's' for subscriptions; the caller
// must alias them accordingly. Returns ("", nil, JoinSet{}, nil) for a nil
// Expr or MatchAllExpr (no constraint).
func Compile(e Expr) (where string, args []any, joins JoinSet, err error)
```

### Steps

- [ ] **Step 1: Write the failing test (whole-AST table test)**

`internal/query/sql_test.go`:

```go
package query

import (
	"strings"
	"testing"
)

func TestCompileTextExpr(t *testing.T) {
	w, args, _, err := Compile(&TextExpr{Value: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, "LOWER(i.title) LIKE ?") {
		t.Fatalf("missing title LIKE: %q", w)
	}
	if len(args) == 0 || args[0] != "%hello%" {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileFieldEqType(t *testing.T) {
	w, args, _, err := Compile(&FieldEqExpr{Field: "type", Value: "Audio"})
	if err != nil {
		t.Fatal(err)
	}
	if w != "LOWER(i.type) = LOWER(?)" {
		t.Fatalf("where=%q", w)
	}
	if len(args) != 1 || args[0] != "Audio" {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileFieldEqFolderRequiresJoin(t *testing.T) {
	_, _, joins, err := Compile(&FieldEqExpr{Field: "folder", Value: "Dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !joins.Folders {
		t.Fatal("folder field must require folders join")
	}
}

func TestCompileFieldEqTagUsesExists(t *testing.T) {
	w, _, joins, err := Compile(&FieldEqExpr{Field: "tag", Value: "weekly"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w, "EXISTS") {
		t.Fatalf("tag should use EXISTS: %q", w)
	}
	if joins.Folders {
		t.Fatal("tag must not require folders join")
	}
}

func TestCompileIsValues(t *testing.T) {
	for _, c := range []struct {
		val, sub string
	}{
		{"unread", "i.is_read = 0"},
		{"starred", "i.is_starred = 1"},
		{"downloaded", "EXISTS (SELECT 1 FROM downloads"},
	} {
		w, _, _, err := Compile(&FieldEqExpr{Field: "is", Value: c.val})
		if err != nil {
			t.Fatalf("%s: %v", c.val, err)
		}
		if !strings.Contains(w, c.sub) {
			t.Fatalf("%s: where=%q want substr %q", c.val, w, c.sub)
		}
	}
}

func TestCompileAndOrNot(t *testing.T) {
	e := &AndExpr{
		Left:  &FieldEqExpr{Field: "type", Value: "audio"},
		Right: &NotExpr{Inner: &FieldEqExpr{Field: "is", Value: "unread"}},
	}
	w, args, _, err := Compile(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, " AND ") || !strings.Contains(w, "NOT (") {
		t.Fatalf("missing AND/NOT: %q", w)
	}
	if len(args) != 1 {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileFieldInExpr(t *testing.T) {
	w, args, _, err := Compile(&FieldInExpr{Field: "type", Values: []string{"audio", "video"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w, "LOWER(i.type) IN (") {
		t.Fatalf("where=%q", w)
	}
	if len(args) != 2 {
		t.Fatalf("args=%v", args)
	}
}

func TestCompileMatchAllReturnsEmpty(t *testing.T) {
	w, args, _, err := Compile(&MatchAllExpr{})
	if err != nil || w != "" || len(args) != 0 {
		t.Fatalf("got w=%q args=%v err=%v", w, args, err)
	}
}

func TestCompileNilReturnsEmpty(t *testing.T) {
	w, _, _, err := Compile(nil)
	if err != nil || w != "" {
		t.Fatalf("got w=%q err=%v", w, err)
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

`go test ./internal/query` → undefined `Compile`/`JoinSet`.

- [ ] **Step 3: Implement `Compile`**

`internal/query/sql.go`:

```go
package query

import (
	"fmt"
	"strings"
)

// JoinSet flags optional joins the caller must add to compose with the
// compiled WHERE fragment. Tag and is:downloaded use correlated EXISTS
// subqueries and require no caller-side join.
type JoinSet struct {
	Folders bool
}

// Compile renders an Expr into a SQL WHERE fragment (no leading "WHERE"), its
// bind args in order, and the caller-side join requirements. The fragment
// references the alias 'i' for items and 's' for subscriptions; the caller
// must alias them accordingly. A nil Expr (or MatchAllExpr) yields an empty
// fragment with no args — i.e. "no constraint".
func Compile(e Expr) (string, []any, JoinSet, error) {
	if e == nil {
		return "", nil, JoinSet{}, nil
	}
	c := compiler{}
	w, err := c.expr(e)
	if err != nil {
		return "", nil, JoinSet{}, err
	}
	return w, c.args, c.joins, nil
}

type compiler struct {
	args  []any
	joins JoinSet
}

func (c *compiler) expr(e Expr) (string, error) {
	switch x := e.(type) {
	case *MatchAllExpr, nil:
		return "", nil
	case *TextExpr:
		return c.likeAcrossFields(x.Value)
	case *PhraseExpr:
		return c.likeAcrossFields(x.Value)
	case *NotExpr:
		inner, err := c.expr(x.Inner)
		if err != nil {
			return "", err
		}
		if inner == "" {
			return "0", nil // "NOT (no constraint)" => never matches
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
		return "", fmt.Errorf("query: unsupported expression type %T", e)
	}
}

func (c *compiler) binary(l, r Expr, op string) (string, error) {
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

var itemColumn = map[string]string{
	"title":        "i.title",
	"author":       "i.author",
	"content":      "i.content_html",
	"type":         "i.type",
	"url":          "i.url",
	"kind":         "s.kind",
	"subscription": "s.title",
}

// fieldEq emits SQL for a :field=value match, honoring case-insensitivity.
func (c *compiler) fieldEq(field, value string) (string, error) {
	switch field {
	case "folder":
		c.joins.Folders = true
		c.args = append(c.args, value)
		return "LOWER(f.name) = LOWER(?)", nil
	case "tag":
		c.args = append(c.args, value)
		return "EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_id = i.id AND LOWER(t.name) = LOWER(?))", nil
	case "is":
		return c.isValue(value)
	case "published":
		c.args = append(c.args, value)
		return "strftime('%Y-%m-%dT%H:%M:%SZ', i.published_at, 'unixepoch') = ?", nil
	}
	col, ok := itemColumn[field]
	if !ok {
		return "", fmt.Errorf("query: unknown field %q", field)
	}
	c.args = append(c.args, value)
	return fmt.Sprintf("LOWER(%s) = LOWER(?)", col), nil
}

func (c *compiler) isValue(v string) (string, error) {
	switch strings.ToLower(v) {
	case "unread":
		return "i.is_read = 0", nil
	case "starred":
		return "i.is_starred = 1", nil
	case "downloaded":
		return "EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = i.id AND d.state = 'ready')", nil
	default:
		return "", fmt.Errorf("query: unknown is-value %q", v)
	}
}

// fieldIn handles :field IN (v1,v2,...). For tag/is/folder we OR the per-value
// fragment; for column-backed fields we emit `LOWER(col) IN (LOWER(?), ...)`.
func (c *compiler) fieldIn(field string, values []string) (string, error) {
	if len(values) == 0 {
		return "0", nil
	}
	switch field {
	case "folder", "tag", "is", "published":
		parts := make([]string, 0, len(values))
		for _, v := range values {
			s, err := c.fieldEq(field, v)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		if len(parts) == 1 {
			return parts[0], nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", nil
	}
	col, ok := itemColumn[field]
	if !ok {
		return "", fmt.Errorf("query: unknown field %q", field)
	}
	placeholders := make([]string, len(values))
	for i, v := range values {
		placeholders[i] = "LOWER(?)"
		c.args = append(c.args, v)
	}
	return fmt.Sprintf("LOWER(%s) IN (%s)", col, strings.Join(placeholders, ", ")), nil
}

// likeAcrossFields emits a LIKE-any-of-many-columns disjunction, plus a tag
// EXISTS. Mirrors the Go TextExpr/PhraseExpr behavior (case-insensitive
// substring across all Record values).
func (c *compiler) likeAcrossFields(value string) (string, error) {
	c.joins.Folders = true
	like := "%" + strings.ToLower(value) + "%"
	cols := []string{
		"LOWER(i.title)",
		"LOWER(i.author)",
		"LOWER(i.content_html)",
		"LOWER(i.url)",
		"LOWER(s.title)",
		"LOWER(s.kind)",
		"LOWER(COALESCE(f.name,''))",
	}
	parts := make([]string, 0, len(cols)+1)
	for _, col := range cols {
		parts = append(parts, col+" LIKE ?")
		c.args = append(c.args, like)
	}
	// Tag-name LIKE.
	parts = append(parts, "EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_id = i.id AND LOWER(t.name) LIKE ?)")
	c.args = append(c.args, like)
	return "(" + strings.Join(parts, " OR ") + ")", nil
}
```

- [ ] **Step 4: Run tests — expect PASS**

`go test ./internal/query` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/query/sql.go internal/query/sql_test.go
git commit -m "query: SQL compiler for cog-grammar AST"
```

---

## Task 2: Integrate compiled query into `ListItemsForView`

**Files:**
- Modify: `plugin/feed/store.go` (`ViewSpec`, `ListItemsForView`)
- Test: `plugin/feed/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `store_test.go`:

```go
func TestListItemsForViewWithQuery(t *testing.T) {
	s := newStore(t)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "Site A"})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio", Title: "alpha keep me"})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "text", Title: "beta skip me"})
	require.NoError(t, err)

	expr, err := query.Parse("type:audio")
	require.NoError(t, err)
	got, err := s.ListItemsForView(feed.ViewSpec{All: true, Query: expr}, 100)
	require.NoError(t, err)
	if len(got) != 1 || got[0].Title != "alpha keep me" {
		t.Fatalf("got %+v", got)
	}
}
```

Add `import "git.duckfam.us/jj-software/cog/internal/query"` to the test file.

- [ ] **Step 2: Run — expect FAIL**

Undefined `Query` field on `ViewSpec`.

- [ ] **Step 3: Add `Query` field and wire compile**

In `store.go`, on `ViewSpec`:

```go
	HideRead bool
	Query    query.Expr // optional cog-grammar filter; compiled to SQL.
```

In `ListItemsForView`, after the existing per-kind switch builds the base WHERE/args, integrate:

```go
	// Compile optional grammar query.
	if spec.Query != nil {
		qWhere, qArgs, joins, err := query.Compile(spec.Query)
		if err != nil {
			return nil, fmt.Errorf("list items for view: compile query: %w", err)
		}
		if joins.Folders {
			base += " LEFT JOIN folders f ON f.id = s.folder_id"
		}
		if qWhere != "" {
			where = append(where, qWhere)
			args = append(args, qArgs...)
		}
	}
```

Add `"git.duckfam.us/jj-software/cog/internal/query"` import to `store.go`.

- [ ] **Step 4: Run — expect PASS**

`go test -run TestListItemsForViewWithQuery ./plugin/feed`

- [ ] **Step 5: Full package**

`go test ./plugin/feed` → PASS.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: ListItemsForView accepts query.Expr for SQL filtering"
```

---

## Task 3: Wire callers (Index, PlaylistView, resolveViewItems) + delete `applyQueryFilter`

**Files:**
- Modify: `plugin/feed/pages.go`

- [ ] **Step 1: Write a failing test**

Append to `pages_index_test.go`:

```go
func TestIndexSearchFindsItemsPastLimit(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "Site"})
	require.NoError(t, err)
	// 600 noise items (newer), 1 matching "needle" item (oldest).
	for i := 0; i < 600; i++ {
		_, _, err = s.UpsertItem(feed.Item{
			SubscriptionID: subID,
			GUID:           "n" + strconv.Itoa(i),
			Type:           "text",
			Title:          "noise " + strconv.Itoa(i),
			PublishedAt:    int64(1000 + i),
		})
		require.NoError(t, err)
	}
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "needle",
		Type:           "text",
		Title:          "UNIQUE-NEEDLE-XYZ",
		PublishedAt:    1, // oldest
	})
	require.NoError(t, err)
	resp := env.Get(t, "/apps/feed/?q=UNIQUE-NEEDLE-XYZ", u.Auth)
	resp.Status(200)
	resp.Contains("UNIQUE-NEEDLE-XYZ")
}
```

- [ ] **Step 2: Run — expect FAIL** (current Go filter sees only newest 500 items, all noise).

- [ ] **Step 3: Switch `Index` to compile-and-push**

In `Index` handler, replace:

```go
	if q != "" {
		items = applyQueryFilter(store, items, q)
	}
```

with parse-and-pass-on-ViewSpec, before the `ListItemsForView` call:

```go
	var qExpr query.Expr
	if q != "" {
		expr, perr := query.Parse(q)
		if perr == nil {
			qExpr = expr
		}
	}
	items, err := store.ListItemsForView(ViewSpec{All: true, HideRead: prefs.HideEmpty, Query: qExpr}, feedViewLimit)
```

(Move the `LoadPlayerPrefs` call above so `prefs` is in scope.) Adjust the error handling block to match the existing one.

- [ ] **Step 4: Switch `PlaylistView` to compile-and-push**

Replace:

```go
	items, _ := store.ListItemsForView(ViewSpec{All: true, HideRead: prefs.HideEmpty}, feedViewLimit)
	items = applyQueryFilter(store, items, pl.Query)
```

with:

```go
	expr, _ := query.Parse(pl.Query)
	items, _ := store.ListItemsForView(ViewSpec{All: true, HideRead: prefs.HideEmpty, Query: expr}, feedViewLimit)
```

- [ ] **Step 5: Switch `resolveViewItems` to compile-and-push**

In the playlist case:

```go
	expr, perr := query.Parse(pl.Query)
	if perr != nil {
		return nil, fmt.Errorf("resolve playlist query: %w", perr)
	}
	items, err = store.ListItemsForView(ViewSpec{All: true, HideRead: hide, Query: expr}, limit)
```

In the default (all) case:

```go
	var expr query.Expr
	if c.Q != "" {
		if e, perr := query.Parse(c.Q); perr == nil {
			expr = e
		}
	}
	items, err = store.ListItemsForView(ViewSpec{All: true, HideRead: hide, Query: expr}, limit)
```

Compute `hide` once at the top of the function via `LoadPlayerPrefs` (existing in this file).

- [ ] **Step 6: Delete `applyQueryFilter`**

Remove the function entirely. `BuildRecord` stays (used by `filter.go` at ingest).

- [ ] **Step 7: Run target test — expect PASS**

`go test -run TestIndexSearchFindsItemsPastLimit ./plugin/feed`

- [ ] **Step 8: Full package**

`go test ./plugin/feed` → PASS.

- [ ] **Step 9: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/pages_index_test.go
git commit -m "feed: push search and playlist queries into SQL"
```

---

## Task 4: Sort subscriptions in SQL

**Files:**
- Modify: `plugin/feed/store.go` (`ListSubscriptions`)
- Modify: `plugin/feed/pages.go` (drop `sortSubscriptionsForSidebar`)

- [ ] **Step 1: Write failing test**

Append to `store_test.go`:

```go
func TestListSubscriptionsOrderedAlpha(t *testing.T) {
	s := newStore(t)
	_, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://z.test/f", Title: "Zeta"})
	require.NoError(t, err)
	_, err = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://a.test/f", Title: "alpha"})
	require.NoError(t, err)
	_, err = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://m.test/f", Title: ""}) // sorts by URL
	require.NoError(t, err)
	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	if len(subs) != 3 {
		t.Fatalf("got %d", len(subs))
	}
	// Expected order: alpha (a), https://m.test/f (m), Zeta (z), all lower-cased
	if subs[0].Title != "alpha" || subs[1].URL != "https://m.test/f" || subs[2].Title != "Zeta" {
		t.Fatalf("got %+v", subs)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (current ListSubscriptions returns insertion order).

- [ ] **Step 3: Add ORDER BY to ListSubscriptions**

Locate the query in `store.go` and append:

```sql
ORDER BY LOWER(COALESCE(NULLIF(title, ''), url)) ASC
```

- [ ] **Step 4: Drop the Go sort**

Delete `sortSubscriptionsForSidebar` from `pages.go` and the call in `buildSidebar`. The error-first-then-alpha behavior the Go sort had (subs with errors floated up) is lost. If preserving that is needed, add a secondary key: `ORDER BY (CASE WHEN last_error <> '' THEN 0 ELSE 1 END) ASC, LOWER(...) ASC` — confirm by reading the existing `sortSubscriptionsForSidebar` first; if it ONLY sorts alpha, use the simple ORDER BY.

- [ ] **Step 5: Run — expect PASS** and full package.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/store.go plugin/feed/pages.go plugin/feed/store_test.go
git commit -m "feed: sort subscriptions in SQL, drop Go sort"
```

---

## Task 5: Bulk delete in one SQL each

**Files:**
- Modify: `plugin/feed/store.go` (`DeleteSubscriptions`, `DeleteFolders`)

- [ ] **Step 1: Write failing test (perf-shaped — verify a single statement path)**

Append to `store_test.go`:

```go
func TestDeleteSubscriptionsBulkOneStatement(t *testing.T) {
	s := newStore(t)
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: fmt.Sprintf("https://%d.test/f", i)})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.NoError(t, s.DeleteSubscriptions(ids))
	subs, _ := s.ListSubscriptions()
	if len(subs) != 0 {
		t.Fatalf("got %+v", subs)
	}
}
```

(The behavioral test is identical to the existing `TestBulkDelete`; this one is a sanity check the bulk path still passes after the rewrite. Keep `TestBulkDelete` too.)

- [ ] **Step 2: Replace loop-of-single-deletes with `WHERE id IN`**

Read the existing `DeleteSubscription` (singular) — note what cascading it performs (the singular path may delete items+downloads+playback+tags first to handle FK lack-of-cascade). The bulk variant must replicate that cascading. If FKs use `ON DELETE CASCADE` (check `sql/schema.sql`), one DELETE on subscriptions suffices; otherwise wrap in a transaction:

```go
func (s *Store) DeleteSubscriptions(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
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
	// Mirror DeleteSubscription's cascade order. Verify the exact set of
	// dependent tables in the singular method and replicate them here.
	for _, stmt := range []string{
		`DELETE FROM playback WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM downloads WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM item_tags WHERE item_id IN (SELECT id FROM items WHERE subscription_id IN (` + in + `))`,
		`DELETE FROM items WHERE subscription_id IN (` + in + `)`,
		`DELETE FROM subscriptions WHERE id IN (` + in + `)`,
	} {
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

Do the equivalent for `DeleteFolders` — read `DeleteFolder` (singular) to learn whether it nulls out `subscriptions.folder_id` or what. Replicate.

- [ ] **Step 3: Run target + existing `TestBulkDelete`** — expect PASS.

- [ ] **Step 4: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: bulk delete subscriptions/folders in one statement each"
```

---

## Task 6: `ListPlaybackHistory` returns joined shape

**Files:**
- Modify: `plugin/feed/store.go`
- Modify: `plugin/feed/pages.go` (`resolveViewItems`)
- Modify: `plugin/feed/pages.templ` (`feedHistoryPage` / `feedHistoryRow` if they consume `PlayedItem` fields — verify)

The current `PlayedItem` adds `SubscriptionTitle`, `ProgressSec`, `Completed`, `LastPlayedAt` on top of `Item`. The history page renders progress+played; the resolver needs only `Item` + `SubscriptionTitle`. To avoid breaking the history page rendering, keep `PlayedItem` but ALSO expose an `Item`/`Sub` shape directly — or change `ListPlaybackHistory` to return `[]ItemWithSubscription` plus separate progress data via a sibling method.

Simpler: add `func (s *Store) PlaybackHistoryItems(limit int) ([]ItemWithSubscription, error)` that returns the same joined rows but without progress/completed/last_played, used solely by `resolveViewItems`. Leave `ListPlaybackHistory` untouched for the history page rendering. Cost: one new helper.

- [ ] **Step 1: Write failing test**

```go
func TestPlaybackHistoryItemsJoined(t *testing.T) {
	s := newStore(t)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "SubT"})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio", Title: "T"})
	require.NoError(t, err)
	require.NoError(t, s.SetPlayback(id, 5.0, true))
	got, err := s.PlaybackHistoryItems(10)
	require.NoError(t, err)
	if len(got) != 1 || got[0].SubscriptionTitle != "SubT" || got[0].Title != "T" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (undefined).

- [ ] **Step 3: Implement**

In `store.go` near `ListPlaybackHistory`:

```go
// PlaybackHistoryItems returns the played items joined with their
// subscription title, ordered by last-played descending. Like
// ListPlaybackHistory but yields the same shape view handlers consume.
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
		if err := rows.Scan(&iws.ID, &iws.SubscriptionID, &iws.GUID, &iws.Type, &iws.Title, &iws.Author,
			&iws.URL, &iws.ContentHTML, &iws.MediaURL, &iws.MediaMIME, &iws.DurationSec,
			&iws.ThumbnailURL, &iws.PublishedAt, &iws.FetchedAt, &isRead, &isStarred,
			&iws.ContentHash, &iws.SubscriptionTitle); err != nil {
			return nil, fmt.Errorf("playback history items scan: %w", err)
		}
		iws.IsRead = isRead != 0
		iws.IsStarred = isStarred != 0
		out = append(out, iws)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Update `resolveViewItems` history case**

Replace the existing remap loop with:

```go
	case "history":
		items, err = store.PlaybackHistoryItems(limit)
		if err != nil {
			return nil, fmt.Errorf("resolve history: %w", err)
		}
```

(Drop the `for _, p := range played { ... }` block and the temporary `played` slice.)

- [ ] **Step 5: Run + commit**

```bash
git add plugin/feed/store.go plugin/feed/pages.go plugin/feed/store_test.go
git commit -m "feed: PlaybackHistoryItems joined query, drop Go remap"
```

---

## Task 7 (optional polish): consolidate sidebar count queries

**Files:**
- Modify: `plugin/feed/store.go` — add `LoadSidebarCounts`
- Modify: `plugin/feed/pages.go` — `buildSidebar` uses it

Drops four queries (TotalUnread, StarredCount, CountUnreadBySubscription, CountUnreadByFolder) to one (sub-counts, plus a tiny aggregate). Implementation sketch:

```go
type SidebarCounts struct {
	TotalUnread  int
	StarredCount int
	BySubscription map[int64]int
	ByFolder       map[int64]int
}

func (s *Store) LoadSidebarCounts() (SidebarCounts, error) {
	var sc SidebarCounts
	sc.BySubscription = map[int64]int{}
	sc.ByFolder = map[int64]int{}
	// Per-subscription unread; aggregate folder + total from it.
	rows, err := s.db.Query(`
		SELECT s.id, s.folder_id, COALESCE(SUM(CASE WHEN i.is_read = 0 THEN 1 ELSE 0 END), 0) AS unread
		FROM subscriptions s LEFT JOIN items i ON i.subscription_id = s.id
		GROUP BY s.id, s.folder_id`)
	if err != nil {
		return sc, fmt.Errorf("sidebar counts subs: %w", err)
	}
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
		if fid.Valid {
			sc.ByFolder[fid.Int64] += unread
		}
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

Then `buildSidebar` becomes:

```go
counts, err := store.LoadSidebarCounts()
if err != nil { slog.Error("feed: sidebar counts", "err", err) }
return sidebarData{
	// ...
	UnreadCounts: counts.BySubscription,
	FolderUnread: counts.ByFolder,
	TotalUnread:  counts.TotalUnread,
	StarredCount: counts.StarredCount,
}
```

Delete `CountUnread`, `CountStarred`, `CountUnreadBySubscription`, `CountUnreadByFolder` (or keep with a deprecation comment if other callers exist — verify with `grep`).

- [ ] **Step 1: Write a test asserting all four counts via `LoadSidebarCounts` after seeding.**
- [ ] **Step 2: Implement, swap `buildSidebar`, delete unused.**
- [ ] **Step 3: Full package + commit.**

```bash
git commit -m "feed: consolidate sidebar count queries into one pass"
```

---

## Final: full verify

- [ ] `go tool verify` — expect "All checks passed."
- [ ] Manual: load `/apps/feed/?q=…` with a query that has matches only past the original 500-item window; confirm visible. Same for a Playlist whose query selects older items. Confirm no behavior regressions on the four settings pages, the item context bar, sidebar toggle, hide-read.

```bash
git commit --allow-empty -m "feed: SQL-pushdown verification complete"  # optional
```

---

## Self-review notes (coverage map)

- `applyQueryFilter` → Tasks 1, 2, 3 (compiler + ViewSpec + caller rewires + delete).
- `sortSubscriptionsForSidebar` → Task 4.
- `DeleteSubscriptions`/`DeleteFolders` loops → Task 5.
- `ListPlaybackHistory` Go remap → Task 6.
- Sidebar count round-trips → Task 7 (optional).

## Open verification points the executor MUST confirm before relying on them

- `sql/schema.sql` cascade behavior on subscription/folder deletion (Task 5): the singular `DeleteSubscription` body is the source of truth — replicate its order.
- `sortSubscriptionsForSidebar`'s exact tie-breaking (errors first?) before deleting it (Task 4).
- Whether `CountUnread`, `CountStarred`, etc. have callers outside `buildSidebar` (Task 7) — grep before deleting.
