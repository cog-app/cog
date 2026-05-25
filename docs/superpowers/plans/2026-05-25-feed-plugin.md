# Feed Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `rss` and `media` plugins with a single unified `feed` plugin that aggregates RSS/Atom (with enclosures), YouTube, and pollable URL subscriptions; runs filters at ingest to tag/skip/download/process; and exposes both a NextCloud News API (text scope) and the cog-wide search/Bleve grammar.

**Architecture:** Greenfield plugin at `plugin/feed/`. Per-user SQLite. One unified `items` table with `type IN (text,audio,video)`. One `subscriptions` table with `kind IN (rss,youtube,url)`. Folders organize subscriptions (flat); tags apply to items. Filters parse the cog-level `internal/query` grammar at save time and evaluate per-item at ingest. The same grammar drives Bleve list views via `SearchSourceDef`. Downloaded media lives on an admin-configured volume, same pattern as today's `plugin/media`.

**Tech Stack:** Go, SQLite, gofeed, bluemonday, yt-dlp, templ, HTMX, Bleve (via cog search manager), `internal/query` (grammar), `plugin/profanity`.

**Spec:** `docs/superpowers/specs/2026-05-25-feed-plugin-design.md`

---

## Conventions used in this plan

- Run all tests with `go test ./...` unless a specific package is called out.
- Run all verifications with `go tool verify`.
- All new files start with a `package` declaration matching the directory name.
- `.templ` files use tabs; the Read tool will display them as spaces, but they're tabs.
- Errors are wrapped+returned or slog'd; never swallowed.
- Per-user SQLite uses the default pool — do **not** set `MaxOpenConns=1` (deadlocks; see CLAUDE.md memory).

---

## Task 1: Make `query.Record` multi-valued

**Why:** The feed plugin needs `tag` (and `is`) to be multi-valued in filter evaluation. The cleanest fix is to make `query.Record` a `map[string][]string` everywhere.

**Files:**
- Modify: `internal/query/ast.go` (Record type and Match methods)
- Modify: `internal/query/query_test.go` (existing tests adapt to slice values)

- [ ] **Step 1: Write new failing tests for multi-value semantics**

Append to `internal/query/query_test.go`:

```go
func TestFieldEqMultiValue(t *testing.T) {
	e := &FieldEqExpr{Field: "tag", Value: "sunday"}
	if !e.Match(Record{"tag": {"weekly", "sunday", "news"}}) {
		t.Error("expected match on second value")
	}
	if e.Match(Record{"tag": {"weekly", "news"}}) {
		t.Error("expected no match")
	}
}

func TestFieldInMultiValue(t *testing.T) {
	e := &FieldInExpr{Field: "is", Values: []string{"unread", "starred"}}
	if !e.Match(Record{"is": {"downloaded", "starred"}}) {
		t.Error("expected match: starred is in record and in expected set")
	}
	if e.Match(Record{"is": {"downloaded"}}) {
		t.Error("expected no match")
	}
}

func TestTextMultiValue(t *testing.T) {
	e := &TextExpr{Value: "hello"}
	if !e.Match(Record{"x": {"nope"}, "y": {"say HELLO world"}}) {
		t.Error("expected match across multi-value fields")
	}
}
```

- [ ] **Step 2: Verify tests fail to compile**

Run: `go test ./internal/query/...`
Expected: build failure — `Record` is `map[string]string`, the tests use `map[string][]string` literals.

- [ ] **Step 3: Change Record type and Match methods**

In `internal/query/ast.go`, replace:

```go
type Record map[string]string
```

with:

```go
type Record map[string][]string
```

Update `TextExpr.Match`:

```go
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
```

Update `PhraseExpr.Match` identically (iterate `vs` then `v`).

Update `FieldEqExpr.Match`:

```go
func (e *FieldEqExpr) Match(r Record) bool {
	for _, v := range r[e.Field] {
		if strings.EqualFold(v, e.Value) {
			return true
		}
	}
	return false
}
```

Update `FieldInExpr.Match`:

```go
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
```

- [ ] **Step 4: Convert existing Record literals in tests**

In `internal/query/query_test.go`, wrap every single-value Record literal in a slice. Example transformation:

```go
// before
Record{"title": "Hello World"}
// after
Record{"title": {"Hello World"}}
```

Apply to all existing tests in the file.

- [ ] **Step 5: Run query tests**

Run: `go test ./internal/query/...`
Expected: PASS.

- [ ] **Step 6: Update callers in `plugin/rss/store.go` and `plugin/media/store.go`**

Both files have a `rec := query.Record{...}` literal followed by a `filterMatches`. The plan deletes these plugins later (Task 24) but we need them to compile in the meantime. Wrap each value in a one-element slice.

Locate `plugin/rss/store.go:509` (the `rec := query.Record{` block). Convert every `"key": value,` line to `"key": {value},`.

Same in `plugin/media/store.go:636`.

- [ ] **Step 7: Run all tests**

Run: `go test ./...`
Expected: PASS (no other call sites build `query.Record`; only `share.Record` exists elsewhere and is a different type).

- [ ] **Step 8: Commit**

```bash
git add internal/query/ plugin/rss/store.go plugin/media/store.go
git commit -m "query: make Record multi-valued (map[string][]string)"
```

---

## Task 2: Create the feed plugin skeleton

**Files:**
- Create: `plugin/feed/feed.go`
- Create: `plugin/feed/sql/schema.sql`
- Create: `plugin/feed/feed_test.go`

- [ ] **Step 1: Write a test that registers and opens the plugin**

Create `plugin/feed/feed_test.go`:

```go
package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestRegister(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "u", "pass")
	// Plugin should be in the registry.
	if env.Providers.Plugins.Get("feed") == nil {
		t.Fatal("feed plugin not registered")
	}
}
```

- [ ] **Step 2: Verify test fails (no feed package)**

Run: `go test ./plugin/feed/...`
Expected: FAIL — no such package.

- [ ] **Step 3: Write the minimal plugin entry**

Create `plugin/feed/feed.go`:

```go
// Package feed implements a unified content aggregation plugin for RSS
// (including podcasts via enclosures), YouTube, and pollable URLs.
package feed

import (
	"context"
	"embed"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "feed"

type Plugin struct {
	plugin.Protected
}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Init(ctx context.Context, c plugin.Context) error {
	if err := c.MigrateUserDB(sqlFS); err != nil {
		return err
	}
	return nil
}
```

Note: real method signatures must match `plugin.Plugin` interface — verify against `internal/plugin/plugin.go` and `plugin/rss/rss.go` for the exact shape (Init/Routes/Widgets/SearchSourceDef). Port the boilerplate from `plugin/rss/rss.go` lines 1–80 verbatim, swapping name and removing rss-specific bits.

- [ ] **Step 4: Create empty schema file**

Create `plugin/feed/sql/schema.sql`:

```sql
-- Schema is added in Task 3.
```

- [ ] **Step 5: Run the registration test**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/
git commit -m "feed: plugin skeleton + registration"
```

---

## Task 3: Add the database schema

**Files:**
- Modify: `plugin/feed/sql/schema.sql`
- Create: `plugin/feed/schema_test.go`

- [ ] **Step 1: Write a test that opens the DB and verifies tables**

Create `plugin/feed/schema_test.go`:

```go
package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSchema(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	db := env.Providers.Plugins.UserDB(u.User.ID, feed.PluginID)
	if db == nil {
		t.Fatal("no user db")
	}

	want := []string{
		"folders", "subscriptions", "items",
		"tags", "item_tags",
		"filters", "downloads", "playback", "playlists",
	}
	for _, name := range want {
		var got string
		row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name)
		if err := row.Scan(&got); err != nil {
			t.Errorf("table %s missing: %v", name, err)
		}
	}
}
```

(`Providers.Plugins.UserDB` is an example accessor; replace with the actual cogtest helper used by `plugin/rss/rss_test.go` for opening the user's plugin DB — port the pattern.)

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/...`
Expected: FAIL — no tables.

