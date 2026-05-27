# Feed Plugin Pre-Merge Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Five UX/structure fixes to the `plugin/feed` package before merging `feat/feed-plugin`: sidebar on every page, a context bar with prev/next on item views, context-aware player nav with browser-history push, a hide-empty-feeds setting, and a four-page settings split with admin tabs.

**Architecture:** A `viewCtx` descriptor (carried as URL query params) identifies the "playlist" an item was opened from. One resolver, `resolveViewItems`, reproduces each view's exact item list so prev/next neighbors match what was on screen. The item page renders a context bar (any-type neighbors, full-page links); the media player advances within the same context (media-only) via existing endpoints, now ctx-aware, with `history.pushState` on each in-place swap. A per-user `hide_empty` pref filters the sidebar. The monolithic settings page splits into four routed pages sharing a tab bar.

**Tech Stack:** Go 1.22+ (`net/http` pattern routing), templ templates, SQLite via the per-user feed `Store`, HTMX, vanilla JS (`static/player.js`). Tests use `internal/testing/cogtest` + `internal/testing/require`/`assert`.

---

## Conventions for every task

- Templates are in `plugin/feed/pages.templ`; after editing run `go generate ./...` (regenerates `pages_templ.go`) then `go build ./...`. Never hand-edit `*_templ.go`.
- Format templ with `go tool templ fmt .` before committing template changes.
- `.templ` and `*.tmpl` indentation is **tabs**.
- WE DO NOT SWALLOW ERRORS — wrap+return or log via `slog`.
- Tests live in `plugin/feed/`. Existing tests use `package feed_test`; white-box tests for unexported helpers use `package feed` in a `*_internal_test.go` file (Go allows both in one dir).
- Run a single test: `go test -run TestName ./plugin/feed`. Run package: `go test ./plugin/feed`.
- Verified API facts: `Store.UpsertItem(it Item) (int64, bool, error)`; `Store.ListPlaybackHistory(limit int) ([]PlayedItem, error)`; `cogtest.Response` embeds `*http.Response` (use `resp.Header.Get("Location")` and `resp.ReadBody()` — there is no `.Header(...)` or `.Body()` method); `cogtest.User` embeds `*user.User` (so `u.Username` and `u.User.Username` both work); the **first** user created via `env.CreateUser` is an admin (subsequent ones are not) — use the first user for admin-page tests.
- Commit after each task's tests pass.

## File structure (what changes)

- `plugin/feed/nav.go` — **new**: `viewCtx`, `parseViewCtx`, `encode`, `label`, `path`, `neighbors`, `itemNav`. Pure context/navigation logic.
- `plugin/feed/nav_internal_test.go` — **new**: white-box tests for `nav.go` + `resolveViewItems`.
- `plugin/feed/pages.go` — modify: `resolveViewItems`, `ItemDetail` (bar + sidebar + ctx), `AdjacentPlayer`/`MarkComplete`/`renderPlayerFragment` (ctx-aware), `buildSidebar` (hide_empty), `SavePlayerPrefs` (hide_empty), settings handlers split, `BulkDelete`, sidebar wrap for admin/sub/system settings handlers.
- `plugin/feed/pages.templ` — modify: thread ctx through `feedItemList`/`feedItemRow`/row templates; new `feedItemContextBar`, `feedSettingsTabs`; split `feedSettingsPage` into four; add hide-empty toggle to `feedSidebar`; wrap item/settings/admin templates in `sidebar-layout`; `feedPlayer` gains `nav`/`ctx`.
- `plugin/feed/store.go` — modify: `PlayerPrefs.HideEmpty`, load/save; `DeleteSubscriptions`/`DeleteFolders` bulk methods.
- `plugin/feed/static/player.js` — modify: read `data-ctx`, send ctx on `/adjacent` & `/complete`, `pushState` + bar update on swap, `popstate` handler.
- `plugin/feed/feed.go` — modify: register split settings routes + bulk-delete action.

---

## Task 1: `hide_empty` pref in the Store

**Files:**
- Modify: `plugin/feed/store.go` (`PlayerPrefs` struct ~1510, var block ~1500-1507, `LoadPlayerPrefs` ~1519, `SavePlayerPrefs` ~1544)
- Test: `plugin/feed/pages_prefs_test.go` (append) — confirm existing file's package first

- [ ] **Step 1: Confirm test package**

Run: `head -1 plugin/feed/pages_prefs_test.go`
Expected: `package feed_test`

- [ ] **Step 2: Write the failing test**

Append to `plugin/feed/pages_prefs_test.go`:

```go
func TestHideEmptyPrefRoundTrips(t *testing.T) {
	s := newStore(t)
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	if p.HideEmpty {
		t.Fatal("hide_empty should default to false")
	}
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))
	got, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	if !got.HideEmpty {
		t.Fatal("hide_empty did not persist")
	}
}
```

(`newStore` is defined in `store_test.go`; both are `package feed_test`.)

- [ ] **Step 3: Run test to verify it fails**

Run: `go test -run TestHideEmptyPrefRoundTrips ./plugin/feed`
Expected: FAIL — `p.HideEmpty` undefined (compile error).

- [ ] **Step 4: Add the setting var**

In `plugin/feed/store.go`, in the settings var block (next to `settingSkipSilence`):

```go
	settingHideEmpty     = BoolSetting("hide_empty", false)
```

- [ ] **Step 5: Add the struct field**

In `PlayerPrefs`:

```go
	SkipSilence  bool    `json:"skipSilence"`
	HideEmpty    bool    `json:"hideEmpty"`
```

- [ ] **Step 6: Load it**

In `LoadPlayerPrefs`, before `return p, nil`:

```go
	if p.HideEmpty, err = settingHideEmpty.Get(s.db); err != nil {
		return p, fmt.Errorf("load hide_empty: %w", err)
	}
```

- [ ] **Step 7: Save it**

In `SavePlayerPrefs`, before `return nil`:

```go
	if err := settingHideEmpty.Set(s.db, p.HideEmpty); err != nil {
		return fmt.Errorf("save hide_empty: %w", err)
	}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test -run TestHideEmptyPrefRoundTrips ./plugin/feed`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add plugin/feed/store.go plugin/feed/pages_prefs_test.go
git commit -m "feed: add per-user hide_empty player pref"
```

---

## Task 2: `viewCtx` descriptor + `neighbors` helper

**Files:**
- Create: `plugin/feed/nav.go`
- Create: `plugin/feed/nav_internal_test.go`

- [ ] **Step 1: Write the failing test (white-box)**

Create `plugin/feed/nav_internal_test.go`:

```go
package feed

import (
	"net/http/httptest"
	"testing"
)

func TestParseViewCtxAndEncode(t *testing.T) {
	r := httptest.NewRequest("GET", "/apps/feed/items/5?ctx=folder&cid=3", nil)
	c := parseViewCtx(r)
	if c.Kind != "folder" || c.ID != 3 {
		t.Fatalf("got %+v", c)
	}
	if got := c.encode(); got != "ctx=folder&cid=3" {
		t.Fatalf("encode = %q", got)
	}
}

func TestParseViewCtxDefaultsToAll(t *testing.T) {
	r := httptest.NewRequest("GET", "/apps/feed/items/5", nil)
	if c := parseViewCtx(r); c.Kind != "all" {
		t.Fatalf("got %+v", c)
	}
}

func TestNeighborsAnyType(t *testing.T) {
	items := []ItemWithSubscription{
		{Item: Item{ID: 1, Type: "text"}},
		{Item: Item{ID: 2, Type: "audio"}},
		{Item: Item{ID: 3, Type: "text"}},
	}
	prev, next := neighbors(items, 2, false)
	if prev == nil || prev.ID != 1 || next == nil || next.ID != 3 {
		t.Fatalf("prev=%v next=%v", prev, next)
	}
}