- [ ] **Step 3: Write the schema**

Replace `plugin/feed/sql/schema.sql` with the full schema from the spec (`docs/superpowers/specs/2026-05-25-feed-plugin-design.md` § Data model). Copy verbatim, including all indexes.

In the `downloads` table, **also** add these two columns (they cover the `mute_profanity` and `transcribe` filter actions; Task 12 details why):

```sql
needs_profanity INTEGER NOT NULL DEFAULT 0,
needs_transcribe INTEGER NOT NULL DEFAULT 0,
```

- [ ] **Step 4: Run the schema test**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/sql/schema.sql plugin/feed/schema_test.go
git commit -m "feed: schema (folders, subscriptions, items, tags, filters, downloads, playback, playlists)"
```

---

## Task 4: Store layer — folders + subscriptions

**Files:**
- Create: `plugin/feed/store.go`
- Create: `plugin/feed/store_test.go`

- [ ] **Step 1: Write failing tests for folders and subscriptions CRUD**

Create `plugin/feed/store_test.go`:

```go
package feed_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func newStore(t *testing.T) *feed.Store {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	db := env.Providers.Plugins.UserDB(u.User.ID, feed.PluginID)
	return feed.NewStore(db)
}

func TestFoldersCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateFolder("Development")
	require.NoError(t, err)
	if id == 0 {
		t.Fatal("zero id")
	}
	fs, err := s.ListFolders()
	require.NoError(t, err)
	if len(fs) != 1 || fs[0].Name != "Development" {
		t.Fatalf("got %+v", fs)
	}
	require.NoError(t, s.RenameFolder(id, "Dev"))
	require.NoError(t, s.DeleteFolder(id))
}

func TestSubscriptionsCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateSubscription(feed.Subscription{
		Kind:            "rss",
		URL:             "https://example.com/feed.xml",
		Title:           "Example",
		PollIntervalSec: 1800,
		NextCheckAt:     time.Now().Unix(),
	})
	require.NoError(t, err)
	sub, err := s.GetSubscription(id)
	require.NoError(t, err)
	if sub.URL != "https://example.com/feed.xml" {
		t.Fatalf("got %+v", sub)
	}
	require.NoError(t, s.PauseSubscription(id, true))
	subs, err := s.ListDueSubscriptions(time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	if len(subs) != 0 {
		t.Fatal("paused subscriptions should not be due")
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./plugin/feed/...`
Expected: FAIL — `Store`/`Subscription` undefined.

- [ ] **Step 3: Implement Store, folders, subscriptions**

Create `plugin/feed/store.go`. Port the structure from `plugin/rss/store.go` (it's the closest match). Replace the `feeds` table with `subscriptions` (column names per spec). Concrete shape:

```go
package feed

import (
	"database/sql"
	"fmt"
	"time"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

type Folder struct {
	ID        int64
	Name      string
	CreatedAt int64
}

type Subscription struct {
	ID              int64
	FolderID        sql.NullInt64
	Kind            string // rss | youtube | url
	URL             string
	Title           string
	Description     string
	SiteURL         string
	ImageURL        string
	FaviconURL      string
	ETag            string
	LastModified    string
	PollIntervalSec int64
	NextCheckAt     int64
	LastCheckedAt   int64
	LastSuccessAt   int64
	ErrorCount      int64
	LastError       string
	Paused          bool
	CreatedAt       int64
}

func (s *Store) CreateFolder(name string) (int64, error) { /* INSERT */ }
func (s *Store) ListFolders() ([]Folder, error)          { /* SELECT ORDER BY name */ }
func (s *Store) RenameFolder(id int64, name string) error
func (s *Store) DeleteFolder(id int64) error

func (s *Store) CreateSubscription(sub Subscription) (int64, error)
func (s *Store) GetSubscription(id int64) (Subscription, error)
func (s *Store) ListSubscriptions() ([]Subscription, error)
func (s *Store) ListSubscriptionsByFolder(folderID sql.NullInt64) ([]Subscription, error)
func (s *Store) UpdateSubscriptionPollState(id int64, etag, lastMod string, nextCheckAt, lastCheckedAt, lastSuccessAt int64) error
func (s *Store) RecordSubscriptionError(id int64, errMsg string, nextCheckAt int64) error
func (s *Store) PauseSubscription(id int64, paused bool) error
func (s *Store) DeleteSubscription(id int64) error
func (s *Store) ListDueSubscriptions(now int64) ([]Subscription, error) // WHERE next_check_at <= ? AND paused = 0
```

Fill in each method following the pattern in `plugin/rss/store.go`. Use prepared queries inline (the rss plugin does this).

- [ ] **Step 4: Run store tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: store layer for folders and subscriptions"
```

---

## Task 5: Store layer — items + tags

**Files:**
- Modify: `plugin/feed/store.go`
- Modify: `plugin/feed/store_test.go`

- [ ] **Step 1: Append failing tests**

Append to `plugin/feed/store_test.go`:

```go
func TestItemsAndTags(t *testing.T) {
	s := newStore(t)
	subID, err := s.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://x/feed.xml", PollIntervalSec: 1800,
	})
	require.NoError(t, err)

	// Insert returns (id, inserted bool).
	id, inserted, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "guid-1",
		Type:           "text",
		Title:          "Hello",
		ContentHTML:    "<p>body</p>",
		PublishedAt:    1000,
	})
	require.NoError(t, err)
	if !inserted || id == 0 {
		t.Fatalf("expected insert, got id=%d inserted=%v", id, inserted)
	}

	// Re-insert with same guid -> no-op.
	_, inserted2, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "guid-1", Type: "text", Title: "dup",
	})
	require.NoError(t, err)
	if inserted2 {
		t.Fatal("expected dedup")
	}

	// Tags.
	require.NoError(t, s.AddTag(id, "sunday"))
	require.NoError(t, s.AddTag(id, "weekly"))
	tags, err := s.ItemTags(id)
	require.NoError(t, err)
	if len(tags) != 2 {
		t.Fatalf("got tags %v", tags)
	}

	// Mark read.
	require.NoError(t, s.MarkRead(id, true))
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./plugin/feed/...`
Expected: FAIL — `UpsertItem`/`AddTag`/`ItemTags`/`MarkRead` undefined.

- [ ] **Step 3: Implement Item type and methods**

Append to `plugin/feed/store.go`:

```go
type Item struct {
	ID             int64
	SubscriptionID int64
	GUID           string
	Type           string // text | audio | video
	Title          string
	Author         string
	URL            string
	ContentHTML    string
	MediaURL       string
	MediaMIME      string
	DurationSec    int64
	ThumbnailURL   string
	PublishedAt    int64
	FetchedAt      int64
	IsRead         bool
	IsStarred      bool
	ContentHash    string
}

// UpsertItem inserts a new item, or returns (existing id, false) on dedup.
func (s *Store) UpsertItem(it Item) (id int64, inserted bool, err error)
func (s *Store) GetItem(id int64) (Item, error)
func (s *Store) DeleteItem(id int64) error
func (s *Store) MarkRead(id int64, read bool) error
func (s *Store) MarkStarred(id int64, starred bool) error

func (s *Store) AddTag(itemID int64, tag string) error      // creates tag if missing
func (s *Store) RemoveTag(itemID int64, tag string) error
func (s *Store) ItemTags(itemID int64) ([]string, error)
```

Implementation notes:
- `UpsertItem` uses `INSERT OR IGNORE` on `(subscription_id, guid)`. If `RowsAffected()==0`, fetch the existing id and return `inserted=false`.
- `AddTag` inserts the tag row with `INSERT OR IGNORE`, then inserts into `item_tags` with `INSERT OR IGNORE`.

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: store layer for items and tags"
```

---

## Task 6: Store layer — filters, downloads, playback, playlists

**Files:**
- Modify: `plugin/feed/store.go`
- Modify: `plugin/feed/store_test.go`

- [ ] **Step 1: Append failing tests**

```go
func TestFiltersCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateFilter(feed.Filter{
		SortOrder:   1,
		Query:       "kind:rss AND tag:morning",
		Action:      "download",
		ActionValue: "",
	})
	require.NoError(t, err)
	fs, err := s.ListFilters()
	require.NoError(t, err)
	if len(fs) != 1 || fs[0].ID != id {
		t.Fatalf("got %+v", fs)
	}
}

func TestDownloadsAndPlayback(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u1"})
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, err)

	require.NoError(t, s.EnqueueDownload(id))
	dls, err := s.PendingDownloads(10)
	require.NoError(t, err)
	if len(dls) != 1 {
		t.Fatalf("got %d", len(dls))
	}
	require.NoError(t, s.MarkDownloadReady(id, "/path/file.mp3", 12345))

	require.NoError(t, s.SetPlayback(id, 42.5, false))
}

func TestPlaylistsCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreatePlaylist("Morning", "type:audio AND tag:morning")
	require.NoError(t, err)
	pls, err := s.ListPlaylists()
	require.NoError(t, err)
	if len(pls) != 1 || pls[0].ID != id {
		t.Fatalf("got %+v", pls)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./plugin/feed/...`
Expected: FAIL — types undefined.

- [ ] **Step 3: Implement types and methods**

Append to `plugin/feed/store.go`:

```go
type Filter struct {
	ID          int64
	SortOrder   int64
	Query       string
	Action      string // skip | tag | download | mute_profanity | transcribe
	ActionValue string
	CreatedAt   int64
}

type Download struct {
	ItemID         int64
	State          string // queued | downloading | ready | error
	LocalPath      string
	CleanAudioPath string
	CaptionsPath   string
	Bytes          int64
	Error          string
	StartedAt      int64
	CompletedAt    int64
}

type Playback struct {
	ItemID       int64
	ProgressSec  float64
	Completed    bool
	LastPlayedAt int64
}

type Playlist struct {
	ID        int64
	Name      string
	Query     string
	SortOrder int64
	CreatedAt int64
}

func (s *Store) CreateFilter(f Filter) (int64, error)
func (s *Store) ListFilters() ([]Filter, error)
func (s *Store) UpdateFilter(f Filter) error
func (s *Store) DeleteFilter(id int64) error

func (s *Store) EnqueueDownload(itemID int64) error      // INSERT OR IGNORE state=queued
func (s *Store) PendingDownloads(limit int) ([]Download, error)
func (s *Store) MarkDownloadStart(itemID int64) error
func (s *Store) MarkDownloadReady(itemID int64, localPath string, bytes int64) error
func (s *Store) MarkDownloadError(itemID int64, errMsg string) error
func (s *Store) SetCleanAudioPath(itemID int64, path string) error
func (s *Store) SetCaptionsPath(itemID int64, path string) error
func (s *Store) GetDownload(itemID int64) (Download, error)

func (s *Store) SetPlayback(itemID int64, progressSec float64, completed bool) error
func (s *Store) GetPlayback(itemID int64) (Playback, error)

func (s *Store) CreatePlaylist(name, query string) (int64, error)
func (s *Store) ListPlaylists() ([]Playlist, error)
func (s *Store) UpdatePlaylist(p Playlist) error
func (s *Store) DeletePlaylist(id int64) error
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: store layer for filters, downloads, playback, playlists"
```

---

## Task 7: Fetcher interface + RSS fetcher

**Files:**
- Create: `plugin/feed/fetcher/fetcher.go`
- Create: `plugin/feed/fetcher/rss.go`
- Create: `plugin/feed/fetcher/rss_test.go`
- Create: `plugin/feed/fetcher/testdata/sample-text.xml`
- Create: `plugin/feed/fetcher/testdata/sample-enclosure.xml`

- [ ] **Step 1: Write failing test**

Create `plugin/feed/fetcher/rss_test.go`:

```go
package fetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

func TestRSSTextOnly(t *testing.T) {
	body, err := os.ReadFile("testdata/sample-text.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write(body)
	}))
	defer srv.Close()

	sub := feed.Subscription{Kind: "rss", URL: srv.URL}
	out, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out.Items) != 2 {
		t.Fatalf("want 2 text items, got %d", len(out.Items))
	}
	for _, it := range out.Items {
		if it.Type != "text" {
			t.Errorf("want type=text, got %s", it.Type)
		}
	}
}

func TestRSSWithAudioEnclosure(t *testing.T) {
	body, err := os.ReadFile("testdata/sample-enclosure.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{Kind: "rss", URL: srv.URL})
	require.NoError(t, err)

	var types []string
	for _, it := range out.Items {
		types = append(types, it.Type)
	}
	if len(types) != 2 || types[0] != "text" || types[1] != "audio" {
		t.Fatalf("want [text audio], got %v", types)
	}
}
```

- [ ] **Step 2: Create test fixtures**

`plugin/feed/fetcher/testdata/sample-text.xml` — minimal RSS 2.0 with two `<item>` elements, no enclosures, each with `<title>`, `<link>`, `<guid>`, `<description>`, `<pubDate>`.

`plugin/feed/fetcher/testdata/sample-enclosure.xml` — RSS 2.0 with one `<item>` that has both a `<description>` and an `<enclosure url="..." type="audio/mpeg" length="123"/>`.

Use existing rss testdata fixtures as a template: `plugin/rss/testdata/*.xml`.

- [ ] **Step 3: Verify tests fail**

Run: `go test ./plugin/feed/fetcher/...`
Expected: FAIL — fetcher package undefined.

- [ ] **Step 4: Implement the fetcher interface and RSS dispatch**

Create `plugin/feed/fetcher/fetcher.go`:

```go
package fetcher

import (
	"context"
	"fmt"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Result is what a Fetch call returns for a subscription poll.
type Result struct {
	Items        []feed.Item
	ETag         string
	LastModified string
	// SubscriptionUpdates contains optional metadata to write back to the
	// subscription row (title, description, site_url, image_url, favicon_url).
	SubscriptionUpdates map[string]string
	NotModified         bool
}

// Fetch dispatches by subscription kind.
func Fetch(ctx context.Context, sub feed.Subscription) (Result, error) {
	switch sub.Kind {
	case "rss":
		return fetchRSS(ctx, sub)
	case "youtube":
		return fetchYouTube(ctx, sub)
	case "url":
		return fetchURL(ctx, sub)
	default:
		return Result{}, fmt.Errorf("unknown subscription kind: %q", sub.Kind)
	}
}
```

Create `plugin/feed/fetcher/rss.go` — port from `plugin/rss/fetcher.go`. Key differences:

- Emit `feed.Item` (not the old rss item type).
- For each parsed gofeed entry: if entry has body text, emit one `feed.Item{Type: "text", ContentHTML: sanitized, ...}`. For each `enclosure`: emit one `feed.Item{Type: "audio"|"video", MediaURL: encURL, MediaMIME: encMIME, ...}`, with `GUID = entry.GUID + "#" + sha256(encURL)[:12]`.
- Sniff `audio` vs `video` from the enclosure mime: prefix `audio/` → `audio`; prefix `video/` → `video`; default `audio`.
- HTML sanitized with `bluemonday.UGCPolicy` (matches rss plugin today).
- Respect `If-None-Match` (etag) and `If-Modified-Since` (last_modified) headers; on `304`, set `Result.NotModified = true`.
- Return discovered metadata (title, description, site URL, favicon, image) in `SubscriptionUpdates`.

Stub `fetchYouTube` and `fetchURL` in the same file (or empty siblings) returning `Result{}, fmt.Errorf("not implemented yet")` — Tasks 8 and 9 implement them.

- [ ] **Step 5: Run tests**

Run: `go test ./plugin/feed/fetcher/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/fetcher/
git commit -m "feed: RSS fetcher (text items + per-enclosure media items)"
```

---

## Task 8: YouTube fetcher

**Files:**
- Modify: `plugin/feed/fetcher/fetcher.go` (replace stub)
- Create: `plugin/feed/fetcher/youtube.go`
- Create: `plugin/feed/fetcher/youtube_test.go`
- Create: `plugin/feed/fetcher/testdata/ytdlp-stub.sh`

- [ ] **Step 1: Write failing test**

```go
package fetcher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

func TestYouTubeFetch(t *testing.T) {
	abs, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{
		Kind: "youtube", URL: "https://youtube.com/@example",
	})
	require.NoError(t, err)
	if len(out.Items) == 0 {
		t.Fatal("no items")
	}
	if out.Items[0].Type != "video" {
		t.Fatalf("want video, got %s", out.Items[0].Type)
	}
}
```

- [ ] **Step 2: Create the yt-dlp stub binary**

Create `plugin/feed/fetcher/testdata/ytdlp-stub.sh` (chmod +x in the test or commit with mode 755). The stub prints a JSON document matching the yt-dlp `--dump-single-json` shape for a channel with 2 entries. Refer to `plugin/media/ytdlp.go` for the exact JSON shape consumed by the existing media plugin and reuse the same struct types.

In the test setup, symlink/copy `ytdlp-stub.sh` to a file named `yt-dlp` on PATH:

```go
// In test:
require.NoError(t, os.Symlink("ytdlp-stub.sh", filepath.Join(abs, "yt-dlp")))
```

(Existing media plugin tests do this — port the pattern.)

- [ ] **Step 3: Verify test fails**

Run: `go test ./plugin/feed/fetcher/...`
Expected: FAIL — `fetchYouTube` stub.

- [ ] **Step 4: Implement YouTube fetcher**

Create `plugin/feed/fetcher/youtube.go`. Port from `plugin/media/fetch_youtube.go` and `plugin/media/ytdlp.go`. Key differences:

- Output `feed.Item{Type: "video", MediaURL: entry.WebpageURL, Title: entry.Title, ThumbnailURL: entry.Thumbnail, DurationSec: int64(entry.Duration), PublishedAt: parsedUploadDate, GUID: entry.ID}`.
- No download here — metadata only.
- Use `exec.CommandContext` so context cancellation propagates.

Remove the stub from `fetcher.go` and have it call this implementation.

- [ ] **Step 5: Run tests**

Run: `go test ./plugin/feed/fetcher/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/fetcher/
git commit -m "feed: YouTube fetcher via yt-dlp"
```

---

## Task 9: URL fetcher with body-hash change detection

**Files:**
- Modify: `plugin/feed/fetcher/fetcher.go` (remove stub)
- Create: `plugin/feed/fetcher/url.go`
- Create: `plugin/feed/fetcher/url_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestURLFetchEmitsItemOnFirstAndChange(t *testing.T) {
	var body = "<html><body>v1</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	sub := feed.Subscription{Kind: "url", URL: srv.URL}

	// First fetch: emit item.
	out1, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out1.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(out1.Items))
	}
	hash1 := out1.Items[0].ContentHash
	if hash1 == "" {
		t.Fatal("expected content_hash")
	}

	// Second fetch (caller supplies last hash via ETag-like mechanism).
	// We model "what the caller knows" via Subscription.ETag = previous hash.
	sub.ETag = hash1
	out2, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out2.Items) != 0 {
		t.Fatal("expected no items on unchanged poll")
	}

	// Change body.
	body = "<html><body>v2</body></html>"
	out3, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out3.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(out3.Items))
	}
	if out3.Items[0].ContentHash == hash1 {
		t.Fatal("expected new hash")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/fetcher/...`
Expected: FAIL — `fetchURL` stub.

- [ ] **Step 3: Implement URL fetcher**

Create `plugin/feed/fetcher/url.go`:

```go
package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func fetchURL(ctx context.Context, sub feed.Subscription) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("url fetch %s: %s", sub.URL, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	// We reuse the subscription's ETag column to persist the body hash; if
	// the body hasn't changed since last fetch, emit no items.
	if sub.ETag == hash {
		return Result{ETag: hash, NotModified: true}, nil
	}

	u, _ := url.Parse(sub.URL)
	title := sub.Title
	if title == "" {
		title = path.Base(u.Path)
		if title == "" || title == "/" {
			title = u.Host
		}
	}

	item := feed.Item{
		Type:        "text",
		Title:       title,
		URL:         sub.URL,
		ContentHTML: string(body), // sanitized below if HTML
		PublishedAt: time.Now().Unix(),
		ContentHash: hash,
		GUID:        hash, // unique per change
	}
	// Sanitize if response is HTML.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		item.ContentHTML = bluemonday.UGCPolicy().Sanitize(string(body))
	} else {
		item.Type = "text"
		item.ContentHTML = "<pre>" + html.EscapeString(string(body)) + "</pre>"
	}

	return Result{Items: []feed.Item{item}, ETag: hash}, nil
}
```

Add imports as needed (`fmt`, `html`, `strings`, `github.com/microcosm-cc/bluemonday`).

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/fetcher/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/fetcher/
git commit -m "feed: URL fetcher with body-hash change detection"
```

---

## Task 10: Poller

**Files:**
- Create: `plugin/feed/poller.go`
- Create: `plugin/feed/poller_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestPollerRunsDueSubscriptions(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	db := env.Providers.Plugins.UserDB(u.User.ID, feed.PluginID)
	store := feed.NewStore(db)

	srv := newTextRSSServer(t) // helper that serves sample-text.xml
	defer srv.Close()

	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, err)

	p := feed.NewPoller(store)
	require.NoError(t, p.RunOnce(context.Background()))

	// Items should now exist.
	items, err := store.ListItemsBySubscription(subID, 100)
	require.NoError(t, err)
	if len(items) == 0 {
		t.Fatal("no items after poll")
	}
}
```

(Add `ListItemsBySubscription(subID int64, limit int) ([]Item, error)` to the store as part of this task.)

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/...`
Expected: FAIL.

- [ ] **Step 3: Implement poller**

Create `plugin/feed/poller.go`. Port the structure from `plugin/media/poller.go` (90 lines). Differences:

- Calls `fetcher.Fetch(ctx, sub)`.
- For each returned item: `store.UpsertItem(item)`; on `inserted`, hand the item to the filter engine (Task 12). For Task 10 only, no filter engine yet — leave a comment `// TODO(Task 12): run filter engine on newly inserted items`.
- On `Result.NotModified`, just update `NextCheckAt` and return.
- On success, write back any `SubscriptionUpdates` (title/description/etc.) via `store.UpdateSubscriptionMetadata`. Add that method.
- On error, call `store.RecordSubscriptionError` with exponential backoff: `min(poll_interval * 2^error_count, 86400)`.
- `RunOnce(ctx)` processes all due subscriptions and returns; long-lived `Run(ctx)` loops with a ticker.

Add the missing store method:

```go
func (s *Store) UpdateSubscriptionMetadata(id int64, updates map[string]string) error
func (s *Store) ListItemsBySubscription(subID int64, limit int) ([]Item, error)
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/
git commit -m "feed: poller dispatches fetcher per subscription"
```

---

## Task 11: Item Record builder

**Files:**
- Create: `plugin/feed/record.go`
- Create: `plugin/feed/record_test.go`

- [ ] **Step 1: Write failing test**

```go
package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestBuildRecord(t *testing.T) {
	it := feed.Item{
		Title: "Hello", Author: "Ada", ContentHTML: "body", Type: "audio",
		URL: "https://x/", PublishedAt: 1700000000,
	}
	sub := feed.Subscription{Kind: "rss", Title: "Daily", URL: "https://x/feed"}
	rec := feed.BuildRecord(it, sub, "Development", []string{"sunday", "weekly"}, true, false, true)

	cases := map[string][]string{
		"title":        {"Hello"},
		"author":       {"Ada"},
		"content":      {"body"},
		"type":         {"audio"},
		"kind":         {"rss"},
		"folder":       {"Development"},
		"subscription": {"Daily"},
		"tag":          {"sunday", "weekly"},
		"is":           {"unread", "downloaded"},
	}
	for k, want := range cases {
		got := rec[k]
		if len(got) != len(want) {
			t.Fatalf("%s: got %v want %v", k, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s[%d]: got %q want %q", k, i, got[i], want[i])
			}
		}
	}

	// Verify it matches a real query.
	expr, err := query.Parse("type:audio AND folder:Development AND tag:sunday")
	if err != nil {
		t.Fatal(err)
	}
	if !expr.Match(rec) {
		t.Fatal("expected match")
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/...`
Expected: FAIL — `BuildRecord` undefined.

- [ ] **Step 3: Implement BuildRecord**

Create `plugin/feed/record.go`:

```go
package feed

import (
	"time"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// BuildRecord constructs the cog-grammar Record used by the filter engine
// and by cog-level Bleve indexing.
//
// Booleans:
//   unread     - is the item currently unread
//   starred    - is the item starred
//   downloaded - does the item have a ready download row
func BuildRecord(it Item, sub Subscription, folderName string, tags []string, unread, starred, downloaded bool) query.Record {
	rec := query.Record{
		"title":        {it.Title},
		"author":       {it.Author},
		"content":      {it.ContentHTML},
		"type":         {it.Type},
		"kind":         {sub.Kind},
		"subscription": {sub.Title},
		"url":          {it.URL},
		"published":    {time.Unix(it.PublishedAt, 0).UTC().Format(time.RFC3339)},
	}
	if folderName != "" {
		rec["folder"] = []string{folderName}
	}
	if len(tags) > 0 {
		rec["tag"] = append([]string(nil), tags...)
	}
	var is []string
	if unread {
		is = append(is, "unread")
	}
	if starred {
		is = append(is, "starred")
	}
	if downloaded {
		is = append(is, "downloaded")
	}
	if len(is) > 0 {
		rec["is"] = is
	}
	return rec
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/record.go plugin/feed/record_test.go
git commit -m "feed: BuildRecord constructs query.Record for filter and search"
```

---

## Task 12: Filter engine

**Files:**
- Create: `plugin/feed/filter.go`
- Create: `plugin/feed/filter_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestFilterEngineActions(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	itemID, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g1", Type: "audio",
		Title: "Morning Brew", Author: "x", ContentHTML: "",
	})
	require.NoError(t, err)

	_, err = s.CreateFilter(feed.Filter{
		SortOrder: 1, Query: "type:audio", Action: "tag", ActionValue: "podcast",
	})
	require.NoError(t, err)
	_, err = s.CreateFilter(feed.Filter{
		SortOrder: 2, Query: "tag:podcast", Action: "download",
	})
	require.NoError(t, err)

	eng, err := feed.NewFilterEngine(s)
	require.NoError(t, err)

	require.NoError(t, eng.Apply(itemID))

	tags, err := s.ItemTags(itemID)
	require.NoError(t, err)
	if len(tags) != 1 || tags[0] != "podcast" {
		t.Fatalf("got tags %v", tags)
	}
	dl, err := s.GetDownload(itemID)
	require.NoError(t, err)
	if dl.State != "queued" {
		t.Fatalf("got download state %q", dl.State)
	}
}

func TestFilterSkipDeletesItem(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "text", Title: "spam"})
	_, _ = s.CreateFilter(feed.Filter{SortOrder: 1, Query: "title:spam", Action: "skip"})

	eng, _ := feed.NewFilterEngine(s)
	require.NoError(t, eng.Apply(id))

	_, err := s.GetItem(id)
	if err == nil {
		t.Fatal("expected item deleted")
	}
}

func TestFilterParseErrorAtSave(t *testing.T) {
	if _, err := feed.ParseFilterQuery("(((unbalanced"); err == nil {
		t.Fatal("expected parse error")
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./plugin/feed/...`
Expected: FAIL — engine undefined.

- [ ] **Step 3: Implement filter engine**

Create `plugin/feed/filter.go`:

```go
package feed

import (
	"fmt"
	"log/slog"

	"git.duckfam.us/jj-software/cog/internal/query"
)

type FilterEngine struct {
	store *Store
}

func NewFilterEngine(s *Store) (*FilterEngine, error) {
	return &FilterEngine{store: s}, nil
}

// ParseFilterQuery validates a filter query string at save time.
func ParseFilterQuery(q string) (query.Expr, error) {
	return query.Parse(q)
}

// Apply runs all filters in sort_order against the given item.
// All matching filters apply (not first-match), with mutations from earlier
// filters visible to later ones (tags added in pass N are part of the Record
// for pass N+1).
func (e *FilterEngine) Apply(itemID int64) error {
	it, err := e.store.GetItem(itemID)
	if err != nil {
		return fmt.Errorf("filter apply: load item: %w", err)
	}
	sub, err := e.store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return fmt.Errorf("filter apply: load subscription: %w", err)
	}
	folder := ""
	if sub.FolderID.Valid {
		f, err := e.store.GetFolder(sub.FolderID.Int64) // add this method in store
		if err == nil {
			folder = f.Name
		}
	}

	filters, err := e.store.ListFilters()
	if err != nil {
		return err
	}

	deleted := false
	for _, f := range filters {
		if deleted {
			break
		}
		expr, err := query.Parse(f.Query)
		if err != nil {
			slog.Warn("feed: filter parse error at apply (skipping)", "filter_id", f.ID, "err", err)
			continue
		}
		tags, _ := e.store.ItemTags(itemID)
		dl, _ := e.store.GetDownload(itemID)
		rec := BuildRecord(it, sub, folder, tags, !it.IsRead, it.IsStarred, dl.State == "ready")
		if !expr.Match(rec) {
			continue
		}
		switch f.Action {
		case "skip":
			if err := e.store.DeleteItem(itemID); err != nil {
				return fmt.Errorf("filter skip: %w", err)
			}
			deleted = true
		case "tag":
			if f.ActionValue == "" {
				slog.Warn("feed: filter tag action with empty value", "filter_id", f.ID)
				continue
			}
			if err := e.store.AddTag(itemID, f.ActionValue); err != nil {
				return fmt.Errorf("filter tag: %w", err)
			}
		case "download":
			if it.Type == "text" {
				continue // nothing to download
			}
			if err := e.store.EnqueueDownload(itemID); err != nil {
				return fmt.Errorf("filter download: %w", err)
			}
		case "mute_profanity":
			if err := e.store.EnqueueProfanity(itemID); err != nil {
				return fmt.Errorf("filter mute_profanity: %w", err)
			}
		case "transcribe":
			if err := e.store.EnqueueTranscribe(itemID); err != nil {
				return fmt.Errorf("filter transcribe: %w", err)
			}
		}
	}
	return nil
}
```

Add to `plugin/feed/store.go`:

```go
func (s *Store) GetFolder(id int64) (Folder, error)
func (s *Store) EnqueueProfanity(itemID int64) error      // upsert downloads + pending_profanity flag
func (s *Store) EnqueueTranscribe(itemID int64) error
```

Decision: rather than add new tables for "pending processing", reuse `downloads` and add two columns to the existing schema via migration. Or — simpler — model pending profanity/transcribe as: clean_audio_path/captions_path = empty + a `needs_profanity`/`needs_transcribe` flag inferred from filter actions. For v1, the simplest model is to add two columns to the `downloads` table via migration:

```sql
ALTER TABLE downloads ADD COLUMN needs_profanity INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN needs_transcribe INTEGER NOT NULL DEFAULT 0;
```

Put this in `plugin/feed/sql/migration001.sql` and update store methods accordingly. `EnqueueProfanity` does an upsert: `INSERT OR IGNORE INTO downloads(item_id, state) VALUES(?, 'queued'); UPDATE downloads SET needs_profanity=1 WHERE item_id=?;`. Same for transcribe.

Actually since this is greenfield, fold these columns into `schema.sql` directly — no migration needed yet. Update the spec's Data model wording to mention these columns in the design doc as a postscript, and add to `schema.sql`.

- [ ] **Step 4: Update schema.sql with the two columns**

In `plugin/feed/sql/schema.sql`, in the `downloads` table:

```sql
needs_profanity INTEGER NOT NULL DEFAULT 0,
needs_transcribe INTEGER NOT NULL DEFAULT 0,
```

- [ ] **Step 5: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/
git commit -m "feed: filter engine evaluates per-item, dispatches actions"
```

---

## Task 13: Wire filter engine into poller

**Files:**
- Modify: `plugin/feed/poller.go`
- Create: `plugin/feed/poller_filter_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestPollerRunsFilters(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	db := env.Providers.Plugins.UserDB(u.User.ID, feed.PluginID)
	store := feed.NewStore(db)

	srv := newTextRSSServer(t)
	defer srv.Close()

	subID, _ := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	_, _ = store.CreateFilter(feed.Filter{SortOrder: 1, Query: "type:text", Action: "tag", ActionValue: "news"})

	p := feed.NewPoller(store)
	require.NoError(t, p.RunOnce(context.Background()))

	items, _ := store.ListItemsBySubscription(subID, 100)
	require.NotEqual(t, 0, len(items))
	tags, _ := store.ItemTags(items[0].ID)
	if len(tags) == 0 || tags[0] != "news" {
		t.Fatalf("got tags %v", tags)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/...`
Expected: FAIL — poller does not invoke engine yet.

- [ ] **Step 3: Invoke engine in poller**

In `plugin/feed/poller.go`, after a successful `UpsertItem` returns `inserted=true`, call:

```go
if err := p.engine.Apply(id); err != nil {
	slog.Error("feed: filter apply failed", "item_id", id, "err", err)
}
```

Update `NewPoller` to construct an internal `FilterEngine` from the same store, or accept it as a parameter:

```go
func NewPoller(store *Store) *Poller {
	eng, _ := NewFilterEngine(store)
	return &Poller{store: store, engine: eng}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/poller.go plugin/feed/poller_filter_test.go
git commit -m "feed: poller runs filter engine on newly inserted items"
```

---

## Task 14: Download worker

**Files:**
- Create: `plugin/feed/pipeline/download.go`
- Create: `plugin/feed/pipeline/download_test.go`

- [ ] **Step 1: Write failing test**

```go
package pipeline_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

func TestDownloadWorker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("AAAAAAAA"))
	}))
	defer srv.Close()

	s := newTestStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio", MediaURL: srv.URL})
	require.NoError(t, s.EnqueueDownload(id))

	dir := t.TempDir()
	dl := pipeline.NewDownloader(s, dir)
	require.NoError(t, dl.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q", d.State)
	}
	b, err := os.ReadFile(d.LocalPath)
	require.NoError(t, err)
	if string(b) != "AAAAAAAA" {
		t.Fatalf("got %q", string(b))
	}
	if filepath.Dir(d.LocalPath) != dir {
		t.Errorf("path not under root: %s", d.LocalPath)
	}
}
```

(`newTestStore(t)` is a small helper that opens a fresh store via cogtest; add it inside `pipeline_test`.)

- [ ] **Step 2: Verify test fails**

Run: `go test ./plugin/feed/pipeline/...`
Expected: FAIL — package missing.

- [ ] **Step 3: Implement downloader**

Create `plugin/feed/pipeline/download.go`:

```go
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

type Downloader struct {
	store *feed.Store
	root  string // base directory for downloaded media
}

func NewDownloader(s *feed.Store, root string) *Downloader {
	return &Downloader{store: s, root: root}
}

// RunOnce processes all currently pending downloads sequentially.
func (d *Downloader) RunOnce(ctx context.Context) error {
	queued, err := d.store.PendingDownloads(100)
	if err != nil {
		return err
	}
	for _, q := range queued {
		if err := d.processOne(ctx, q.ItemID); err != nil {
			slog.Error("feed: download failed", "item_id", q.ItemID, "err", err)
		}
	}
	return nil
}

func (d *Downloader) processOne(ctx context.Context, itemID int64) error {
	if err := d.store.MarkDownloadStart(itemID); err != nil {
		return err
	}
	it, err := d.store.GetItem(itemID)
	if err != nil {
		return d.fail(itemID, err)
	}
	sub, err := d.store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return d.fail(itemID, err)
	}

	dst := filepath.Join(d.root, strconv.FormatInt(itemID, 10)+extForMIME(it.MediaMIME, it.Type))
	if err := os.MkdirAll(d.root, 0o755); err != nil {
		return d.fail(itemID, err)
	}

	switch sub.Kind {
	case "youtube":
		// yt-dlp -o <dst-template> <url>
		cmd := exec.CommandContext(ctx, "yt-dlp", "-o", dst, it.MediaURL)
		if out, err := cmd.CombinedOutput(); err != nil {
			return d.fail(itemID, fmt.Errorf("yt-dlp: %w: %s", err, string(out)))
		}
	default:
		// HTTP GET.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, it.MediaURL, nil)
		if err != nil {
			return d.fail(itemID, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return d.fail(itemID, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return d.fail(itemID, errors.New(resp.Status))
		}
		f, err := os.Create(dst)
		if err != nil {
			return d.fail(itemID, err)
		}
		n, err := io.Copy(f, resp.Body)
		closeErr := f.Close()
		if err != nil {
			return d.fail(itemID, err)
		}
		if closeErr != nil {
			return d.fail(itemID, closeErr)
		}
		_ = n
	}

	info, err := os.Stat(dst)
	if err != nil {
		return d.fail(itemID, err)
	}
	return d.store.MarkDownloadReady(itemID, dst, info.Size())
}

func (d *Downloader) fail(itemID int64, err error) error {
	if dbErr := d.store.MarkDownloadError(itemID, err.Error()); dbErr != nil {
		return fmt.Errorf("download error %v (and recording failed: %w)", err, dbErr)
	}
	return err
}

func extForMIME(mime, typ string) string {
	switch mime {
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	if typ == "video" {
		return ".bin.video"
	}
	return ".bin"
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/pipeline/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/pipeline/
git commit -m "feed: download worker pulls queued media via HTTP or yt-dlp"
```

---

## Task 15: Profanity worker

**Files:**
- Create: `plugin/feed/pipeline/profanity.go`
- Create: `plugin/feed/pipeline/profanity_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestProfanityWorker(t *testing.T) {
	// Prepare a download in 'ready' state with needs_profanity=1 and a real file path.
	// The profanity worker should read local_path, produce clean_audio_path,
	// and update the row.
	s := newTestStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))

	src := filepath.Join(t.TempDir(), "in.wav")
	require.NoError(t, os.WriteFile(src, []byte("dummy"), 0o644))
	require.NoError(t, s.MarkDownloadReady(id, src, 5))
	require.NoError(t, s.EnqueueProfanity(id))

	w := pipeline.NewProfanity(s, t.TempDir())
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.CleanAudioPath == "" {
		t.Fatal("expected clean_audio_path")
	}
	if _, err := os.Stat(d.CleanAudioPath); err != nil {
		t.Fatalf("clean file missing: %v", err)
	}
}
```

(For v1, the worker can be a thin wrapper that calls into the existing `plugin/profanity` package. If a real ML run is too heavy for unit tests, allow injecting a `Processor` interface and use a stub in tests that copies the input to the output.)

- [ ] **Step 2: Implement profanity worker**

Create `plugin/feed/pipeline/profanity.go`. Define a `Processor` interface with a single `Process(ctx, in, out string) error` method. Default constructor uses `plugin/profanity`. Tests inject a stub.

- [ ] **Step 3: Run tests**

Run: `go test ./plugin/feed/pipeline/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add plugin/feed/pipeline/
git commit -m "feed: profanity-mute worker"
```

---

## Task 16: Transcribe stub

**Files:**
- Create: `plugin/feed/pipeline/transcribe.go`
- Create: `plugin/feed/pipeline/transcribe_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestTranscribeStubLogsAndClears(t *testing.T) {
	// Stub worker should consume needs_transcribe flag without producing captions.
	s := newTestStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueTranscribe(id))

	w := pipeline.NewTranscribe(s)
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	// Stub clears the needs_transcribe flag but writes no captions.
	if d.CaptionsPath != "" {
		t.Errorf("captions path should remain empty in stub: %q", d.CaptionsPath)
	}
}
```

Add `func (s *Store) ClearNeedsTranscribe(itemID int64) error` and the symmetric `ClearNeedsProfanity` in the store.

- [ ] **Step 2: Implement stub**

```go
package pipeline

import (
	"context"
	"log/slog"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

type Transcribe struct {
	store *feed.Store
}

func NewTranscribe(s *feed.Store) *Transcribe { return &Transcribe{store: s} }

func (w *Transcribe) RunOnce(ctx context.Context) error {
	pending, err := w.store.PendingTranscribe(100) // add to store
	if err != nil {
		return err
	}
	for _, itemID := range pending {
		slog.Info("feed: transcribe stub", "item_id", itemID)
		if err := w.store.ClearNeedsTranscribe(itemID); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./plugin/feed/pipeline/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add plugin/feed/pipeline/
git commit -m "feed: transcribe pipeline stub (no-op in v1)"
```

---

## Task 17: Search-source registration

**Files:**
- Modify: `plugin/feed/feed.go`
- Create: `plugin/feed/search.go`
- Create: `plugin/feed/search_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestSearchSourceRegistered(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	// Insert an item, then perform a cog-level search and verify it appears.
	db := env.Providers.Plugins.UserDB(u.User.ID, feed.PluginID)
	s := feed.NewStore(db)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u", Title: "Daily"})
	_, _, _ = s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "text", Title: "FindMe",
	})

	// Trigger a reindex of feed items (call whatever cogtest exposes; same as
	// rss tests do today).
	env.ReindexSearch(t, u.User.ID)

	hits, err := env.Providers.Search.Query(context.Background(), u.User.ID, "FindMe")
	require.NoError(t, err)
	if len(hits) == 0 {
		t.Fatal("expected feed item in search results")
	}
}
```

(Use whatever the rss plugin's existing search test does as the exact template; adapt the API name if `Search.Query` differs.)

- [ ] **Step 2: Implement search source**

Create `plugin/feed/search.go`. Mirror the structure of `plugin/rss/rss.go:64` (the `RegisterSearchSource` call) and any helper Enrich function in `plugin/media/media.go:84`. Expose:

```go
package feed

import (
	"context"
	"database/sql"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/search"
)

func (p *Plugin) searchSource() plugin.SearchSourceDef {
	return plugin.SearchSourceDef{
		PrimaryTable: "items",
		Enrich: func(db *sql.DB, rowIDs []int64) ([]search.Document, error) {
			// SELECT items.*, subscriptions.title, subscriptions.kind, folders.name, group_concat(tags.name)
			// FROM items
			// JOIN subscriptions ...
			// LEFT JOIN folders ...
			// LEFT JOIN item_tags ... LEFT JOIN tags ...
			// WHERE items.id IN (?...)
			// GROUP BY items.id;
			//
			// Return a Document per item with fields:
			//   id, title, content, author, type, kind, folder, subscription, tag(multi), is(multi), published.
			return nil, nil
		},
	}
}
```

Register it in `Init`:

```go
ctx.RegisterSearchSource(p.searchSource())
```

- [ ] **Step 3: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add plugin/feed/
git commit -m "feed: register items as a cog-level search source"
```

---

## Task 18: NextCloud News API surface

**Files:**
- Create: `plugin/feed/ncapi/ncapi.go`
- Create: `plugin/feed/ncapi/ncapi_test.go`

- [ ] **Step 1: Write failing tests**

Port the test set from `plugin/rss/rss_test.go` covering NC News endpoints: list folders, create folder, list feeds (subscriptions where kind=rss), list items (type=text), mark items read/starred. Same fixture URLs, same JSON shapes.

- [ ] **Step 2: Implement NC News handler**

Create `plugin/feed/ncapi/ncapi.go`. Port from `plugin/rss/newsapi.go` (451 lines). Adjustments:

- Source of items: query feed plugin's items table with `WHERE type='text'`.
- Source of feeds: `subscriptions` with `WHERE kind='rss'`. JSON keys stay `"feeds"` for compatibility.
- Folder shape unchanged.
- All response JSON shapes match what the rss plugin produces today — diff the rss tests' golden responses against new tests and adjust until identical.

- [ ] **Step 3: Wire into Plugin.Routes**

In `plugin/feed/feed.go`, when constructing routes for `Init`, add the NC News routes under `/index.php/apps/news/api/v1-2/*` and `/ocs/v2.php/apps/news/api/...` if used. Mirror `plugin/rss/rss.go`.

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/ncapi/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/ncapi/
git commit -m "feed: NextCloud News API (scoped to text items, kind=rss subscriptions)"
```

---

## Task 19: Web UI — settings page

**Files:**
- Create: `plugin/feed/pages.go`
- Create: `plugin/feed/pages.templ`
- Create: `plugin/feed/widget.templ`
- Create: `plugin/feed/pages_test.go`
- Run: `go tool templ fmt .` after editing templ files
- Run: `go generate ./plugin/feed/` to regen `pages_templ.go`/`widget_templ.go`

- [ ] **Step 1: Write failing browser test (settings forms)**

Create `plugin/feed/pages_test.go` modeled after `plugin/rss/browser_test.go`:

```go
package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "u", "pass")

	t.Run("Settings", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "u", "pass")
		b.Navigate(t, "/apps/feed/settings")
		b.WaitVisible(t, "#feed-settings")
		b.FillForm(t, "add-subscription", map[string]string{
			"kind": "rss",
			"url":  "https://example.com/feed.xml",
		})
		b.Submit(t, "add-subscription")
		b.WaitText(t, "https://example.com/feed.xml")
	})
}
```

- [ ] **Step 2: Implement settings templ + handler**

Create `plugin/feed/pages.templ` with components:

```templ
templ SettingsPage(subs []feed.Subscription, folders []feed.Folder, filters []feed.Filter, playlists []feed.Playlist) {
    <div id="feed-settings">
        <section>
            <h2>Subscriptions</h2>
            // List + add form (kind dropdown, url input)
        </section>
        <section>
            <h2>Folders</h2>
        </section>
        <section>
            <h2>Filters</h2>
            // List + add form (query, action, action_value)
        </section>
        <section>
            <h2>Playlists</h2>
            // List + add form (name, query)
        </section>
    </div>
}
```

Create `plugin/feed/pages.go` with handler methods:

- `GET /apps/feed/settings` — render SettingsPage.
- `POST /apps/feed/subscriptions` — create. PRG → redirect.
- `POST /apps/feed/subscriptions/{id}/delete` — delete. PRG.
- `POST /apps/feed/folders` — create. PRG.
- `POST /apps/feed/filters` — parse query (`ParseFilterQuery`), validate, save. PRG with flash on error.
- `POST /apps/feed/playlists` — parse query, save. PRG.

All POST handlers follow the PRG rule in CLAUDE.md (set flash, 303 redirect).

Register routes from `Init`.

- [ ] **Step 3: Run `go tool templ fmt .` and `go generate`**

```bash
go tool templ fmt .
go generate ./plugin/feed/
```

- [ ] **Step 4: Run tests**

Run: `go test ./plugin/feed/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/pages.templ plugin/feed/pages.go plugin/feed/pages_templ.go plugin/feed/pages_test.go
git commit -m "feed: settings page (subscriptions, folders, filters, playlists)"
```

---

## Task 20: Web UI — sidebar + item list + detail

**Files:**
- Modify: `plugin/feed/pages.templ`
- Modify: `plugin/feed/pages.go`
- Modify: `plugin/feed/pages_test.go`

- [ ] **Step 1: Append failing browser test**

```go
t.Run("ItemList", func(t *testing.T) {
    // Seed a subscription and one item via store, then verify the list page
    // shows it. Click into detail.
})

t.Run("SearchBoxFiltersList", func(t *testing.T) {
    // Type "type:audio" into the search box, verify only audio items shown.
})
```

- [ ] **Step 2: Implement sidebar + list templ**

Add to `pages.templ`:

```templ
templ IndexPage(folders []feed.Folder, tags []string, playlists []feed.Playlist, items []feed.Item, query string) {
    <div id="feed-app">
        <aside id="feed-sidebar">
            <section>
                <h3>Folders</h3>
                for _, f := range folders {
                    <a href={ templ.URL("/apps/feed/?q=folder:" + f.Name) }>{ f.Name }</a>
                }
            </section>
            <section>
                <h3>Tags</h3>
                for _, t := range tags {
                    <a href={ templ.URL("/apps/feed/?q=tag:" + t) }>{ t }</a>
                }
            </section>
            <section>
                <h3>Playlists</h3>
                for _, p := range playlists {
                    <a href={ templ.URL("/apps/feed/?q=" + p.Query) }>{ p.Name }</a>
                }
            </section>
        </aside>
        <main id="feed-main">
            <form action="/apps/feed/" method="get">
                <input type="search" name="q" value={ query }/>
            </form>
            <ul id="feed-items">
                for _, it := range items {
                    @ItemRow(it)
                }
            </ul>
        </main>
    </div>
}

templ ItemRow(it feed.Item) {
    <li class={ "feed-item", "type-" + it.Type }>
        <a href={ templ.URL("/apps/feed/items/" + strconv.FormatInt(it.ID, 10)) }>{ it.Title }</a>
    </li>
}

templ ItemDetail(it feed.Item, tags []string, dl feed.Download) {
    <article>
        <h1>{ it.Title }</h1>
        if it.Type == "text" {
            @templ.Raw(it.ContentHTML)
        } else if it.Type == "audio" {
            <audio controls src={ mediaSource(it, dl) }></audio>
        } else if it.Type == "video" {
            <video controls src={ mediaSource(it, dl) }></video>
        }
    </article>
}
```

Add handler methods:

- `GET /apps/feed/` — render IndexPage. Read `q` query param; pass to `search.Manager.Query` if non-empty, otherwise default to all items sorted by `published_at DESC` with a sensible limit (e.g. 100).
- `GET /apps/feed/items/{id}` — render ItemDetail. Mark read.
- `POST /apps/feed/items/{id}/read` — toggle read. PRG.
- `POST /apps/feed/items/{id}/star` — toggle starred. PRG.
- `GET /apps/feed/items/{id}/media` — serve the bytes (signed URL); use `clean_audio_path` if present, then `local_path`, then 302 to `media_url`.

`mediaSource` is a helper that returns the URL for the `/media` endpoint.

- [ ] **Step 3: Regen + run tests**

```bash
go tool templ fmt .
go generate ./plugin/feed/
go test ./plugin/feed/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add plugin/feed/
git commit -m "feed: index page (sidebar, item list, detail, media stream)"
```

---

## Task 21: Dashboard widget

**Files:**
- Create: `plugin/feed/widget.templ`
- Modify: `plugin/feed/feed.go` (register widget)
- Modify: `plugin/feed/pages_test.go`

- [ ] **Step 1: Implement widget templ**

```templ
templ Widget(unread int, recent []feed.Item) {
    <div class="widget feed-widget">
        <h3>Feed</h3>
        <p>{ strconv.Itoa(unread) } unread</p>
        <ul>
            for _, it := range recent {
                <li><a href={ templ.URL("/apps/feed/items/" + strconv.FormatInt(it.ID, 10)) }>{ it.Title }</a></li>
            }
        </ul>
    </div>
}
```

Register widget in `Init` (mirror `plugin/rss/rss.go` / `plugin/media/media.go` widget registration).

- [ ] **Step 2: Run + commit**

```bash
go tool templ fmt .
go generate ./plugin/feed/
go test ./plugin/feed/...

git add plugin/feed/
git commit -m "feed: dashboard widget"
```

---

## Task 22: Switch service registration to feed

**Files:**
- Modify: `cmd/cog/main.go` (and/or `internal/services/services.go` — whichever currently registers the rss and media plugins)

- [ ] **Step 1: Find registration call sites**

Run: `grep -rn "rss.New\|media.New" cmd/ internal/`
Note each file.

- [ ] **Step 2: Replace with feed.New**

In each file, replace the `rss.New()` and `media.New()` arguments with `feed.New()`. Remove duplicates.

- [ ] **Step 3: Verify the dev server builds and runs**

```bash
go tool dev -reset
```

Expected: server starts, log line confirms feed plugin registered.

- [ ] **Step 4: Run full test suite + verify**

```bash
go tool verify
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ internal/services/
git commit -m "feed: replace rss and media in service graph"
```

---

## Task 23: Delete old rss and media plugins

**Files:**
- Delete: `plugin/rss/` (recursively)
- Delete: `plugin/media/` (recursively)
- Remove all imports of those packages anywhere they remain.

- [ ] **Step 1: Verify no remaining imports**

```bash
grep -rn '"git.duckfam.us/jj-software/cog/plugin/rss\|"git.duckfam.us/jj-software/cog/plugin/media' .
```

Expected: only matches inside `plugin/rss/` and `plugin/media/` themselves.

- [ ] **Step 2: Delete the directories**

```bash
rm -rf plugin/rss plugin/media
```

- [ ] **Step 3: Run full verify**

```bash
go tool verify
```

Expected: PASS. If anything else imports rss/media, fix it (delete the import + replace functionality with feed equivalents).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feed: remove obsolete rss and media plugins"
```

---

## Task 24: Final verification

- [ ] **Step 1: Full verify**

```bash
go tool verify
```

Expected: PASS, coverage steady.

- [ ] **Step 2: Manually exercise the dev server**

```bash
go tool dev -reset
```

Open `http://localhost:8580/apps/feed/`. Add an RSS subscription, an RSS-with-enclosure subscription, a YouTube channel, and a pollable URL. Add a folder, assign subscriptions to it. Add a filter `type:audio` → `tag:podcast`, and another `tag:podcast` → `download`. Wait for the poller. Verify text items appear in the NC News API at `/index.php/apps/news/api/v1-2/items` and that audio items are downloaded under the media volume.

- [ ] **Step 3: Commit any final fixes**

```bash
git add -A
git commit -m "feed: post-cutover fixes from manual verification"
```

---

## Self-review checklist (for the planner)

- Spec sections covered:
  - Architecture/packages: Tasks 2, 7, 12, 14, 17, 18, 19, 20, 21.
  - Schema: Tasks 3, 12 (downloads columns).
  - Ingest dispatch + per-kind fetchers: Tasks 7, 8, 9.
  - Poller + filter integration: Tasks 10, 13.
  - Filter actions skip/tag/download/mute_profanity/transcribe: Tasks 12, 14, 15, 16.
  - Search grammar (multi-value Record + per-item match + Bleve registration): Tasks 1, 11, 12, 17.
  - NC News API: Task 18.
  - Cutover (registration switch + delete olds): Tasks 22, 23.
- Method signatures consistent across tasks (Store methods declared in earlier tasks reused later without rename).
- No placeholders / TBDs in code blocks. Where a task says "port from existing X with these changes," the changes are enumerated.

---

## Notes for the executor

- Use `superpowers:test-driven-development` per task.
- Run `go tool verify` after every task that modifies more than one package.
- If a `Store` method signature drifts from this plan (e.g., you find a cleaner shape during implementation), update the plan inline before moving on.
- The web UI tasks (19, 20, 21) are the riskiest. Lean on `plugin/rss/pages.templ` and `plugin/media/pages.templ` as references during implementation — they're getting deleted, but until Task 23 they're a live source of patterns.