func TestNeighborsMediaOnlySkipsText(t *testing.T) {
	items := []ItemWithSubscription{
		{Item: Item{ID: 1, Type: "audio"}},
		{Item: Item{ID: 2, Type: "text"}},
		{Item: Item{ID: 3, Type: "video"}},
	}
	prev, next := neighbors(items, 2, true)
	if prev == nil || prev.ID != 1 || next == nil || next.ID != 3 {
		t.Fatalf("prev=%v next=%v", prev, next)
	}
}

func TestNeighborsBoundariesAndAbsent(t *testing.T) {
	items := []ItemWithSubscription{{Item: Item{ID: 1, Type: "audio"}}}
	if p, n := neighbors(items, 1, false); p != nil || n != nil {
		t.Fatal("single item should have no neighbors")
	}
	if p, n := neighbors(items, 99, false); p != nil || n != nil {
		t.Fatal("absent id should have no neighbors")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run 'TestParseViewCtx|TestNeighbors' ./plugin/feed`
Expected: FAIL — undefined `parseViewCtx`, `neighbors`, `viewCtx`.

- [ ] **Step 3: Implement `nav.go`**

Create `plugin/feed/nav.go`:

```go
package feed

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// viewCtx identifies the view ("playlist") an item was opened from, so the
// item page and player can compute prev/next within that exact list.
type viewCtx struct {
	Kind string // all|starred|history|folder|sub|tag|playlist
	ID   int64  // folder/sub/playlist id
	Name string // tag name
	Q    string // all-view search query
}

// parseViewCtx reads a viewCtx from request query params, defaulting to "all".
func parseViewCtx(r *http.Request) viewCtx {
	q := r.URL.Query()
	c := viewCtx{Kind: q.Get("ctx"), Name: q.Get("cname"), Q: q.Get("cq")}
	if id, err := strconv.ParseInt(q.Get("cid"), 10, 64); err == nil {
		c.ID = id
	}
	switch c.Kind {
	case "starred", "history", "folder", "sub", "tag", "playlist", "all":
	default:
		c.Kind = "all"
	}
	return c
}

// encode renders the ctx as a URL query string (no leading "?"), omitting
// empty fields. Field order is stable: ctx, cid, cname, cq.
func (c viewCtx) encode() string {
	v := url.Values{}
	v.Set("ctx", c.Kind)
	if c.ID != 0 {
		v.Set("cid", strconv.FormatInt(c.ID, 10))
	}
	if c.Name != "" {
		v.Set("cname", c.Name)
	}
	if c.Q != "" {
		v.Set("cq", c.Q)
	}
	// url.Values.Encode sorts keys; cid<cname<cq<ctx, so build manually for
	// a predictable, readable order.
	parts := []string{"ctx=" + url.QueryEscape(c.Kind)}
	if c.ID != 0 {
		parts = append(parts, "cid="+strconv.FormatInt(c.ID, 10))
	}
	if c.Name != "" {
		parts = append(parts, "cname="+url.QueryEscape(c.Name))
	}
	if c.Q != "" {
		parts = append(parts, "cq="+url.QueryEscape(c.Q))
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "&" + p
	}
	return out
}

// label returns a human-readable name for the context, doing best-effort store
// lookups for folder/sub/playlist names. Errors degrade to a generic label.
func (c viewCtx) label(store *Store) string {
	switch c.Kind {
	case "starred":
		return "Starred"
	case "history":
		return "History"
	case "folder":
		if f, err := store.GetFolder(c.ID); err == nil {
			return f.Name
		}
		return "Folder"
	case "sub":
		if s, err := store.GetSubscription(c.ID); err == nil {
			return subscriptionDisplayTitle(s)
		}
		return "Subscription"
	case "tag":
		return "Tag: " + c.Name
	case "playlist":
		if pl, err := store.GetPlaylist(c.ID); err == nil {
			return "Playlist: " + pl.Name
		}
		return "Playlist"
	default:
		return "All"
	}
}

// path returns the originating view's URL, used to highlight the right sidebar
// entry while viewing an item.
func (c viewCtx) path() string {
	switch c.Kind {
	case "starred":
		return "/apps/feed/starred"
	case "history":
		return "/apps/feed/history"
	case "folder":
		return fmt.Sprintf("/apps/feed/folder/%d", c.ID)
	case "sub":
		return fmt.Sprintf("/apps/feed/subscription/%d", c.ID)
	case "tag":
		return "/apps/feed/tag/" + url.PathEscape(c.Name)
	case "playlist":
		return fmt.Sprintf("/apps/feed/playlists/%d", c.ID)
	default:
		return "/apps/feed/"
	}
}

// itemNav holds everything the context bar and player need to render prev/next
// links. A zero ID means "no neighbor in that direction".
type itemNav struct {
	Ctx       string // encoded ctx (no leading "?")
	Label     string
	PrevID    int64
	PrevTitle string
	NextID    int64
	NextTitle string
}

// neighbors returns the items immediately before and after id in items. When
// mediaOnly is true, text items are skipped when choosing neighbors. Returns
// nil for a direction with no neighbor (boundary or id absent).
func neighbors(items []ItemWithSubscription, id int64, mediaOnly bool) (prev, next *ItemWithSubscription) {
	idx := -1
	for i := range items {
		if items[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, nil
	}
	playable := func(it ItemWithSubscription) bool {
		return !mediaOnly || it.Type == "audio" || it.Type == "video"
	}
	for i := idx - 1; i >= 0; i-- {
		if playable(items[i]) {
			prev = &items[i]
			break
		}
	}
	for i := idx + 1; i < len(items); i++ {
		if playable(items[i]) {
			next = &items[i]
			break
		}
	}
	return prev, next
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run 'TestParseViewCtx|TestNeighbors' ./plugin/feed`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/nav.go plugin/feed/nav_internal_test.go
git commit -m "feed: add viewCtx descriptor and neighbors helper"
```

---

## Task 3: `resolveViewItems`

**Files:**
- Modify: `plugin/feed/pages.go` (add method near `applyQueryFilter` ~392)
- Test: `plugin/feed/nav_internal_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `plugin/feed/nav_internal_test.go`:

```go
func TestResolveViewItemsAllMatchesListItemsForView(t *testing.T) {
	s := newStoreInternal(t)
	subID, err := s.CreateSubscription(Subscription{Kind: "rss", URL: "https://x.test/f"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := s.UpsertItem(Item{
			SubscriptionID: subID,
			GUID:           "g" + strconv.Itoa(i),
			Type:           "text",
			Title:          "t" + strconv.Itoa(i),
			PublishedAt:    int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	h := &Pages{}
	got, err := h.resolveViewItems(s, viewCtx{Kind: "all"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.ListItemsForView(ViewSpec{All: true}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || len(got) != 3 {
		t.Fatalf("got %d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Fatalf("order mismatch at %d", i)
		}
	}
}
```

Add a `newStoreInternal` helper at the top of `nav_internal_test.go` (white-box, mirrors `store_test.go`'s `newStore`):

```go
func newStoreInternal(t *testing.T) *Store {
	t.Helper()
	env := cogtest.New(t, New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*Plugin).DB(u.User.Username)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}
```

Add imports `"strconv"` and `"git.duckfam.us/jj-software/cog/internal/testing/cogtest"` to the test file. Verify `UpsertItem`'s exact name/signature first:

Run: `grep -n 'func (s \*Store) UpsertItem' plugin/feed/store.go`
If the name differs, use the actual item-insert method (search `grep -n 'func (s \*Store).*Item' plugin/feed/store.go`) and adjust the test.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestResolveViewItems ./plugin/feed`
Expected: FAIL — `h.resolveViewItems` undefined.

- [ ] **Step 3: Implement `resolveViewItems`**

In `plugin/feed/pages.go`, after `applyQueryFilter`:

```go
// resolveViewItems returns the same ordered item slice the originating view
// rendered, so prev/next neighbors match exactly what the user saw.
func (h *Pages) resolveViewItems(store *Store, c viewCtx) ([]ItemWithSubscription, error) {
	const limit = 500
	switch c.Kind {
	case "starred":
		return store.ListItemsForView(ViewSpec{Starred: true}, limit)
	case "folder":
		return store.ListItemsForView(ViewSpec{FolderID: validID(c.ID)}, limit)
	case "sub":
		return store.ListItemsForView(ViewSpec{SubscriptionID: validID(c.ID)}, limit)
	case "tag":
		return store.ListItemsForView(ViewSpec{TagName: c.Name}, limit)
	case "playlist":
		pl, err := store.GetPlaylist(c.ID)
		if err != nil {
			return nil, fmt.Errorf("resolve playlist %d: %w", c.ID, err)
		}
		items, err := store.ListItemsForView(ViewSpec{All: true}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve playlist items: %w", err)
		}
		return applyQueryFilter(store, items, pl.Query), nil
	case "history":
		played, err := store.ListPlaybackHistory(limit)
		if err != nil {
			return nil, fmt.Errorf("resolve history: %w", err)
		}
		out := make([]ItemWithSubscription, 0, len(played))
		for _, p := range played {
			out = append(out, ItemWithSubscription{Item: p.Item, SubscriptionTitle: p.SubscriptionTitle})
		}
		return out, nil
	default: // all
		items, err := store.ListItemsForView(ViewSpec{All: true}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve all items: %w", err)
		}
		if c.Q != "" {
			items = applyQueryFilter(store, items, c.Q)
		}
		return items, nil
	}
}

// validID wraps an int64 as a valid sql.NullInt64.
func validID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }
```

Confirm `database/sql` is imported in `pages.go` (it is used elsewhere; if not, add it). Confirm `ListPlaybackHistory` exists:

Run: `grep -n 'func (s \*Store) ListPlaybackHistory' plugin/feed/store.go`
Adjust the call if the name/return differs.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestResolveViewItems ./plugin/feed`
Expected: PASS

- [ ] **Step 5: Run the full feed package**

Run: `go test ./plugin/feed`
Expected: PASS (no regressions)

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/nav_internal_test.go
git commit -m "feed: add resolveViewItems context resolver"
```

---

## Task 4: Thread ctx through item links in templates

The list-page templates pass `(items, csrfToken)` to `feedItemList`. Add a `ctx string` (the encoded `viewCtx`) parameter so each item link carries the context. The three item-detail anchors (text title, media title, media Play) get `?{ctx}` appended.

**Files:**
- Modify: `plugin/feed/pages.templ` (`feedItemList`, `feedItemRow`, `feedTextItemRow`, `feedMediaItemRow`, and every page template that calls `feedItemList`: `feedIndexPage`, `feedSubscriptionPage`, `feedFolderPage`, `feedStarredPage`, `feedTagPage`, `feedPlaylistPage`)
- Modify: `plugin/feed/pages.go` (view handlers that render those pages: `Index`, `SubscriptionView`, `FolderView`, `StarredView`, `TagView`, `PlaylistView`)

- [ ] **Step 1: Change `feedItemList` and row signatures**

In `pages.templ`:

```go
templ feedItemList(items []ItemWithSubscription, csrfToken string, ctx string) {
	<div id="item-list">
		for _, item := range items {
			@feedItemRow(item, csrfToken, ctx)
		}
	</div>
	// ... unchanged script ...
}

templ feedItemRow(item ItemWithSubscription, csrfToken string, ctx string) {
	if item.Type == "audio" || item.Type == "video" {
		@feedMediaItemRow(item, csrfToken, ctx)
	} else {
		@feedTextItemRow(item, csrfToken, ctx)
	}
}
```

(Confirm the existing `feedItemRow` dispatch logic at line ~370 and preserve it; only add the `ctx` param.)

- [ ] **Step 2: Append ctx to the three item links**

Add a helper at the bottom of `pages.templ` (Go func section):

```go
// itemHref builds an item-detail URL carrying the view context.
func itemHref(id int64, ctx string) string {
	if ctx == "" {
		return fmt.Sprintf("/apps/feed/items/%d", id)
	}
	return fmt.Sprintf("/apps/feed/items/%d?%s", id, ctx)
}
```

In `feedTextItemRow` change the title link (line ~395):

```go
					<a
						href={ templ.SafeURL(itemHref(item.ID, ctx)) }
						style="font-weight:500;color:inherit;text-decoration:none"
						onclick="event.stopPropagation()"
					>{ item.Title }</a>
```

In `feedMediaItemRow` change the title link (~470) and the Play link (~476) the same way:

```go
					<a
						href={ templ.SafeURL(itemHref(item.ID, ctx)) }
						style="font-weight:500;color:inherit;text-decoration:none"
					>{ item.Title }</a>
```

```go
					<a
						href={ templ.SafeURL(itemHref(item.ID, ctx)) }
						class="btn btn-outline btn-sm"
						title={ "Play " + item.Type }
					>&#9654; Play</a>
```

Update the `feedTextItemRow` / `feedMediaItemRow` signatures to add `ctx string`.

- [ ] **Step 3: Update page templates to accept + forward ctx**

For each of `feedIndexPage`, `feedSubscriptionPage`, `feedFolderPage`, `feedStarredPage`, `feedTagPage`, `feedPlaylistPage`: add a `ctx string` parameter and pass it to `@feedItemList(items, csrfToken, ctx)`. Example for `feedStarredPage`:

```go
templ feedStarredPage(sb sidebarData, items []ItemWithSubscription, csrfToken string, ctx string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			// ... existing header ...
			@feedItemList(items, csrfToken, ctx)
		</div>
	</div>
}
```

- [ ] **Step 4: Update the view handlers to compute + pass ctx**

In `pages.go`, each handler builds the matching `viewCtx` and passes `c.encode()`:

```go
// Index
c := viewCtx{Kind: "all", Q: q}
feedIndexPage(sb, items, q, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
```

```go
// StarredView
c := viewCtx{Kind: "starred"}
feedStarredPage(sb, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
```

```go
// SubscriptionView (id is the sub id)
c := viewCtx{Kind: "sub", ID: id}
feedSubscriptionPage(sb, sub, items, plugin.CSRFToken(r), c.encode()).Render(...)
```

```go
// FolderView
c := viewCtx{Kind: "folder", ID: id}
```

```go
// TagView (name is the tag)
c := viewCtx{Kind: "tag", Name: name}
```

```go
// PlaylistView
c := viewCtx{Kind: "playlist", ID: id}
```

(Use the actual local variable names already present in each handler. For `TagView`, confirm the path-value variable name with `grep -n 'func (h \*Pages) TagView' -A8 plugin/feed/pages.go`.)

- [ ] **Step 5: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test ./plugin/feed`
Expected: builds clean; tests PASS.

- [ ] **Step 6: Format templ + commit**

```bash
go tool templ fmt .
git add plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages.go
git commit -m "feed: carry view context on item links"
```

---

## Task 5: Context bar + sidebar on the item page

**Files:**
- Modify: `plugin/feed/pages.templ` (new `feedItemContextBar`; wrap `feedItemDetail` in sidebar-layout; `feedItemDetail` gains `sb` + `nav`)
- Modify: `plugin/feed/pages.go` (`ItemDetail`)
- Test: `plugin/feed/pages_reader_test.go` (append) — confirm package is `feed_test`

- [ ] **Step 1: Write the failing test**

Append to `plugin/feed/pages_reader_test.go` (use existing seed helpers in that file or `pages_index_test.go`'s `seedFeedItems`; verify names with `grep -n 'func seed' plugin/feed/*_test.go`):

```go
func TestItemDetailShowsContextBar(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	textID, audioID := seedFeedItems(t, env, u.User.Username) // newest first ordering
	_ = audioID
	resp := env.Get(t, fmt.Sprintf("/apps/feed/items/%d?ctx=all", textID), u.Auth)
	resp.Status(200)
	resp.Contains("feed-context-bar")
	resp.Contains("All")        // context label
	resp.Contains("feed-sidebar") // sidebar present on item page
}
```

Adjust seeded IDs/labels to whatever `seedFeedItems` returns. The assertion only needs: bar element present, label "All", sidebar present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestItemDetailShowsContextBar ./plugin/feed`
Expected: FAIL — no `feed-context-bar`/`feed-sidebar` in item response.

- [ ] **Step 3: Add the context bar template**

In `pages.templ`:

```go
templ feedItemContextBar(nav itemNav) {
	<div class="feed-context-bar" style="display:flex;align-items:center;justify-content:space-between;gap:1rem;margin-bottom:1rem">
		<span class="text-muted">{ nav.Label }</span>
		<div class="flex gap-1">
			if nav.PrevID != 0 {
				<a class="btn btn-outline btn-sm" href={ templ.SafeURL(itemHref(nav.PrevID, nav.Ctx)) } title={ nav.PrevTitle }>&#8592; Prev</a>
			} else {
				<span class="btn btn-outline btn-sm" style="opacity:0.5">&#8592; Prev</span>
			}
			if nav.NextID != 0 {
				<a class="btn btn-outline btn-sm" href={ templ.SafeURL(itemHref(nav.NextID, nav.Ctx)) } title={ nav.NextTitle }>Next &#8594;</a>
			} else {
				<span class="btn btn-outline btn-sm" style="opacity:0.5">Next &#8594;</span>
			}
		</div>
	</div>
}
```

- [ ] **Step 4: Wrap `feedItemDetail` in the sidebar layout + bar**

Change `feedItemDetail` signature and body:

```go
templ feedItemDetail(sb sidebarData, nav itemNav, ip ItemPlayback, tags []string, prefs PlayerPrefs, artwork string, csrfToken string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedItemContextBar(nav)
			@ui.Card(ip.Title) {
				// ... existing card body unchanged, but the player call gains nav/ctx (Task 7) ...
			}
		</div>
	</div>
}
```

Keep the existing card inner content. The `@feedPlayer(...)` call inside gets its new args in Task 7; for now pass `nav.Ctx` and `nav` once `feedPlayer`'s signature changes — to keep this task self-contained, temporarily call `feedPlayer(ip, prefs, artwork, csrfToken)` unchanged and update it in Task 7. (If you implement Task 7 first, use the new signature here.)

- [ ] **Step 5: Update `ItemDetail` handler**

In `pages.go` `ItemDetail`, after loading `ip` and before render:

```go
	c := parseViewCtx(r)
	items, rerr := h.resolveViewItems(store, c)
	if rerr != nil {
		slog.Error("feed: resolve view items", "err", rerr)
	}
	prev, next := neighbors(items, id, false)
	nav := itemNav{Ctx: c.encode(), Label: c.label(store)}
	if prev != nil {
		nav.PrevID, nav.PrevTitle = prev.ID, prev.Title
	}
	if next != nil {
		nav.NextID, nav.NextTitle = next.ID, next.Title
	}
	sb := h.buildSidebar(store, c.path())
	// ... existing prefs/tags/artwork loads ...
	feedItemDetail(sb, nav, ip, tags, prefs, artwork, plugin.CSRFToken(r)).Render(r.Context(), w)
```

- [ ] **Step 6: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test -run TestItemDetailShowsContextBar ./plugin/feed`
Expected: PASS

- [ ] **Step 7: Full package + commit**

```bash
go test ./plugin/feed && go tool templ fmt .
git add plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages.go plugin/feed/pages_reader_test.go
git commit -m "feed: context bar and sidebar on item page"
```

---

## Task 6: Context-aware player endpoints + nav data

The player advances media-only within the context. `renderPlayerFragment`, `AdjacentPlayer`, and `MarkComplete` become ctx-aware; the player fragment carries `data-ctx` and the bar's any-type neighbors so the client can update the bar after an in-place swap.

**Files:**
- Modify: `plugin/feed/pages.go` (`renderPlayerFragment`, `AdjacentPlayer`, `MarkComplete`)
- Modify: `plugin/feed/pages.templ` (`feedPlayer` gains `nav itemNav`; data attributes)
- Test: `plugin/feed/pages_player_test.go` (append) — confirm package `feed_test`

- [ ] **Step 1: Write the failing test**

Append to `plugin/feed/pages_player_test.go` (seed two media items; verify the adjacent endpoint advances within ctx and the fragment carries data-ctx). Inspect existing helpers first: `grep -n 'func ' plugin/feed/pages_player_test.go`.

```go
func TestAdjacentPlayerCarriesCtx(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	a1, a2 := seedTwoMedia(t, env, u.User.Username) // a1 older, a2 newer (published_at)
	// From a1, "next" should reach a2 and echo the ctx.
	resp := env.Get(t, fmt.Sprintf("/apps/feed/items/%d/adjacent?dir=next&ctx=all", a1), u.Auth)
	resp.Status(200)
	resp.Contains(`data-ctx="ctx=all"`)
	resp.Contains(fmt.Sprintf(`data-item-id="%d"`, a2))
}
```

If no `seedTwoMedia` helper exists, write one in the test file using `feed.NewStore(db)` + `CreateSubscription` + the item-insert method (mirror `seedFeedItems`), inserting two `Type:"audio"` items with increasing `PublishedAt`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestAdjacentPlayerCarriesCtx ./plugin/feed`
Expected: FAIL — fragment lacks `data-ctx`; endpoint ignores ctx.

- [ ] **Step 3: `feedPlayer` gains nav + data attributes**

In `pages.templ`, change `feedPlayer` signature to add `nav itemNav` and add data attributes on the root `#feed-player` div (next to existing `data-*`):

```go
templ feedPlayer(ip ItemPlayback, prefs PlayerPrefs, artwork string, csrfToken string, nav itemNav) {
	<div
		id="feed-player"
		class="card feed-player"
		data-item-id={ strconv.FormatInt(ip.ID, 10) }
		data-ctx={ nav.Ctx }
		data-bar-prev-id={ strconv.FormatInt(nav.PrevID, 10) }
		data-bar-prev-title={ nav.PrevTitle }
		data-bar-next-id={ strconv.FormatInt(nav.NextID, 10) }
		data-bar-next-title={ nav.NextTitle }
		// ... all existing data-* attributes unchanged ...
	>
```

Update the `@feedPlayer(...)` call site inside `feedItemDetail` (Task 5) to `@feedPlayer(ip, prefs, artwork, csrfToken, nav)`.

- [ ] **Step 4: Make `renderPlayerFragment` ctx-aware**

Change its signature and body in `pages.go`:

```go
func renderPlayerFragment(w http.ResponseWriter, r *http.Request, h *Pages, store *Store, itemID int64, c viewCtx) bool {
	ip, err := store.GetItemPlayback(itemID)
	if err != nil {
		return false
	}
	prefs, perr := store.LoadPlayerPrefs()
	if perr != nil {
		prefs = PlayerPrefs{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1, AutoplayNext: true}
	}
	items, _ := h.resolveViewItems(store, c)
	prev, next := neighbors(items, itemID, false) // any-type neighbors for the bar
	nav := itemNav{Ctx: c.encode(), Label: c.label(store)}
	if prev != nil {
		nav.PrevID, nav.PrevTitle = prev.ID, prev.Title
	}
	if next != nil {
		nav.NextID, nav.NextTitle = next.ID, next.Title
	}
	feedPlayer(ip, prefs, ip.ThumbnailURL, plugin.CSRFToken(r), nav).Render(r.Context(), w)
	return true
}
```

Note: it now takes `h *Pages`. Update both call sites (`MarkComplete`, `AdjacentPlayer`) accordingly.

- [ ] **Step 5: `AdjacentPlayer` resolves media neighbor within ctx**

Replace the `store.AdjacentMedia` call:

```go
func (h *Pages) AdjacentPlayer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir != "next" && dir != "prev" {
		http.Error(w, "dir must be next or prev", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c := parseViewCtx(r)
	items, err := h.resolveViewItems(store, c)
	if err != nil {
		slog.Error("feed: adjacent resolve", "err", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	prev, next := neighbors(items, id, true) // media-only for player nav
	target := next
	if dir == "prev" {
		target = prev
	}
	if target == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !renderPlayerFragment(w, r, h, store, target.ID, c) {
		w.WriteHeader(http.StatusNoContent)
	}
}
```

- [ ] **Step 6: `MarkComplete` auto-advances within ctx (media-only forward)**

Replace the `store.NextUnplayedMedia` block:

```go
	c := parseViewCtx(r)
	items, rerr := h.resolveViewItems(store, c)
	if rerr != nil {
		slog.Error("feed: complete resolve", "err", rerr)
	}
	_, next := neighbors(items, id, true)
	if next != nil {
		if renderPlayerFragment(w, r, h, store, next.ID, c) {
			return
		}
	}
	w.WriteHeader(http.StatusOK)
```

`MarkComplete` reads ctx from the request: the client sends it (Task 7). Keep the existing `SetPlayback`/`MarkRead` calls above this block.

- [ ] **Step 7: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test -run TestAdjacentPlayerCarriesCtx ./plugin/feed`
Expected: PASS

- [ ] **Step 8: Full package**

Run: `go test ./plugin/feed`
Expected: PASS. If `AdjacentMedia`/`NextUnplayedMedia` are now unreferenced and `go vet`/`verify` complains as unused (they are exported methods, so unused is allowed), leave them; otherwise no change needed.

- [ ] **Step 9: Format + commit**

```bash
go tool templ fmt .
git add plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages_player_test.go
git commit -m "feed: context-aware player navigation endpoints"
```

---

## Task 7: player.js — ctx, history push, popstate, bar update

**Files:**
- Modify: `plugin/feed/static/player.js`

No Go test covers JS; verify by build + manual checklist. Keep edits minimal and localized.

- [ ] **Step 1: Read ctx into cfg**

In `init()` `cfg` object (~43), add:

```js
			ctx: root.dataset.ctx || "ctx=all",
			barPrevId: root.dataset.barPrevId || "0",
			barPrevTitle: root.dataset.barPrevTitle || "",
			barNextId: root.dataset.barNextId || "0",
			barNextTitle: root.dataset.barNextTitle || "",
```

- [ ] **Step 2: Send ctx on `/adjacent` and `/complete`**

In `advance(dir)` (~367) change the URL:

```js
			fetch("/apps/feed/items/" + cfg.itemID + "/adjacent?dir=" + dir + "&" + cfg.ctx, {
```

In the `ended` handler (~334) change the complete POST to include ctx as a query string:

```js
				post("/apps/feed/items/" + cfg.itemID + "/complete?" + cfg.ctx, "", cfg.csrf)
```

(Confirm `post()` builds the URL verbatim; it does — it takes a url string. The server reads ctx from `r.URL.Query()` which works for POST too.)

- [ ] **Step 3: Push history + update bar on swap**

Replace `swapPlayer` (~354):

```js
		function swapPlayer(html) {
			var tmp = document.createElement("div");
			tmp.innerHTML = html;
			var next = tmp.querySelector("#feed-player");
			if (next && root.parentNode) {
				root.parentNode.replaceChild(next, root);
				var id = next.dataset.itemId;
				var ctx = next.dataset.ctx || "ctx=all";
				history.pushState({ itemId: id }, "", "/apps/feed/items/" + id + "?" + ctx);
				updateContextBar(next);
				init(); // bind the new player
			}
		}

		// updateContextBar rewrites the page's context bar prev/next links from
		// the freshly-swapped player fragment's data attributes.
		function updateContextBar(playerEl) {
			var bar = document.querySelector(".feed-context-bar");
			if (!bar) return;
			var ctx = playerEl.dataset.ctx || "ctx=all";
			var links = bar.querySelectorAll(".btn");
			// bar markup: [0] = prev, [1] = next
			rewireNavBtn(links[0], playerEl.dataset.barPrevId, playerEl.dataset.barPrevTitle, ctx, "← Prev");
			rewireNavBtn(links[1], playerEl.dataset.barNextId, playerEl.dataset.barNextTitle, ctx, "Next →");
		}

		function rewireNavBtn(el, id, title, ctx, text) {
			if (!el) return;
			var span = document.createElement(id && id !== "0" ? "a" : "span");
			span.className = "btn btn-outline btn-sm";
			span.textContent = text;
			if (id && id !== "0") {
				span.setAttribute("href", "/apps/feed/items/" + id + "?" + ctx);
				if (title) span.setAttribute("title", title);
			} else {
				span.style.opacity = "0.5";
			}
			el.parentNode.replaceChild(span, el);
		}
```

- [ ] **Step 4: Add popstate handler (module scope, near the bottom `htmx:afterSwap` listener ~488)**

```js
	window.addEventListener("popstate", function () {
		// Back/forward to a previous item: reload to render that item's page.
		window.location.reload();
	});
```

- [ ] **Step 5: Build (asset is content-hashed; regen if needed)**

Static assets are content-hash cache-busted (see recent commits). Confirm whether a generate step hashes them:

Run: `grep -rn 'player.js' plugin/feed/feed.go plugin/feed/*.go | grep -i 'asset\|hash\|playerAssetURL'`
If `playerAssetURL()` computes the hash at runtime from file bytes, no regen needed. If a generator embeds a manifest, run `go generate ./...`.

Run: `go build ./...`
Expected: clean.

- [ ] **Step 6: Manual smoke (document for executor, not automated)**

Run dev server: `go tool dev` → open `http://localhost:8580/apps/feed/`, log in `admin:admin`. Open a media item with `?ctx=all`. Verify: (a) bar shows "All" + prev/next; (b) clicking player Next swaps player and the URL changes to `/items/<id>?ctx=all` (history entry); (c) browser Back returns to the previous item; (d) autoplay at end advances within context.

- [ ] **Step 7: Commit**

```bash
git add plugin/feed/static/player.js
git commit -m "feed: player context nav, history push, bar sync"
```

---

## Task 8: Hide-empty sidebar filter + toggle

**Files:**
- Modify: `plugin/feed/pages.go` (`buildSidebar`, `SavePlayerPrefs`)
- Modify: `plugin/feed/pages.templ` (`sidebarData` gets `HideEmpty bool`; toggle in `feedSidebar`; filter folders/subs)
- Test: `plugin/feed/pages_test.go` (append) — confirm package `feed_test`

- [ ] **Step 1: Write the failing test**

Append to `plugin/feed/pages_test.go`:

```go
func TestSidebarHidesEmptyWhenEnabled(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	db, err := pluginDB(t, env, u.User.Username) // helper: returns *sql.DB for the user
	require.NoError(t, err)
	s := feed.NewStore(db)
	// One sub with an unread item, one sub with none.
	withItems, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://a.test/f", Title: "HasUnread"})
	require.NoError(t, err)
	_, err = s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://b.test/f", Title: "Empty"})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: withItems, GUID: "g1", Type: "text", Title: "x"})
	require.NoError(t, err)
	// Enable hide_empty.
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))

	resp := env.Get(t, "/apps/feed/", u.Auth)
	resp.Status(200)
	resp.Contains("HasUnread")
	if strings.Contains(resp.ReadBody(), "Empty") {
		t.Fatal("empty subscription should be hidden")
	}
}
```

Verify helper names: `grep -n 'func pluginDB\|UpsertItem\|func (r \*Resp) Body\|Body()' plugin/feed/*_test.go internal/testing/cogtest/*.go`. If there's no `pluginDB` helper, inline the `raw.(*feed.Plugin).DB(...)` pattern from `store_test.go`. If the response type lacks `Body()`, use `resp.Contains`/the negative via a separate `NotContains` if available, else fetch body via the documented accessor.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestSidebarHidesEmptyWhenEnabled ./plugin/feed`
Expected: FAIL — "Empty" still present.

- [ ] **Step 3: Add `HideEmpty` to `sidebarData` and load it**

In `pages.templ` `sidebarData` struct:

```go
	HideEmpty     bool
```

In `pages.go` `buildSidebar`, load the pref and set it:

```go
func (h *Pages) buildSidebar(store *Store, activePath string) sidebarData {
	// ... existing loads ...
	prefs, _ := store.LoadPlayerPrefs() // HideEmpty default false on error
	return sidebarData{
		// ... existing fields ...
		HideEmpty: prefs.HideEmpty,
	}
}
```

- [ ] **Step 4: Filter in the template**

In `feedSidebar`, guard each subscription and folder render. For loose subs (~61):

```go
		for _, sub := range sb.Subscriptions {
			if !sub.FolderID.Valid {
				if !sb.HideEmpty || sb.UnreadCounts[sub.ID] > 0 {
					@feedSidebarSubLink(sub, sb)
				}
			}
		}
```

For folders (~42), wrap the `<details>` in a visibility guard (folder hidden when its unread sum is 0):

```go
		for _, folder := range sb.Folders {
			if !sb.HideEmpty || sb.FolderUnread[folder.ID] > 0 {
				<details class="sidebar-folder" ...>
					// ... inside the folder's sub loop, also guard: ...
					for _, sub := range sb.Subscriptions {
						if sub.FolderID.Valid && sub.FolderID.Int64 == folder.ID {
							if !sb.HideEmpty || sb.UnreadCounts[sub.ID] > 0 {
								@feedSidebarSubLink(sub, sb)
							}
						}
					}
				</details>
			}
		}
```

This hides errored-with-0-unread subs/folders too (errors don't force visibility), per spec.

- [ ] **Step 5: Add the toggle to the sidebar**

At the top of `feedSidebar` (just inside `<nav>`, before the "All" link), add a small toggle that posts the pref and refreshes the sidebar:

```go
		<label class="sidebar-hide-empty" style="display:flex;align-items:center;gap:0.4rem;padding:0.25rem 0.5rem;font-size:0.8rem;color:var(--color-muted)">
			<input
				type="checkbox"
				checked?={ sb.HideEmpty }
				hx-post="/apps/feed/player/prefs"
				hx-vals='js:{hide_empty: event.target.checked ? "1" : "0"}'
				hx-target="#feed-sidebar"
				hx-swap="outerHTML"
				name="hide_empty"
			/>
			Hide read
		</label>
```

The `hx-post` returns the refreshed sidebar (Step 6). Confirm CSRF: other feed HTMX posts rely on a global `htmx:configRequest` CSRF header (check `grep -rn 'X-CSRF\|configRequest' ui/ internal/web/ | head`). If feed pages include the CSRF token via a meta/JS hook already (the item-list script reads `[name=csrf_token]`), ensure the sidebar page includes that token input; if not, add `hx-headers` with the token. Verify before relying on it.

- [ ] **Step 6: `SavePlayerPrefs` accepts `hide_empty` and returns refreshed sidebar for HTMX**

In `pages.go` `SavePlayerPrefs`, before the save:

```go
	if v := r.FormValue("hide_empty"); v != "" {
		cur.HideEmpty = v == "true" || v == "1" || v == "on"
	}
```

After a successful save, if the request is an HTMX sidebar toggle, return the refreshed sidebar instead of 204:

```go
	if r.Header.Get("HX-Target") == "feed-sidebar" {
		sb := h.buildSidebar(store, "/apps/feed/")
		feedSidebar(sb).Render(r.Context(), w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
```

(`feedSidebar` renders the `<nav id="feed-sidebar">` + its script; an `outerHTML` swap of `#feed-sidebar` replaces it cleanly. The active-path highlight resets to "All" on toggle, acceptable; if you want to preserve it, thread the current path via an `hx-vals` field — out of scope.)

- [ ] **Step 7: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test -run TestSidebarHidesEmptyWhenEnabled ./plugin/feed`
Expected: PASS

- [ ] **Step 8: Format + commit**

```bash
go tool templ fmt .
git add plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages_test.go
git commit -m "feed: hide-empty sidebar filter and toggle"
```

---

## Task 9: Bulk-delete store methods

**Files:**
- Modify: `plugin/feed/store.go` (add `DeleteSubscriptions`, `DeleteFolders`)
- Test: `plugin/feed/store_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `plugin/feed/store_test.go`:

```go
func TestBulkDelete(t *testing.T) {
	s := newStore(t)
	s1, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://1.test/f"})
	require.NoError(t, err)
	s2, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://2.test/f"})
	require.NoError(t, err)
	f1, err := s.CreateFolder("F1")
	require.NoError(t, err)
	require.NoError(t, s.DeleteSubscriptions([]int64{s1, s2}))
	require.NoError(t, s.DeleteFolders([]int64{f1}))
	subs, _ := s.ListSubscriptions()
	if len(subs) != 0 {
		t.Fatalf("subs not deleted: %+v", subs)
	}
	folders, _ := s.ListFolders()
	if len(folders) != 0 {
		t.Fatalf("folders not deleted: %+v", folders)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestBulkDelete ./plugin/feed`
Expected: FAIL — undefined `DeleteSubscriptions`/`DeleteFolders`.

- [ ] **Step 3: Implement (reuse single-delete to preserve cascade semantics)**

In `store.go`:

```go
// DeleteSubscriptions deletes each subscription by id, stopping at the first
// error (which it wraps). Empty input is a no-op.
func (s *Store) DeleteSubscriptions(ids []int64) error {
	for _, id := range ids {
		if err := s.DeleteSubscription(id); err != nil {
			return fmt.Errorf("bulk delete subscription %d: %w", id, err)
		}
	}
	return nil
}

// DeleteFolders deletes each folder by id, stopping at the first error.
func (s *Store) DeleteFolders(ids []int64) error {
	for _, id := range ids {
		if err := s.DeleteFolder(id); err != nil {
			return fmt.Errorf("bulk delete folder %d: %w", id, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -run TestBulkDelete ./plugin/feed`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: bulk delete subscriptions and folders"
```

---

## Task 10: Split settings into four pages + tabs + routes

**Files:**
- Modify: `plugin/feed/pages.templ` (new `feedSettingsTabs`; replace `feedSettingsPage` with `feedSettingsSubscriptionsPage`, `feedSettingsBulkDeletePage`, `feedSettingsFiltersPage`, `feedSettingsPlaylistsPage`; all sidebar-wrapped)
- Modify: `plugin/feed/pages.go` (replace `Settings` with `SettingsSubscriptions`, `SettingsBulkDelete`, `SettingsFilters`, `SettingsPlaylists`, `BulkDelete`; redirect old `/settings`; wrap `SubscriptionSettings`, `SystemSettings`, `AdminStatus` in sidebar)
- Modify: `plugin/feed/feed.go` (route registration)
- Test: `plugin/feed/settings_test.go` (append) — confirm package `feed_test`

This is the largest task; build incrementally and keep the package compiling between steps.

- [ ] **Step 1: Write the failing tests**

Append to `plugin/feed/settings_test.go`:

```go
func TestSettingsRedirectsToSubscriptions(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	resp := env.Get(t, "/apps/feed/settings", u.Auth, cogtest.StopRedirect)
	resp.Status(303)
	if loc := resp.Header.Get("Location"); loc != "/apps/feed/settings/subscriptions" {
		t.Fatalf("Location = %q", loc)
	}
}

func TestSettingsPagesRender(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	for _, p := range []string{"subscriptions", "bulk-delete", "filters", "playlists"} {
		resp := env.Get(t, "/apps/feed/settings/"+p, u.Auth)
		resp.Status(200)
		resp.Contains("feed-sidebar")
		resp.Contains("feed-settings-tabs")
	}
}

func TestBulkDeleteRemovesSelected(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	db, err := pluginDB(t, env, u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	id, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f"})
	require.NoError(t, err)
	form := url.Values{"sub_id": {fmt.Sprint(id)}}
	resp := env.PostForm(t, "/apps/feed/settings/bulk-delete", form, u.Auth, cogtest.StopRedirect)
	resp.Status(303)
	subs, _ := s.ListSubscriptions()
	if len(subs) != 0 {
		t.Fatalf("subscription not bulk-deleted: %+v", subs)
	}
}
```

Confirm the helper names in `cogtest` (`StopRedirect`, `PostForm`, `Header`, `PostJSON`): `grep -rn 'func.*StopRedirect\|func (.*Env) Post\|func (.*Resp) Header' internal/testing/cogtest/*.go`. Adjust method names to match (e.g. it may be `env.Do` with a method+body). Use `pluginDB` or inline the DB accessor as in earlier tasks.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run 'TestSettings|TestBulkDelete' ./plugin/feed`
Expected: FAIL — routes/handlers not present.

- [ ] **Step 3: Add the tab bar template**

In `pages.templ`:

```go
templ feedSettingsTabs(active string, isAdmin bool) {
	<div class="feed-settings-tabs" style="display:flex;gap:0.5rem;margin-bottom:1rem;flex-wrap:wrap">
		@feedSettingsTab("subscriptions", "Subscriptions", active)
		@feedSettingsTab("bulk-delete", "Bulk Delete", active)
		@feedSettingsTab("filters", "Filters", active)
		@feedSettingsTab("playlists", "Playlists", active)
		if isAdmin {
			<a class="btn btn-outline btn-sm" href="/apps/feed/admin/system-settings">System</a>
			<a class="btn btn-outline btn-sm" href="/apps/feed/admin/status">Status</a>
		}
	</div>
}

templ feedSettingsTab(key string, label string, active string) {
	if key == active {
		<span class="btn btn-sm" aria-current="page">{ label }</span>
	} else {
		<a class="btn btn-outline btn-sm" href={ templ.SafeURL("/apps/feed/settings/" + key) }>{ label }</a>
	}
}
```

- [ ] **Step 4: Create the four page templates**

Split the current `feedSettingsPage` (pages.templ ~525-678) content into four sidebar-wrapped pages. Each wraps in `sidebar-layout` + `@feedSidebar(sb)` + `@feedSettingsTabs(<key>, isAdmin)`. Reuse the existing card markup verbatim — move, don't rewrite.

```go
templ feedSettingsSubscriptionsPage(sb sidebarData, isAdmin bool, subs []Subscription, folders []Folder, flashMsg string, flashKind string, csrfToken string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedSettingsTabs("subscriptions", isAdmin)
			// MOVE here: the add-subscription card, add-folder card, OPML import/export
			// card, other-format import card, and the subscription + folder lists from
			// the old feedSettingsPage. (Filters and Playlists cards move to their pages.)
		</div>
	</div>
}

templ feedSettingsBulkDeletePage(sb sidebarData, isAdmin bool, subs []Subscription, folders []Folder, flashMsg string, flashKind string, csrfToken string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedSettingsTabs("bulk-delete", isAdmin)
			@ui.Card("Bulk delete") {
				<form method="post" action="/apps/feed/settings/bulk-delete">
					<input type="hidden" name="csrf_token" value={ csrfToken }/>
					<p class="text-muted">Select folders and subscriptions to delete.</p>
					if len(folders) > 0 {
						<h4>Folders</h4>
						for _, f := range folders {
							<label style="display:block">
								<input type="checkbox" name="folder_id" value={ strconv.FormatInt(f.ID, 10) }/>
								{ f.Name }
							</label>
						}
					}
					<h4>Subscriptions</h4>
					for _, s := range subs {
						<label style="display:block">
							<input type="checkbox" name="sub_id" value={ strconv.FormatInt(s.ID, 10) }/>
							{ subscriptionDisplayTitle(s) }
						</label>
					}
					<button class="btn btn-danger" type="submit" style="margin-top:0.75rem">Delete selected</button>
				</form>
			}
		</div>
	</div>
}

templ feedSettingsFiltersPage(sb sidebarData, isAdmin bool, filters []Filter, flashMsg string, flashKind string, csrfToken string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedSettingsTabs("filters", isAdmin)
			// MOVE here: the Filters card (add form with live filter-preview + list + delete).
		</div>
	</div>
}

templ feedSettingsPlaylistsPage(sb sidebarData, isAdmin bool, playlists []Playlist, flashMsg string, flashKind string, csrfToken string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedSettingsTabs("playlists", isAdmin)
			// MOVE here: the Playlists card (add form + list + delete).
		</div>
	</div>
}
```

Delete the old `feedSettingsPage` template once its content has been distributed. If a shared flash banner partial is used inside the old page, keep rendering it at the top of each new page's `sidebar-content`.

- [ ] **Step 5: Replace the `Settings` handler with four + a redirect**

In `pages.go`, remove the old `Settings` body and add:

```go
// SettingsRedirect sends the bare /settings path to the default tab.
func (h *Pages) SettingsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

func (h *Pages) SettingsSubscriptions(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	subs, _ := store.ListSubscriptions()
	folders, _ := store.ListFolders()
	msg, kind := h.flashParts(w, r)
	sb := h.buildSidebar(store, "/apps/feed/settings")
	plugin.SetTitle(w, "Feed Settings")
	feedSettingsSubscriptionsPage(sb, h.isAdmin(r), subs, folders, msg, kind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

func (h *Pages) SettingsBulkDelete(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	subs, _ := store.ListSubscriptions()
	folders, _ := store.ListFolders()
	msg, kind := h.flashParts(w, r)
	sb := h.buildSidebar(store, "/apps/feed/settings")
	feedSettingsBulkDeletePage(sb, h.isAdmin(r), subs, folders, msg, kind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

func (h *Pages) SettingsFilters(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filters, _ := store.ListFilters()
	msg, kind := h.flashParts(w, r)
	sb := h.buildSidebar(store, "/apps/feed/settings")
	feedSettingsFiltersPage(sb, h.isAdmin(r), filters, msg, kind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

func (h *Pages) SettingsPlaylists(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	playlists, _ := store.ListPlaylists()
	msg, kind := h.flashParts(w, r)
	sb := h.buildSidebar(store, "/apps/feed/settings")
	feedSettingsPlaylistsPage(sb, h.isAdmin(r), playlists, msg, kind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// BulkDelete deletes the selected folders and subscriptions, then redirects.
func (h *Pages) BulkDelete(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	subIDs := parseIDs(r.Form["sub_id"])
	folderIDs := parseIDs(r.Form["folder_id"])
	var failed bool
	if err := store.DeleteSubscriptions(subIDs); err != nil {
		slog.Error("feed: bulk delete subs", "err", err)
		failed = true
	}
	if err := store.DeleteFolders(folderIDs); err != nil {
		slog.Error("feed: bulk delete folders", "err", err)
		failed = true
	}
	if failed {
		h.setFlash(w, "error", "Some items could not be deleted; see server logs.")
	} else {
		h.setFlash(w, "success", fmt.Sprintf("Deleted %d subscription(s), %d folder(s).", len(subIDs), len(folderIDs)))
	}
	http.Redirect(w, r, "/apps/feed/settings/bulk-delete", http.StatusSeeOther)
}

// parseIDs parses a slice of decimal id strings, skipping invalid entries.
func parseIDs(ss []string) []int64 {
	out := make([]int64, 0, len(ss))
	for _, s := range ss {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// flashParts reads the flash (if any) into message+kind for templates.
func (h *Pages) flashParts(w http.ResponseWriter, r *http.Request) (msg, kind string) {
	if f := h.getFlash(w, r); f != nil {
		return f.Message, f.Kind
	}
	return "", ""
}

// isAdmin reports whether the request's user is an admin.
func (h *Pages) isAdmin(r *http.Request) bool {
	u := plugin.UserFromContext(r.Context())
	return u != nil && u.IsAdmin
}
```

Confirm `plugin.UserFromContext` return type/nil-safety and the `IsAdmin` field (`plugin/context.go:74`). Confirm `h.getFlash` signature matches usage in the old `Settings`.

- [ ] **Step 6: Update routes in `feed.go`**

Replace `ctx.RegisterPage("settings", ...)` and add:

```go
	ctx.RegisterPage("settings", http.HandlerFunc(pages.SettingsRedirect))
	ctx.RegisterPage("settings/subscriptions", http.HandlerFunc(pages.SettingsSubscriptions))
	ctx.RegisterPage("settings/bulk-delete", http.HandlerFunc(pages.SettingsBulkDelete))
	ctx.RegisterPage("settings/filters", http.HandlerFunc(pages.SettingsFilters))
	ctx.RegisterPage("settings/playlists", http.HandlerFunc(pages.SettingsPlaylists))
	ctx.RegisterAction("POST", "settings/bulk-delete", http.HandlerFunc(pages.BulkDelete))
```

- [ ] **Step 7: Point existing PRG redirects at the right tab**

The add/delete handlers for subscriptions, folders, filters, playlists, and OPML/import currently redirect to `/apps/feed/settings`. Update each `http.Redirect(..., "/apps/feed/settings", ...)` to the relevant tab so the user lands where they acted:
- subscriptions, folders, OPML, import → `/apps/feed/settings/subscriptions`
- filters → `/apps/feed/settings/filters`
- playlists → `/apps/feed/settings/playlists`

Find them: `grep -n '"/apps/feed/settings"' plugin/feed/pages.go` and update each. (Add/Delete handlers for filters/playlists already exist; just change their redirect target.)

- [ ] **Step 8: Sidebar active-path match for settings**

In the `feedSidebarClass` helper (pages.templ ~1253), the Settings link uses path `/apps/feed/settings`. Ensure it highlights for any settings sub-path. If `feedSidebarClass` does exact match, change the Settings link comparison to a prefix check. Add a helper:

```go
func feedSettingsActive(activePath string) string {
	if strings.HasPrefix(activePath, "/apps/feed/settings") {
		return "sidebar-link active"
	}
	return "sidebar-link"
}
```

and use `class={ feedSettingsActive(sb.ActivePath) }` on the Settings `<a>` (match the exact class strings `feedSidebarClass` returns — check it first).

- [ ] **Step 9: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test -run 'TestSettings|TestBulkDelete' ./plugin/feed`
Expected: PASS

- [ ] **Step 10: Full package + format + commit**

```bash
go test ./plugin/feed && go tool templ fmt .
git add plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/feed.go plugin/feed/settings_test.go
git commit -m "feed: split settings into four pages with tabs and bulk delete"
```

---

## Task 11: Sidebar on subscription-settings, system-settings, admin-status

**Files:**
- Modify: `plugin/feed/pages.templ` (`feedSubscriptionSettingsPage`, `feedSystemSettingsPage`, `feedAdminStatusPage` gain `sb` + sidebar-layout; system/admin pages also render `feedSettingsTabs` with their non-tab context)
- Modify: `plugin/feed/pages.go` (`SubscriptionSettings`, `SystemSettings`, `AdminStatus` build the sidebar)
- Test: `plugin/feed/pages_test.go` (append)

- [ ] **Step 1: Write the failing test**

```go
func TestAdminPagesHaveSidebar(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "admin", "pass") // first user is admin (see Verified API facts)
	for _, p := range []string{"/apps/feed/admin/status", "/apps/feed/admin/system-settings"} {
		resp := env.Get(t, p, u.Auth)
		resp.Status(200)
		resp.Contains("feed-sidebar")
	}
}
```

The first user created via `env.CreateUser` is an admin, so this single-user test user satisfies any admin gate on the system-settings/status routes. Confirm those routes are actually admin-gated (and 200 for an admin) before asserting.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -run TestAdminPagesHaveSidebar ./plugin/feed`
Expected: FAIL — no `feed-sidebar`.

- [ ] **Step 3: Add `sb` params + wrap the three templates**

For each template, add `sb sidebarData` as the first param and wrap the existing body:

```go
templ feedSystemSettingsPage(sb sidebarData, model string, modelsDir string, cliPath string, flash string, flashKind string) {
	<div class="sidebar-layout">
		@feedSidebar(sb)
		<div class="sidebar-content">
			@feedSettingsTabs("", true)
			// ... existing whisper-settings card body unchanged ...
		</div>
	</div>
}
```

Same wrapping for `feedSubscriptionSettingsPage` (no tabs; it's reached from a subscription) and `feedAdminStatusPage` (`@feedSettingsTabs("", true)` so the user can tab back). For `feedAdminStatusPage`, confirm its existing params and prepend `sb`.

- [ ] **Step 4: Update handlers to build the sidebar**

```go
// In SubscriptionSettings, SystemSettings, AdminStatus, after obtaining store:
sb := h.buildSidebar(store, "/apps/feed/settings")
// ... pass sb as the first arg to the respective template render ...
```

Confirm each handler already has a `store` (some admin handlers may use a different accessor — check `SystemSettings`/`AdminStatus` bodies and reuse their store).

- [ ] **Step 5: Regenerate, build, test**

Run: `go generate ./... && go build ./... && go test -run TestAdminPagesHaveSidebar ./plugin/feed`
Expected: PASS

- [ ] **Step 6: Format + commit**

```bash
go tool templ fmt .
git add plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages_test.go
git commit -m "feed: sidebar on subscription/system/admin pages"
```

---

## Task 12: Full verification + manual acceptance

**Files:** none (verification only)

- [ ] **Step 1: Run the project verifier**

Run: `go tool verify`
Expected: generate/fmt/vet clean, all tests pass with coverage. Fix anything it surfaces.

- [ ] **Step 2: Manual acceptance against the spec**

Run dev server: `go tool dev` (admin:admin), then verify each requirement:
1. Open a media item, click player Next / let it autoplay → URL changes and browser Back returns to the prior item.
2. Sidebar visible on: an item page, all four settings pages, subscription settings, system settings, admin status.
3. Item page shows the context bar with the correct playlist label and working Prev/Next reflecting the originating view (try from All, a folder, a subscription, Starred, History, a playlist).
4. Toggle "Hide read" in the sidebar → folders/subscriptions with zero unread (including any in an error state) disappear; toggling off restores them.
5. Settings tabs switch between Add Subscriptions, Bulk Delete, Filters, Playlists; admin sees System + Status tabs; bulk delete removes selected items.

- [ ] **Step 3: Final commit if any verifier fixes were needed**

```bash
git add -A
git commit -m "feed: verification fixes for pre-merge changes"
```

---

## Self-review notes (coverage map)

- Spec §1 (sidebar everywhere): Tasks 5 (item), 10 (4 settings pages), 11 (sub/system/admin).
- Spec §2 (view context): Tasks 2 (viewCtx), 3 (resolveViewItems), 4 (links).
- Spec §3 (context bar + player nav + history push): Tasks 5 (bar), 6 (endpoints), 7 (player.js pushState/popstate).
- Spec §4 (hide-empty): Tasks 1 (pref), 8 (filter + toggle).
- Spec §5 (admin status discoverable): Task 10 (tabs include Status).
- Spec §6 (settings split + admin tabs): Tasks 9 (bulk store), 10 (pages/tabs/routes), 11 (admin tabs).
- Verification: Task 12.

Open verification points the executor MUST confirm before relying on them (flagged inline): the seed-helper names in existing tests (`seedFeedItems`/`seedTwoMedia`/`pluginDB` — write them if absent), the CSRF mechanism for HTMX posts (Task 8 Step 5), `feedSidebarClass` return strings (Task 10 Step 8), and `plugin.UserFromContext` nil-safety (Task 10 Step 5). Resolved during planning: `UpsertItem`/`ListPlaybackHistory` signatures, `cogtest.Response`/`User` shape, and first-user-is-admin (see "Verified API facts" above).
