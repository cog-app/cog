# Interactive Media Player Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the feed plugin's minimal media-player stub with a full custom YouTube-style player: draggable scrubber, keyboard + touch controls, playback speed, skip-silence, auto-advance, MediaSession (device OS controls), and per-user preferences.

**Architecture:** Vanilla JS, no build pipeline. Player behavior lives in one embedded static asset (`plugin/feed/static/player.js`) served via a plugin route; the `feedPlayer` templ renders only markup + a `data-*` config blob. Profanity-mute and silence-skip converge on one `Range{Start,End,Action}` model exposed by a single `api/ranges/{id}` endpoint; one client loop applies them. Silence ranges come from `ffmpeg silencedetect`, cached on the `downloads` row. Per-user prefs use the existing typed `Setting[T]` / `feed_settings` infrastructure.

**Tech Stack:** Go (net/http, database/sql, SQLite), templ, HTMX, vanilla JS, ffmpeg. Tests via `internal/cogtest` + `internal/testing/{assert,require}`.

---

## Reference: existing code this plan builds on

- `plugin/feed/store.go`
  - `Setting[T]` (`:1369`), `StringSetting`/`IntSetting`/`BoolSetting` (`:1415`–`:1443`) — **no FloatSetting yet** (Task 1 adds it).
  - `Download` struct (`:525`), `GetDownload` (`:708`).
  - `ItemPlayback` struct (`:905`), `GetItemPlayback` (`:918`).
  - `NextUnplayedMedia(excludeID)` (`:1542`).
  - `Item` struct (`:341`) has `ThumbnailURL`; subscription has `ImageURL`, `FaviconURL`.
- `plugin/feed/pages.go`
  - `Pages` struct (`:26`), `NewPages` (`:31`), `userStore(r)` (`:66`).
  - `CleanAudio` handler (`:717`) — returns `{"ranges":[...]}` from profanity captions. **Replaced by Task 4.**
  - `UpdateProgress` (`:639`), `MarkComplete` (`:662`, already renders next player).
  - `ItemMedia` (serves local/clean/remote media).
- `plugin/feed/pages.templ`
  - `feedPlayer(ip ItemPlayback, csrfToken string)` (`:738`) + `feedPlayerScript()` (`:797`) — **rewritten in Tasks 8–9.**
  - Called from `feedItemDetail` (`pages.templ:728`) and `MarkComplete` (`pages.go:684`).
- `plugin/feed/feed.go`
  - `Register` mounts routes via `ctx.RegisterPage/RegisterAPI/RegisterAction` (`:96`–`:147`).
  - `ctx.RegisterAPI("api/clean-audio/{id}", ...)` (`:110`) — **renamed in Task 4.**
- `plugin/profanity` — `ParseVTT`, `(*Plugin).AudioRanges(username, captions) []plugin.AudioRange`; `plugin.AudioRange{Start,End float64}`.
- Route mounting semantics (`internal/plugin/registry.go`):
  - `RegisterAPI(path, h)` → `GET /apps/feed/<path>`, auth-required, no CSRF.
  - `RegisterAction(method, path, h)` → `<method> /apps/feed/<path>`, auth + CSRF.
  - `RegisterPage(path, h)` → `GET /apps/feed/<path>`, wrapped in HTML shell.

**Commands:**
- Generate templ: `go tool templ generate ./plugin/feed/`
- Test package: `go test ./plugin/feed/...`
- Full verify: `go tool verify`
- Update browser snapshot: `WEBTEST_UPDATE=1 go test ./cmd/cog/...`

**Note on JS testing:** the repo has no JS test runner. `player.js` is verified by `go build`, the existing browser snapshot test (`TestBrowserMin`), and a manual checklist (Task 12). Keep pure helpers (`rangeAt`, `formatTime`) small; they are exercised indirectly. Do not invent a JS test harness.

---

## Task 1: FloatSetting helper + player preference definitions

**Files:**
- Modify: `plugin/feed/store.go` (add `FloatSetting` near `:1433`; add pref `Setting` vars + a `PlayerPrefs` struct + `LoadPlayerPrefs`/`SavePlayerPrefs` near the other settings)
- Test: `plugin/feed/store_test.go` (create if absent, else append)

- [ ] **Step 1: Write the failing test**

In `plugin/feed/store_test.go`:

```go
func TestPlayerPrefsRoundTrip(t *testing.T) {
	db := newTestDB(t) // see note below
	s := NewStore(db)

	// Defaults when unset.
	got, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	assert.Equal(t, 1.0, got.Speed)
	assert.Equal(t, 15, got.SkipBackSec)
	assert.Equal(t, 30, got.SkipFwdSec)
	assert.Equal(t, 1.0, got.Volume)
	assert.Equal(t, true, got.AutoplayNext)
	assert.Equal(t, false, got.SkipSilence)

	// Persist and read back.
	want := PlayerPrefs{Speed: 1.5, SkipBackSec: 10, SkipFwdSec: 45, Volume: 0.8, AutoplayNext: false, SkipSilence: true}
	require.NoError(t, s.SavePlayerPrefs(want))
	got, err = s.LoadPlayerPrefs()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
```

Note: check the top of `store_test.go` / other `_test.go` in the package for the existing helper that opens a migrated in-memory feed DB. If one exists (e.g. `newTestDB(t)` or a `cogtest`-based helper), use it. If none exists, add:

```go
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, plugin.Migrate(db, sqlFS))
	return db
}
```
(Imports: `database/sql`, `git.duckfam.us/jj-software/cog/internal/plugin`, the sqlite driver already used by the package, and the require/assert packages.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestPlayerPrefsRoundTrip`
Expected: FAIL — `undefined: PlayerPrefs` / `LoadPlayerPrefs`.

- [ ] **Step 3: Implement**

In `plugin/feed/store.go`, after `BoolSetting` (`:1443`):

```go
// FloatSetting creates a Setting for a float64 value.
func FloatSetting(key string, defaultValue float64) Setting[float64] {
	return Setting[float64]{
		Key: key, DefaultValue: defaultValue,
		Parse:     func(s string) (float64, error) { return strconv.ParseFloat(s, 64) },
		Stringify: func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
	}
}

// Per-user player preferences (stored in feed_settings).
var (
	settingPlaybackSpeed = FloatSetting("playback_speed", 1.0)
	settingSkipBackSec   = IntSetting("skip_back_sec", 15)
	settingSkipFwdSec    = IntSetting("skip_fwd_sec", 30)
	settingVolume        = FloatSetting("volume", 1.0)
	settingAutoplayNext  = BoolSetting("autoplay_next", true)
	settingSkipSilence   = BoolSetting("skip_silence", false)
)

// PlayerPrefs is the per-user set of media-player preferences.
type PlayerPrefs struct {
	Speed        float64 `json:"speed"`
	SkipBackSec  int     `json:"skipBackSec"`
	SkipFwdSec   int     `json:"skipFwdSec"`
	Volume       float64 `json:"volume"`
	AutoplayNext bool    `json:"autoplayNext"`
	SkipSilence  bool    `json:"skipSilence"`
}

// LoadPlayerPrefs reads the per-user player preferences, falling back to defaults.
func (s *Store) LoadPlayerPrefs() (PlayerPrefs, error) {
	return PlayerPrefs{
		Speed:        settingPlaybackSpeed.MustGet(s.db),
		SkipBackSec:  settingSkipBackSec.MustGet(s.db),
		SkipFwdSec:   settingSkipFwdSec.MustGet(s.db),
		Volume:       settingVolume.MustGet(s.db),
		AutoplayNext: settingAutoplayNext.MustGet(s.db),
		SkipSilence:  settingSkipSilence.MustGet(s.db),
	}, nil
}

// SavePlayerPrefs persists the per-user player preferences.
func (s *Store) SavePlayerPrefs(p PlayerPrefs) error {
	if err := settingPlaybackSpeed.Set(s.db, p.Speed); err != nil {
		return fmt.Errorf("save playback_speed: %w", err)
	}
	if err := settingSkipBackSec.Set(s.db, p.SkipBackSec); err != nil {
		return fmt.Errorf("save skip_back_sec: %w", err)
	}
	if err := settingSkipFwdSec.Set(s.db, p.SkipFwdSec); err != nil {
		return fmt.Errorf("save skip_fwd_sec: %w", err)
	}
	if err := settingVolume.Set(s.db, p.Volume); err != nil {
		return fmt.Errorf("save volume: %w", err)
	}
	if err := settingAutoplayNext.Set(s.db, p.AutoplayNext); err != nil {
		return fmt.Errorf("save autoplay_next: %w", err)
	}
	if err := settingSkipSilence.Set(s.db, p.SkipSilence); err != nil {
		return fmt.Errorf("save skip_silence: %w", err)
	}
	return nil
}
```

`LoadPlayerPrefs` returns an error in its signature (never non-nil today) so the API can grow validation later without a signature change.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestPlayerPrefsRoundTrip`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/store_test.go
git commit -m "feed: FloatSetting + per-user player preferences"
```

---

## Task 2: Silence-range storage on the downloads row

**Files:**
- Modify: `plugin/feed/sql/schema.sql` (add columns to `downloads`)
- Modify: `plugin/feed/store.go` (`Download` struct + `GetDownload` scan + new `SetSilenceRanges`/`GetSilenceRanges`; define `Range` type)
- Test: `plugin/feed/store_test.go`

Silence ranges are cached as JSON on `downloads`. A separate `silence_state` column distinguishes "not computed" from "computed, found none" so empty results are not recomputed forever.

- [ ] **Step 1: Write the failing test**

```go
func TestSilenceRangesRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)

	// Need an item + download row (FK). Insert minimally.
	subID, err := s.AddSubscription(Subscription{URL: "http://x", Kind: "rss", Title: "X"})
	require.NoError(t, err)
	itemID, err := s.UpsertItem(Item{SubscriptionID: subID, GUID: "g1", Type: "audio", Title: "Ep1", MediaURL: "http://x/1.mp3"})
	require.NoError(t, err)
	require.NoError(t, s.UpsertDownload(Download{ItemID: itemID, State: "ready", LocalPath: "/tmp/1.mp3"}))

	// Before computing: state is "unknown", no ranges.
	d, err := s.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "", d.SilenceState) // unknown == empty default

	// Save ranges + mark done.
	want := []Range{{Start: 1.0, End: 2.5, Action: "skip"}, {Start: 10, End: 11, Action: "skip"}}
	require.NoError(t, s.SetSilenceRanges(itemID, want))

	d, err = s.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "done", d.SilenceState)

	got, err := s.GetSilenceRanges(itemID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
```

> Before writing, confirm the exact signatures of `AddSubscription`, `UpsertItem`, `UpsertDownload` in `store.go` and adjust the struct literals to match required fields. If `UpsertDownload` does not exist, use the method the download pipeline uses to create rows (grep `INSERT INTO downloads`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestSilenceRangesRoundTrip`
Expected: FAIL — `d.SilenceState undefined` / `s.SetSilenceRanges undefined`.

- [ ] **Step 3: Implement**

In `plugin/feed/sql/schema.sql`, append after the `downloads` table block (`:13`). SQLite has no `ADD COLUMN IF NOT EXISTS`; the migration runner applies each schema file once, but to stay consistent with the file's idempotent `IF NOT EXISTS` style and survive re-runs on existing DBs, add the columns to the `CREATE TABLE` **and** guard with the migration. Simplest robust approach: add the columns directly in the `CREATE TABLE IF NOT EXISTS downloads(...)` definition (greenfield — no deployed DBs):

```sql
    needs_profanity INTEGER NOT NULL DEFAULT 0,
    needs_transcribe INTEGER NOT NULL DEFAULT 0,
    silence_ranges TEXT NOT NULL DEFAULT '',
    silence_state TEXT NOT NULL DEFAULT ''
);
```

> Verify how `plugin.Migrate` applies `sqlFS` (grep `func Migrate` in `internal/plugin`). The feed schema uses `IF NOT EXISTS` and is greenfield, so editing the `CREATE TABLE` is sufficient. If `Migrate` tracks applied files by hash/version and would skip the changed file on an existing dev DB, the implementer should `go tool dev -reset` to recreate. Do NOT add an out-of-band ALTER unless `Migrate` requires versioned migration files.

In `plugin/feed/store.go`, add to the `Download` struct (after `NeedsTranscribe`, `:536`):

```go
	NeedsProfanity  bool
	NeedsTranscribe bool
	SilenceRanges   string // JSON array of Range (action="skip"); empty when unknown
	SilenceState    string // "" = unknown, "done" = computed (may be empty result)
```

Add the `Range` type near the other public types (e.g. after `Download`):

```go
// Range is a time span within a media item with an associated player action.
// Action is "mute" (drop volume during the span) or "skip" (seek past it).
type Range struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Action string  `json:"action"`
}
```

Update `GetDownload` (`:708`) to select and scan the two new columns. Find the `SELECT ... FROM downloads` and `.Scan(...)` and add `silence_ranges, silence_state` and `&d.SilenceRanges, &d.SilenceState` in matching positions.

Add methods (near `GetDownload`):

```go
// GetSilenceRanges decodes the cached silence ranges for an item.
func (s *Store) GetSilenceRanges(itemID int64) ([]Range, error) {
	d, err := s.GetDownload(itemID)
	if err != nil {
		return nil, err
	}
	if d.SilenceRanges == "" {
		return nil, nil
	}
	var rs []Range
	if err := json.Unmarshal([]byte(d.SilenceRanges), &rs); err != nil {
		return nil, fmt.Errorf("decode silence ranges for item %d: %w", itemID, err)
	}
	return rs, nil
}

// SetSilenceRanges caches silence ranges for an item and marks detection done.
func (s *Store) SetSilenceRanges(itemID int64, ranges []Range) error {
	buf, err := json.Marshal(ranges)
	if err != nil {
		return fmt.Errorf("encode silence ranges: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE downloads SET silence_ranges = ?, silence_state = 'done' WHERE item_id = ?`,
		string(buf), itemID)
	if err != nil {
		return fmt.Errorf("save silence ranges for item %d: %w", itemID, err)
	}
	return nil
}
```

Add `"encoding/json"` to imports if not present.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestSilenceRangesRoundTrip`
Expected: PASS (if the dev DB is stale, the test uses a fresh in-memory DB so it is unaffected).

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/sql/schema.sql plugin/feed/store_test.go
git commit -m "feed: cache silence ranges + state on downloads row"
```

---

## Task 3: Silence detection (ffmpeg silencedetect) + parser

**Files:**
- Create: `plugin/feed/silence.go`
- Test: `plugin/feed/silence_test.go`

Parsing is the testable unit. The ffmpeg invocation is a thin wrapper.

- [ ] **Step 1: Write the failing test**

`plugin/feed/silence_test.go`:

```go
package feed

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

func TestParseSilenceDetect(t *testing.T) {
	// Representative ffmpeg -af silencedetect stderr.
	stderr := `
[silencedetect @ 0x55] silence_start: 1.234
[silencedetect @ 0x55] silence_end: 2.567 | silence_duration: 1.333
size=N/A time=00:00:05.00 bitrate=N/A
[silencedetect @ 0x55] silence_start: 10.0
[silencedetect @ 0x55] silence_end: 11.5 | silence_duration: 1.5
`
	got := parseSilenceDetect(stderr)
	want := []Range{
		{Start: 1.234, End: 2.567, Action: "skip"},
		{Start: 10.0, End: 11.5, Action: "skip"},
	}
	assert.Equal(t, want, got)
}

func TestParseSilenceDetect_DanglingStart(t *testing.T) {
	// A silence_start with no matching end (e.g. silence runs to EOF) is dropped.
	stderr := "[silencedetect @ 0x1] silence_start: 4.0\n"
	got := parseSilenceDetect(stderr)
	assert.Equal(t, 0, len(got))
}

func TestParseSilenceDetect_Empty(t *testing.T) {
	assert.Equal(t, 0, len(parseSilenceDetect("")))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestParseSilenceDetect`
Expected: FAIL — `undefined: parseSilenceDetect`.

- [ ] **Step 3: Implement**

`plugin/feed/silence.go`:

```go
package feed

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// silenceNoiseDB and silenceMinDur control silencedetect sensitivity.
// -30dB for ~0.5s is a reasonable podcast default (trims gaps, not normal pauses).
const (
	silenceNoiseDB  = "-30dB"
	silenceMinDur   = "0.5"
)

// detectSilence runs ffmpeg silencedetect on a local media file and returns
// the detected silent spans as skip ranges. Returns (nil, nil) when no silence
// is found. ffmpeg writes silencedetect output to stderr.
func detectSilence(ctx context.Context, mediaPath string) ([]Range, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-hide_banner", "-nostats",
		"-i", mediaPath,
		"-af", "silencedetect=noise="+silenceNoiseDB+":d="+silenceMinDur,
		"-f", "null", "-",
	)
	// ffmpeg returns non-zero only on real errors; silencedetect output is on stderr.
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg silencedetect %q: %w: %s", mediaPath, err, stderr.String())
	}
	return parseSilenceDetect(stderr.String()), nil
}

// parseSilenceDetect extracts silence spans from ffmpeg silencedetect stderr.
// Each "silence_start" must be paired with a following "silence_end"; an
// unpaired trailing start (silence to EOF) is dropped.
func parseSilenceDetect(stderr string) []Range {
	var out []Range
	var haveStart bool
	var start float64
	sc := bufio.NewScanner(strings.NewReader(stderr))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "silence_start:"); i >= 0 {
			field := strings.TrimSpace(line[i+len("silence_start:"):])
			if v, err := strconv.ParseFloat(strings.Fields(field)[0], 64); err == nil {
				start, haveStart = v, true
			}
			continue
		}
		if i := strings.Index(line, "silence_end:"); i >= 0 && haveStart {
			field := strings.TrimSpace(line[i+len("silence_end:"):])
			// "silence_end: 2.567 | silence_duration: 1.333" -> first field.
			if v, err := strconv.ParseFloat(strings.Fields(field)[0], 64); err == nil {
				out = append(out, Range{Start: start, End: v, Action: "skip"})
			}
			haveStart = false
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestParseSilenceDetect`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/silence.go plugin/feed/silence_test.go
git commit -m "feed: ffmpeg silencedetect runner + stderr parser"
```

---

## Task 4: Unified ranges endpoint (replaces clean-audio)

**Files:**
- Modify: `plugin/feed/pages.go` (replace `CleanAudio` with `Ranges`)
- Modify: `plugin/feed/feed.go` (`:110` route rename)
- Test: `plugin/feed/pages_test.go` (or the package's existing handler test file — grep `cogtest.New` in the package to find it)

The endpoint returns `{"ranges":[{start,end,action}]}`. `mute` ranges come from profanity captions (existing logic, now tagged `action:"mute"`); `skip` ranges come from cached silence detection, computed lazily on first request when a local file exists.

- [ ] **Step 1: Write the failing test**

In the package's handler test file:

```go
func TestRangesEndpoint(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser("alice", "pw")

	// Seed an item with no captions and no local file → empty ranges.
	st, err := env.Plugins.Feed.DB(u.Username) // see note on accessing the store
	require.NoError(t, err)
	store := feed.NewStore(st)
	subID, _ := store.AddSubscription(feed.Subscription{URL: "http://x", Kind: "rss", Title: "X"})
	itemID, _ := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio", Title: "E", MediaURL: "http://x/a.mp3"})

	var body struct {
		Ranges []feed.Range `json:"ranges"`
	}
	env.Get(t, fmt.Sprintf("/apps/feed/api/ranges/%d", itemID), cogtest.Auth(u)).
		Status(200).JSON(&body)
	assert.Equal(t, 0, len(body.Ranges))
}
```

> Note: this plan does not assume a specific test accessor for the feed store. Before writing, grep the existing feed handler tests (`grep -rn "cogtest.New" plugin/feed`) and mirror exactly how they obtain a `*feed.Store` / per-user DB and seed data. If feed has no `cogtest`-based handler test yet, the simpler path is to test `Ranges` via a direct `httptest` request against a `Pages` built with a hand-made store — follow whatever pattern `pages_test.go` already uses. Keep the assertion (empty ranges for an itemless-of-captions audio with no local file) regardless of harness.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestRangesEndpoint`
Expected: FAIL — 404 (route still `api/clean-audio`) or compile error.

- [ ] **Step 3: Implement**

In `plugin/feed/pages.go`, replace the `CleanAudio` method (`:717`) with:

```go
// Ranges returns the unified mute (profanity) + skip (silence) ranges for an item.
// Silence ranges are computed lazily on first request when a local file exists.
func (h *Pages) Ranges(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d, err := store.GetDownload(id)
	if err != nil {
		// No download row yet — nothing to analyze.
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": []Range{}})
		return
	}

	ranges := []Range{}

	// Mute ranges from profanity captions (when captions + plugin present).
	if d.CaptionsPath != "" {
		if prof := h.plugin.Profanity(); prof != nil {
			if f, ferr := os.Open(d.CaptionsPath); ferr == nil {
				u := plugin.UserFromContext(r.Context())
				captions := profanity.ParseVTT(f)
				f.Close()
				for _, ar := range prof.AudioRanges(u.Username, captions) {
					ranges = append(ranges, Range{Start: ar.Start, End: ar.End, Action: "mute"})
				}
			} else {
				slog.Error("feed: open captions", "id", id, "err", ferr)
			}
		}
	}

	// Skip ranges from silence detection (lazy, cached, requires local file).
	if d.LocalPath != "" {
		skips, derr := h.silenceRanges(r.Context(), store, id, d)
		if derr != nil {
			slog.Error("feed: silence ranges", "id", id, "err", derr)
		}
		ranges = append(ranges, skips...)
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": ranges})
}

// silenceRanges returns cached silence ranges, computing them once if not yet done.
func (h *Pages) silenceRanges(ctx context.Context, store *Store, id int64, d Download) ([]Range, error) {
	if d.SilenceState == "done" {
		return store.GetSilenceRanges(id)
	}
	rs, err := detectSilence(ctx, d.LocalPath)
	if err != nil {
		// Cache an empty "done" result so we don't recompute in a tight loop.
		if serr := store.SetSilenceRanges(id, nil); serr != nil {
			slog.Error("feed: cache empty silence ranges", "id", id, "err", serr)
		}
		return nil, err
	}
	if serr := store.SetSilenceRanges(id, rs); serr != nil {
		return rs, serr
	}
	return rs, nil
}
```

Ensure imports in `pages.go` include `context`, `os`, `log/slog` (most already present). Confirm `h.plugin` is the `*Plugin` field on `Pages` (grep `type Pages struct`).

In `plugin/feed/feed.go`, change the route (`:110`):

```go
	ctx.RegisterAPI("api/ranges/{id}", http.HandlerFunc(pages.Ranges))
```
(remove the `api/clean-audio/{id}` registration).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestRangesEndpoint`
Expected: PASS

- [ ] **Step 5: Verify no stale references**

Run: `grep -rn "clean-audio\|CleanAudio" plugin/feed`
Expected: no matches (the templ/JS reference is removed in Task 9). If `pages.templ` / `player.js` still reference it, that is fixed in Task 9 — note it and continue.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/feed.go plugin/feed/*_test.go
git commit -m "feed: unified mute+skip ranges endpoint (replaces clean-audio)"
```

---

## Task 5: Player preferences endpoint

**Files:**
- Modify: `plugin/feed/pages.go` (add `SavePlayerPrefs` handler)
- Modify: `plugin/feed/feed.go` (register `POST player/prefs`)
- Test: feed handler test file

- [ ] **Step 1: Write the failing test**

```go
func TestSavePlayerPrefsEndpoint(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser("bob", "pw")

	form := url.Values{
		"speed":         {"1.5"},
		"skip_back_sec": {"10"},
		"skip_fwd_sec":  {"45"},
		"volume":        {"0.8"},
		"autoplay_next": {"false"},
		"skip_silence":  {"true"},
	}
	env.PostForm(t, "/apps/feed/player/prefs", form, cogtest.Auth(u)).Status(204)

	// Confirm persisted.
	st, err := env.Plugins.Feed.DB(u.Username)
	require.NoError(t, err)
	got, err := feed.NewStore(st).LoadPlayerPrefs()
	require.NoError(t, err)
	assert.Equal(t, 1.5, got.Speed)
	assert.Equal(t, false, got.AutoplayNext)
	assert.Equal(t, true, got.SkipSilence)
}
```

> Adjust `env.PostForm` / store-access to the package's actual cogtest API (grep `PostForm`, `PostJSON`, `cogtest.Auth` usage in the repo). CSRF: `RegisterAction` wraps with `RequireCSRF`; the cogtest client handles CSRF for authed form posts the same way other action tests in this package do — mirror them. If they pass a CSRF header/field, do the same.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestSavePlayerPrefsEndpoint`
Expected: FAIL — 404 / undefined handler.

- [ ] **Step 3: Implement**

In `plugin/feed/pages.go`:

```go
// SavePlayerPrefs persists per-user player preferences (gear-menu changes).
func (h *Pages) SavePlayerPrefs(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur, err := store.LoadPlayerPrefs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only overwrite fields actually present in the form (partial updates).
	if v := r.FormValue("speed"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cur.Speed = clampFloat(f, 0.25, 4.0)
		}
	}
	if v := r.FormValue("skip_back_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cur.SkipBackSec = clampInt(n, 1, 120)
		}
	}
	if v := r.FormValue("skip_fwd_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cur.SkipFwdSec = clampInt(n, 1, 120)
		}
	}
	if v := r.FormValue("volume"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cur.Volume = clampFloat(f, 0, 1)
		}
	}
	if v := r.FormValue("autoplay_next"); v != "" {
		cur.AutoplayNext = v == "true" || v == "1" || v == "on"
	}
	if v := r.FormValue("skip_silence"); v != "" {
		cur.SkipSilence = v == "true" || v == "1" || v == "on"
	}
	if err := store.SavePlayerPrefs(cur); err != nil {
		slog.Error("feed: save player prefs", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
```

In `plugin/feed/feed.go`, with the other `RegisterAction` calls (near `:127`):

```go
	ctx.RegisterAction("POST", "player/prefs", http.HandlerFunc(pages.SavePlayerPrefs))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestSavePlayerPrefsEndpoint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/feed.go plugin/feed/*_test.go
git commit -m "feed: player prefs POST endpoint"
```

---

## Task 6: Serve player.js as an embedded static asset

**Files:**
- Create: `plugin/feed/static/player.js` (placeholder content this task; behavior added Task 7)
- Modify: `plugin/feed/feed.go` (embed static dir + serve route + handler)
- Test: feed handler test file

- [ ] **Step 1: Write the failing test**

```go
func TestServePlayerJS(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser("carol", "pw")
	res := env.Get(t, "/apps/feed/static/player.js", cogtest.Auth(u)).Status(200)
	res.HeaderContains("Content-Type", "javascript")
	res.Contains("FeedPlayer") // sentinel present in the file
}
```

> Adjust `HeaderContains`/`Contains` to the package's actual response-assertion API. If there is no `HeaderContains`, assert content-type via reading the header off the recorded response the way other tests do, or drop to just `.Status(200).Contains("FeedPlayer")`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./plugin/feed/ -run TestServePlayerJS`
Expected: FAIL — 404.

- [ ] **Step 3: Implement**

Create `plugin/feed/static/player.js` with placeholder:

```js
// Feed player. Replaced with full implementation in the next task.
window.FeedPlayer = window.FeedPlayer || {};
```

In `plugin/feed/feed.go`, add a second embed near the existing `//go:embed sql` (`:22`):

```go
//go:embed sql
var sqlFS embed.FS

//go:embed static
var staticFS embed.FS
```

Add a handler method on `*Plugin` (or `*Pages`) in `feed.go`:

```go
// serveStatic serves embedded static assets (e.g. player.js) with caching.
func (p *Plugin) serveStatic(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	data, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	if _, err := w.Write(data); err != nil {
		slog.Error("feed: serve static", "file", name, "err", err)
	}
}
```

Register in `Register` (near the page routes, `:109`):

```go
	ctx.RegisterAPI("static/{file}", http.HandlerFunc(p.serveStatic))
```

Add `"strings"` to `feed.go` imports if missing (`embed`, `log/slog` already present).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./plugin/feed/ -run TestServePlayerJS`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/static/player.js plugin/feed/feed.go plugin/feed/*_test.go
git commit -m "feed: serve embedded static assets (player.js)"
```

---

## Task 7: Implement player.js (controls, scrubber, keyboard, touch, ranges, MediaSession)

**Files:**
- Modify: `plugin/feed/static/player.js` (full implementation)

This is the core deliverable. No unit test (no JS runner); verified by Task 8's snapshot + Task 12's manual checklist. Read the config from `#feed-player` `data-*` attributes (rendered in Task 8).

- [ ] **Step 1: Replace `player.js` with the full implementation**

```js
// Feed player: custom YouTube-style chrome over a <video>/<audio> element.
// Reads config from #feed-player data-* attributes. Idempotent: init() can be
// called again after an HTMX swap of #feed-player (auto-advance).
(function () {
	"use strict";

	function formatTime(sec) {
		sec = Math.floor(sec || 0);
		var h = Math.floor(sec / 3600);
		var m = Math.floor((sec % 3600) / 60);
		var s = sec % 60;
		var pad = function (n) { return (n < 10 ? "0" : "") + n; };
		return h > 0 ? h + ":" + pad(m) + ":" + pad(s) : m + ":" + pad(s);
	}

	// rangeAt returns the first range containing t, or null.
	function rangeAt(t, ranges) {
		for (var i = 0; i < ranges.length; i++) {
			if (t >= ranges[i].start && t <= ranges[i].end) return ranges[i];
		}
		return null;
	}

	function post(url, body, csrf) {
		return fetch(url, {
			method: "POST",
			headers: {
				"Content-Type": "application/x-www-form-urlencoded",
				"X-CSRF-Token": csrf,
			},
			body: body,
		});
	}

	function init() {
		var root = document.getElementById("feed-player");
		if (!root || root.dataset.bound === "1") return;
		root.dataset.bound = "1";

		var el = root.querySelector("#player-el");
		if (!el) return;

		var cfg = {
			itemID: root.dataset.itemId,
			type: root.dataset.mediaType,
			sourceKind: root.dataset.sourceKind,
			resume: parseFloat(root.dataset.resume) || 0,
			csrf: root.dataset.csrf,
			title: root.dataset.title || "",
			subscription: root.dataset.subscription || "",
			artwork: root.dataset.artwork || "",
			speed: parseFloat(root.dataset.speed) || 1,
			skipBack: parseInt(root.dataset.skipBack, 10) || 15,
			skipFwd: parseInt(root.dataset.skipFwd, 10) || 30,
			volume: root.dataset.volume === "" ? 1 : parseFloat(root.dataset.volume),
			autoplayNext: root.dataset.autoplayNext === "true",
			skipSilence: root.dataset.skipSilence === "true",
		};

		// --- element refs (rendered by feedPlayer templ) ---
		var playBtn = root.querySelector("#player-play");
		var backBtn = root.querySelector("#player-back");
		var fwdBtn = root.querySelector("#player-fwd");
		var timeLabel = root.querySelector("#player-time");
		var track = root.querySelector("#player-track");
		var played = root.querySelector("#player-played");
		var buffered = root.querySelector("#player-buffered");
		var handle = root.querySelector("#player-handle");
		var markers = root.querySelector("#player-markers");
		var speedBtn = root.querySelector("#player-speed");
		var volBtn = root.querySelector("#player-mute");
		var volRange = root.querySelector("#player-volume");
		var gearBtn = root.querySelector("#player-gear");
		var gearMenu = root.querySelector("#player-gear-menu");
		var fsBtn = root.querySelector("#player-fullscreen");
		var surface = root.querySelector("#player-surface");
		var centerBtn = root.querySelector("#player-center");
		var iconPlay = root.querySelector("#icon-play");
		var iconPause = root.querySelector("#icon-pause");
		var upNext = root.querySelector("#player-upnext");

		// --- initial element state ---
		el.volume = isNaN(cfg.volume) ? 1 : cfg.volume;
		el.playbackRate = cfg.speed;
		if (volRange) volRange.value = String(el.volume);
		if (speedBtn) speedBtn.textContent = cfg.speed + "×";

		// --- source loading (YouTube resolves a stream URL) ---
		if (cfg.sourceKind === "youtube") {
			fetch("/apps/feed/api/stream-url/" + cfg.itemID, {
				headers: { "X-CSRF-Token": cfg.csrf },
			})
				.then(function (r) { return r.json(); })
				.then(function (d) {
					if (d.url) {
						el.src = d.url;
						if (cfg.resume > 0) el.currentTime = cfg.resume;
						el.play().catch(function () {});
					}
				})
				.catch(function (e) { console.error("feed: stream-url", e); });
		} else {
			el.addEventListener("loadedmetadata", function () {
				if (cfg.resume > 0) el.currentTime = cfg.resume;
				el.play().catch(function () {});
			}, { once: true });
		}

		// --- ranges (mute + skip) ---
		var ranges = [];
		fetch("/apps/feed/api/ranges/" + cfg.itemID, {
			headers: { "X-CSRF-Token": cfg.csrf },
		})
			.then(function (r) { return r.json(); })
			.then(function (d) {
				ranges = (d.ranges || []);
				renderMarkers();
			})
			.catch(function (e) { console.error("feed: ranges", e); });

		function renderMarkers() {
			if (!markers || !el.duration) return;
			markers.innerHTML = "";
			for (var i = 0; i < ranges.length; i++) {
				var r = ranges[i];
				var d = document.createElement("div");
				d.className = "feed-marker feed-marker-" + r.action;
				d.style.position = "absolute";
				d.style.top = "0";
				d.style.bottom = "0";
				d.style.left = (100 * r.start / el.duration) + "%";
				d.style.width = Math.max(0.4, 100 * (r.end - r.start) / el.duration) + "%";
				markers.appendChild(d);
			}
		}

		// --- play/pause icon + MediaSession ---
		function reflectPlaying(playing) {
			if (iconPlay) iconPlay.style.display = playing ? "none" : "";
			if (iconPause) iconPause.style.display = playing ? "" : "none";
			if (centerBtn) centerBtn.style.opacity = playing ? "0" : "1";
			if ("mediaSession" in navigator) {
				navigator.mediaSession.playbackState = playing ? "playing" : "paused";
			}
		}
		el.addEventListener("play", function () { reflectPlaying(true); });
		el.addEventListener("pause", function () { reflectPlaying(false); });

		function toggle() { if (el.paused) el.play(); else el.pause(); }
		function seekBy(sec) { el.currentTime = Math.max(0, Math.min((el.duration || 1e9), el.currentTime + sec)); }
		function setSpeed(rate) {
			rate = Math.max(0.25, Math.min(4, rate));
			el.playbackRate = rate;
			if (speedBtn) speedBtn.textContent = (Math.round(rate * 100) / 100) + "×";
			savePrefs({ speed: rate });
			updatePositionState();
		}

		// --- preferences persistence (debounced) ---
		var prefTimer = null;
		var pendingPrefs = {};
		function savePrefs(partial) {
			Object.assign(pendingPrefs, partial);
			clearTimeout(prefTimer);
			prefTimer = setTimeout(function () {
				var body = Object.keys(pendingPrefs)
					.map(function (k) {
						var form = { speed: "speed", volume: "volume", skipSilence: "skip_silence", autoplayNext: "autoplay_next" }[k] || k;
						return form + "=" + encodeURIComponent(pendingPrefs[k]);
					})
					.join("&");
				pendingPrefs = {};
				post("/apps/feed/player/prefs", body, cfg.csrf)
					.catch(function (e) { console.error("feed: prefs", e); });
			}, 400);
		}

		// --- buttons ---
		if (playBtn) playBtn.addEventListener("click", toggle);
		if (centerBtn) centerBtn.addEventListener("click", toggle);
		if (backBtn) backBtn.addEventListener("click", function () { seekBy(-cfg.skipBack); });
		if (fwdBtn) fwdBtn.addEventListener("click", function () { seekBy(cfg.skipFwd); });
		if (volBtn) volBtn.addEventListener("click", function () {
			el.muted = !el.muted;
		});
		if (volRange) volRange.addEventListener("input", function () {
			el.muted = false;
			el.volume = parseFloat(volRange.value);
			savePrefs({ volume: el.volume });
		});
		if (gearBtn && gearMenu) gearBtn.addEventListener("click", function () {
			gearMenu.style.display = gearMenu.style.display === "block" ? "none" : "block";
		});
		// gear menu speed buttons (data-speed) + toggles (data-toggle).
		if (gearMenu) gearMenu.addEventListener("click", function (e) {
			var sp = e.target.getAttribute("data-speed");
			if (sp) { setSpeed(parseFloat(sp)); return; }
			var tg = e.target.getAttribute("data-toggle");
			if (tg === "skip_silence") {
				cfg.skipSilence = !cfg.skipSilence;
				e.target.setAttribute("aria-pressed", cfg.skipSilence ? "true" : "false");
				savePrefs({ skipSilence: cfg.skipSilence });
			} else if (tg === "autoplay_next") {
				cfg.autoplayNext = !cfg.autoplayNext;
				e.target.setAttribute("aria-pressed", cfg.autoplayNext ? "true" : "false");
				savePrefs({ autoplayNext: cfg.autoplayNext });
			}
		});
		if (speedBtn) speedBtn.addEventListener("click", function () {
			var steps = [1, 1.25, 1.5, 1.75, 2, 0.75];
			var idx = steps.indexOf(Math.round(el.playbackRate * 100) / 100);
			setSpeed(steps[(idx + 1) % steps.length]);
		});
		if (fsBtn) fsBtn.addEventListener("click", function () {
			var target = surface || root;
			if (document.fullscreenElement) document.exitFullscreen();
			else if (target.requestFullscreen) target.requestFullscreen();
		});

		// --- scrubber: click + drag ---
		function seekToClientX(clientX) {
			if (!track || !el.duration) return;
			var rect = track.getBoundingClientRect();
			var frac = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
			el.currentTime = frac * el.duration;
		}
		var dragging = false;
		if (track) {
			track.addEventListener("mousedown", function (e) { dragging = true; seekToClientX(e.clientX); });
			document.addEventListener("mousemove", function (e) { if (dragging) seekToClientX(e.clientX); });
			document.addEventListener("mouseup", function () { dragging = false; });
			// touch drag
			track.addEventListener("touchstart", function (e) { dragging = true; seekToClientX(e.touches[0].clientX); }, { passive: true });
			track.addEventListener("touchmove", function (e) { if (dragging) seekToClientX(e.touches[0].clientX); }, { passive: true });
			track.addEventListener("touchend", function () { dragging = false; });
		}

		// --- touch: tap center play/pause, double-tap thirds to seek ---
		if (surface) {
			var lastTap = 0;
			surface.addEventListener("touchend", function (e) {
				var now = Date.now();
				var rect = surface.getBoundingClientRect();
				var x = e.changedTouches[0].clientX - rect.left;
				if (now - lastTap < 300) {
					if (x < rect.width / 3) seekBy(-10);
					else if (x > rect.width * 2 / 3) seekBy(10);
					else toggle();
				} else {
					showControls();
				}
				lastTap = now;
			});
		}

		// --- progress + ranges loop ---
		var lastSaved = 0;
		var wasMuted = false;
		var savedVol = el.volume;
		el.addEventListener("timeupdate", function () {
			var t = el.currentTime;
			var dur = el.duration || 0;
			if (timeLabel) timeLabel.textContent = formatTime(t) + " / " + formatTime(dur);
			if (played && dur) played.style.width = (100 * t / dur) + "%";
			if (handle && dur) handle.style.left = (100 * t / dur) + "%";
			if (buffered && dur && el.buffered.length) {
				buffered.style.width = (100 * el.buffered.end(el.buffered.length - 1) / dur) + "%";
			}

			// ranges: mute vs skip
			var hit = rangeAt(t, ranges);
			if (hit && hit.action === "skip" && cfg.skipSilence) {
				el.currentTime = hit.end;
			} else if (hit && hit.action === "mute") {
				if (!wasMuted) { savedVol = el.volume; el.volume = 0; wasMuted = true; }
			} else if (wasMuted) {
				el.volume = savedVol; wasMuted = false;
			}

			updatePositionState();

			if (Math.abs(t - lastSaved) >= 15) {
				lastSaved = t;
				post("/apps/feed/items/" + cfg.itemID + "/progress", "progress=" + t.toFixed(1), cfg.csrf)
					.catch(function (e) { console.error("feed: progress", e); });
			}
		});
		el.addEventListener("loadedmetadata", renderMarkers);

		// --- ended: complete + auto-advance ---
		el.addEventListener("ended", function () {
			post("/apps/feed/items/" + cfg.itemID + "/complete", "", cfg.csrf)
				.then(function (r) { return r.text(); })
				.then(function (html) {
					if (cfg.autoplayNext && html && html.indexOf("feed-player") !== -1) {
						if (upNext) {
							upNext.style.display = "block";
							setTimeout(function () { swapPlayer(html); }, 1500);
						} else {
							swapPlayer(html);
						}
					}
				})
				.catch(function (e) { console.error("feed: complete", e); });
		});

		function swapPlayer(html) {
			var tmp = document.createElement("div");
			tmp.innerHTML = html;
			var next = tmp.querySelector("#feed-player");
			if (next && root.parentNode) {
				root.parentNode.replaceChild(next, root);
				init(); // bind the new player
			}
		}

		// --- controls auto-hide (video) ---
		var hideTimer = null;
		function showControls() {
			root.classList.remove("controls-hidden");
			clearTimeout(hideTimer);
			if (cfg.type === "video" && !el.paused) {
				hideTimer = setTimeout(function () { root.classList.add("controls-hidden"); }, 2500);
			}
		}
		if (surface) {
			surface.addEventListener("mousemove", showControls);
			el.addEventListener("play", showControls);
		}

		// --- keyboard ---
		function typingTarget(e) {
			var n = e.target.nodeName;
			return n === "INPUT" || n === "TEXTAREA" || e.target.isContentEditable;
		}
		document.addEventListener("keydown", function (e) {
			if (typingTarget(e)) return;
			if (!document.body.contains(root)) return; // player removed
			switch (e.key) {
				case " ": case "k": e.preventDefault(); toggle(); break;
				case "ArrowLeft": seekBy(-5); break;
				case "ArrowRight": seekBy(5); break;
				case "j": seekBy(-10); break;
				case "l": seekBy(10); break;
				case "ArrowUp": el.muted = false; el.volume = Math.min(1, el.volume + 0.05); savePrefs({ volume: el.volume }); break;
				case "ArrowDown": el.muted = false; el.volume = Math.max(0, el.volume - 0.05); savePrefs({ volume: el.volume }); break;
				case "m": el.muted = !el.muted; break;
				case "f": if (fsBtn) fsBtn.click(); break;
				case "<": case ",": setSpeed(el.playbackRate - 0.25); break;
				case ">": case ".": setSpeed(el.playbackRate + 0.25); break;
				default:
					if (e.key >= "0" && e.key <= "9" && el.duration) {
						el.currentTime = el.duration * (parseInt(e.key, 10) / 10);
					}
			}
		});

		// --- MediaSession (device OS controls) ---
		function updatePositionState() {
			if (!("mediaSession" in navigator) || !navigator.mediaSession.setPositionState) return;
			if (!el.duration || !isFinite(el.duration)) return;
			try {
				navigator.mediaSession.setPositionState({
					duration: el.duration,
					position: Math.min(el.currentTime, el.duration),
					playbackRate: el.playbackRate,
				});
			} catch (e) { /* some browsers throw on bad values */ }
		}
		if ("mediaSession" in navigator) {
			var art = cfg.artwork ? [{ src: cfg.artwork, sizes: "512x512", type: "image/png" }] : [];
			navigator.mediaSession.metadata = new MediaMetadata({
				title: cfg.title,
				artist: cfg.subscription,
				artwork: art,
			});
			var handlers = {
				play: function () { el.play(); },
				pause: function () { el.pause(); },
				seekbackward: function () { seekBy(-cfg.skipBack); },
				seekforward: function () { seekBy(cfg.skipFwd); },
				seekto: function (d) { if (d.seekTime != null) el.currentTime = d.seekTime; },
				previoustrack: function () { el.currentTime = 0; },
				nexttrack: function () {
					post("/apps/feed/items/" + cfg.itemID + "/complete", "", cfg.csrf)
						.then(function (r) { return r.text(); })
						.then(function (html) { if (html.indexOf("feed-player") !== -1) swapPlayer(html); })
						.catch(function (e) { console.error("feed: nexttrack", e); });
				},
			};
			Object.keys(handlers).forEach(function (action) {
				try { navigator.mediaSession.setActionHandler(action, handlers[action]); }
				catch (e) { /* unsupported action */ }
			});
		}
	}

	window.FeedPlayer = { init: init };

	// Initialize on load and after HTMX swaps that bring in #feed-player.
	if (document.readyState !== "loading") init();
	else document.addEventListener("DOMContentLoaded", init);
	document.body.addEventListener("htmx:afterSwap", function () { init(); });
})();
```

- [ ] **Step 2: Lint/build check (no JS runner)**

Run: `go build ./...`
Expected: success (the .js is embedded, not compiled). Eyeball the file for balanced braces.

- [ ] **Step 3: Commit**

```bash
git add plugin/feed/static/player.js
git commit -m "feed: full interactive player.js (scrubber, keyboard, touch, ranges, MediaSession)"
```

---

## Task 8: Rewrite feedPlayer templ markup + config blob + CSS

**Files:**
- Modify: `plugin/feed/pages.templ` (rewrite `feedPlayer`; delete `feedPlayerScript`; add player CSS)
- Regenerate: `go tool templ generate ./plugin/feed/`
- Modify (call sites): none needed — `feedItemDetail` and `MarkComplete` already call `feedPlayer(ip, csrfToken)`.

The templ must render the element IDs `player.js` queries (Task 7), the `data-*` config, and load the script. `feedPlayer` now needs `PlayerPrefs` and artwork — pass them in.

- [ ] **Step 1: Change the `feedPlayer` signature + render the new markup**

Replace `feedPlayer` (`pages.templ:738`) and delete `feedPlayerScript` (`:797`). New signature:

```
templ feedPlayer(ip ItemPlayback, prefs PlayerPrefs, artwork string, csrfToken string) {
```

Markup (tabs, not spaces). IDs must match Task 7 exactly:

```
templ feedPlayer(ip ItemPlayback, prefs PlayerPrefs, artwork string, csrfToken string) {
	<div
		id="feed-player"
		class="card feed-player"
		data-item-id={ strconv.FormatInt(ip.ID, 10) }
		data-media-type={ ip.Type }
		data-source-kind={ ip.SourceKind }
		data-resume={ fmt.Sprintf("%.1f", ip.ProgressSec) }
		data-csrf={ csrfToken }
		data-title={ ip.Title }
		data-subscription={ ip.SubscriptionTitle }
		data-artwork={ artwork }
		data-speed={ strconv.FormatFloat(prefs.Speed, 'f', -1, 64) }
		data-skip-back={ strconv.Itoa(prefs.SkipBackSec) }
		data-skip-fwd={ strconv.Itoa(prefs.SkipFwdSec) }
		data-volume={ strconv.FormatFloat(prefs.Volume, 'f', -1, 64) }
		data-autoplay-next={ strconv.FormatBool(prefs.AutoplayNext) }
		data-skip-silence={ strconv.FormatBool(prefs.SkipSilence) }
	>
		<div style="font-weight:600;margin-bottom:0.25rem">{ ip.Title }</div>
		<div class="text-muted" style="font-size:0.85rem;margin-bottom:0.5rem">{ ip.SubscriptionTitle }</div>
		if ip.Type == "video" {
			<div id="player-surface" class="player-surface">
				<video id="player-el" preload="metadata" playsinline
					if ip.SourceKind != "youtube" {
						src={ "/apps/feed/items/" + strconv.FormatInt(ip.ID, 10) + "/media" }
					}
				></video>
				<button id="player-center" class="player-center" title="Play/Pause" type="button">
					<svg width="34" height="34" viewBox="0 0 24 24" fill="#fff"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>
				</button>
			</div>
		} else {
			<audio id="player-el" preload="metadata"
				if ip.SourceKind != "youtube" {
					src={ "/apps/feed/items/" + strconv.FormatInt(ip.ID, 10) + "/media" }
				}
			></audio>
		}
		<!-- scrubber -->
		<div id="player-track" class="player-track">
			<div id="player-buffered" class="player-buffered"></div>
			<div id="player-played" class="player-played"></div>
			<div id="player-markers" class="player-markers"></div>
			<div id="player-handle" class="player-handle"></div>
		</div>
		<!-- buttons -->
		<div class="player-bar">
			<button id="player-play" class="btn btn-primary btn-sm player-round" title="Play/Pause" type="button">
				<svg id="icon-play" width="18" height="18" viewBox="0 0 24 24" fill="currentColor"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>
				<svg id="icon-pause" width="18" height="18" viewBox="0 0 24 24" fill="currentColor" style="display:none"><rect x="6" y="4" width="4" height="16"></rect><rect x="14" y="4" width="4" height="16"></rect></svg>
			</button>
			<button id="player-back" class="btn btn-outline btn-sm" title="Skip back" type="button">&#8630;</button>
			<button id="player-fwd" class="btn btn-outline btn-sm" title="Skip forward" type="button">&#8631;</button>
			<span id="player-time" class="text-muted" style="font-size:0.85rem;font-variant-numeric:tabular-nums">0:00 / 0:00</span>
			<span style="margin-left:auto"></span>
			<button id="player-speed" class="btn btn-outline btn-sm" title="Speed" type="button">1&times;</button>
			<button id="player-mute" class="btn btn-outline btn-sm" title="Mute" type="button">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polygon points="11 5 6 9 2 9 2 15 6 15 11 19 11 5"></polygon><path d="M19.07 4.93a10 10 0 0 1 0 14.14M15.54 8.46a5 5 0 0 1 0 7.07"></path></svg>
			</button>
			<input id="player-volume" class="player-volume" type="range" min="0" max="1" step="0.05" title="Volume"/>
			<div style="position:relative">
				<button id="player-gear" class="btn btn-outline btn-sm" title="Settings" type="button">
					<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="3"></circle><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"></path></svg>
				</button>
				<div id="player-gear-menu" class="player-gear-menu" style="display:none">
					<div class="player-gear-label">Speed</div>
					<div class="player-gear-speeds">
						<button class="btn btn-outline btn-sm" data-speed="0.75" type="button">0.75&times;</button>
						<button class="btn btn-outline btn-sm" data-speed="1" type="button">1&times;</button>
						<button class="btn btn-outline btn-sm" data-speed="1.25" type="button">1.25&times;</button>
						<button class="btn btn-outline btn-sm" data-speed="1.5" type="button">1.5&times;</button>
						<button class="btn btn-outline btn-sm" data-speed="2" type="button">2&times;</button>
					</div>
					<button class="player-gear-toggle" data-toggle="skip_silence" type="button" aria-pressed={ strconv.FormatBool(prefs.SkipSilence) }>Skip silence</button>
					<button class="player-gear-toggle" data-toggle="autoplay_next" type="button" aria-pressed={ strconv.FormatBool(prefs.AutoplayNext) }>Autoplay next</button>
				</div>
			</div>
			<button id="player-fullscreen" class="btn btn-outline btn-sm" title="Fullscreen" type="button">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M8 3H5a2 2 0 0 0-2 2v3m18 0V5a2 2 0 0 0-2-2h-3m0 18h3a2 2 0 0 0 2-2v-3M3 16v3a2 2 0 0 0 2 2h3"></path></svg>
			</button>
		</div>
		<div id="player-upnext" class="text-muted" style="display:none;font-size:0.8rem;margin-top:0.5rem">Up next&hellip;</div>
		<script src="/apps/feed/static/player.js" defer></script>
	</div>
}
```

- [ ] **Step 2: Add player CSS**

The repo styles via global CSS / `ui` package. Add a small `<style>` once. Simplest: include a `feedPlayerStyles()` templ rendered alongside the player, or append to the player markup a single `<style>` block. Add this `<style>` immediately inside `#feed-player` (top), so it ships with the fragment:

```
		<style>
			.feed-player .player-surface { position:relative; background:#000; border-radius:8px; overflow:hidden; margin-bottom:0.5rem; }
			.feed-player .player-surface video { width:100%; max-height:480px; display:block; }
			.feed-player .player-center { position:absolute; inset:0; margin:auto; width:72px; height:72px; border:0; border-radius:50%; background:rgba(0,0,0,0.55); display:flex; align-items:center; justify-content:center; cursor:pointer; transition:opacity .2s; }
			.feed-player .player-track { position:relative; height:5px; border-radius:3px; background:rgba(127,127,127,0.3); margin:0.25rem 0 0.75rem; cursor:pointer; }
			.feed-player .player-track:hover { height:7px; }
			.feed-player .player-buffered, .feed-player .player-played { position:absolute; left:0; top:0; bottom:0; border-radius:3px; }
			.feed-player .player-buffered { width:0; background:rgba(127,127,127,0.5); }
			.feed-player .player-played { width:0; background:var(--accent,#4a9eff); }
			.feed-player .player-handle { position:absolute; left:0; top:50%; transform:translate(-50%,-50%); width:12px; height:12px; border-radius:50%; background:#fff; box-shadow:0 0 3px rgba(0,0,0,0.5); opacity:0; }
			.feed-player .player-track:hover .player-handle { opacity:1; }
			.feed-player .player-markers { position:absolute; inset:0; pointer-events:none; }
			.feed-player .feed-marker-mute { background:rgba(220,80,80,0.7); }
			.feed-player .feed-marker-skip { background:rgba(120,120,120,0.7); }
			.feed-player .player-bar { display:flex; align-items:center; gap:0.5rem; flex-wrap:wrap; }
			.feed-player .player-round { width:36px; height:36px; padding:0; border-radius:50%; display:inline-flex; align-items:center; justify-content:center; }
			.feed-player .player-volume { width:80px; }
			.feed-player .player-gear-menu { position:absolute; right:0; bottom:calc(100% + 4px); z-index:10; background:var(--card-bg,#fff); border:1px solid var(--border,#ddd); border-radius:8px; padding:0.5rem; min-width:180px; box-shadow:0 4px 16px rgba(0,0,0,0.15); }
			.feed-player .player-gear-label { font-size:0.75rem; text-transform:uppercase; opacity:0.6; margin-bottom:0.25rem; }
			.feed-player .player-gear-speeds { display:flex; gap:0.25rem; flex-wrap:wrap; margin-bottom:0.5rem; }
			.feed-player .player-gear-toggle { display:block; width:100%; text-align:left; padding:0.35rem 0.5rem; border:0; background:none; cursor:pointer; border-radius:4px; }
			.feed-player .player-gear-toggle[aria-pressed=true] { background:var(--accent,#4a9eff); color:#fff; }
			.feed-player.controls-hidden .player-bar, .feed-player.controls-hidden .player-track { opacity:0; transition:opacity .3s; }
			.feed-player.controls-hidden .player-center { opacity:0; }
		</style>
```

> If the implementer finds the repo already centralizes plugin CSS (grep for an existing `<style>` pattern in feed templ or a shared stylesheet), prefer adding these rules there. The inline `<style>` is acceptable because the fragment may be HTMX-swapped standalone.

- [ ] **Step 3: Update call sites to pass prefs + artwork**

`feedItemDetail` (renders `@feedPlayer(ip, csrfToken)` at `pages.templ:728`) must now pass prefs + artwork. Two options — pick the one matching how `feedItemDetail` gets its data:

a) Change `feedItemDetail`'s signature to accept `prefs PlayerPrefs` and compute artwork, threading from the `ItemDetail` handler. Artwork: `ip.ThumbnailURL` if set, else the subscription image — for v1 use `ip.ThumbnailURL`.

In `pages.go` `ItemDetail` handler (around `:633`), before rendering, load prefs:

```go
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		slog.Error("feed: load player prefs", "err", err)
		prefs, _ = (&Store{}).LoadPlayerPrefs() // defaults; or zero-value safe defaults
	}
	artwork := ip.ThumbnailURL
```
Then pass into `feedItemDetail(ip, tags, prefs, artwork, plugin.CSRFToken(r))` and update the templ `feedItemDetail` signature + its `@feedPlayer(...)` call to `@feedPlayer(ip, prefs, artwork, csrfToken)`.

> The fallback `(&Store{}).LoadPlayerPrefs()` will panic (nil db) — instead just use a literal default `PlayerPrefs{Speed:1, SkipBackSec:15, SkipFwdSec:30, Volume:1, AutoplayNext:true}` on error. Use that literal, not the nil-store call.

`MarkComplete` (`pages.go:684`) also renders `feedPlayer(nextIP, plugin.CSRFToken(r))`. Update it to load prefs + artwork for the next item the same way:

```go
		prefs, err := store.LoadPlayerPrefs()
		if err != nil {
			prefs = PlayerPrefs{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1, AutoplayNext: true}
		}
		feedPlayer(nextIP, prefs, nextIP.ThumbnailURL, plugin.CSRFToken(r)).Render(r.Context(), w)
```

- [ ] **Step 4: Regenerate templ + build**

Run:
```bash
go tool templ generate ./plugin/feed/
go build ./...
```
Expected: success. (Ignore any gopls "undefined: feedPlayer/sidebarData" editor diagnostics — verify with the real build, which is authoritative.)

- [ ] **Step 5: Run package tests**

Run: `go test ./plugin/feed/...`
Expected: PASS. The `ended`→complete→auto-advance test (if present from Task 4/5) still passes; `MarkComplete` still returns the next player HTML.

- [ ] **Step 6: Commit**

```bash
git add plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages.go
git commit -m "feed: custom player chrome markup + config blob + CSS; load player.js"
```

---

## Task 9: Remove dead clean-audio references + old script

**Files:**
- Verify/modify: `plugin/feed/pages.templ`, `plugin/feed/static/player.js`, any test

- [ ] **Step 1: Grep for stragglers**

Run:
```bash
grep -rn "clean-audio\|feedPlayerScript\|playerToggle\|playerSkip" plugin/feed
```
Expected: no matches (old inline script + endpoint fully replaced). If any remain (e.g. a test referencing `api/clean-audio`), update it to `api/ranges`.

- [ ] **Step 2: Build + full test**

Run:
```bash
go build ./...
go test ./plugin/feed/...
```
Expected: PASS.

- [ ] **Step 3: Commit (only if changes were needed)**

```bash
git add -A plugin/feed
git commit -m "feed: drop dead clean-audio/inline-player references"
```

---

## Task 10: Update browser snapshot

**Files:**
- Snapshot fixtures under `cmd/cog/` (managed by `TestBrowserMin`)

- [ ] **Step 1: Run the snapshot test to see the diff**

Run: `go test ./cmd/cog/... -run TestBrowserMin`
Expected: likely FAIL (player markup changed dimensions/content).

- [ ] **Step 2: Update snapshots**

Run: `WEBTEST_UPDATE=1 go test ./cmd/cog/...`
Expected: snapshots regenerated.

- [ ] **Step 3: Re-run to confirm green**

Run: `go test ./cmd/cog/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cmd/cog
git commit -m "feed: update browser snapshot for new player chrome"
```

---

## Task 11: Dockerfile — confirm ffmpeg present

**Files:**
- Review: `Dockerfile`

Silence detection needs `ffmpeg` (profanity TTS already uses it).

- [ ] **Step 1: Check**

Run: `grep -n "ffmpeg" Dockerfile`
Expected: ffmpeg already installed (profanity path). If **absent**, add it to the apt install line in the runtime stage, e.g. `ffmpeg`. If present, no change.

- [ ] **Step 2: Commit (only if changed)**

```bash
git add Dockerfile
git commit -m "feed: ensure ffmpeg present for silence detection"
```

---

## Task 12: Full verify + manual checklist

**Files:** none (verification only)

- [ ] **Step 1: Full verify**

Run: `go tool verify`
Expected: generate/fmt/vet/tests all green.

- [ ] **Step 2: Manual smoke test (dev server)**

Run: `go tool dev` (admin:admin), open the feed plugin, play an audio item and a video item. Verify, checking each:
- [ ] Scrubber fills as it plays; click + drag seeks; handle appears on hover.
- [ ] Play/pause via button, center overlay (video), and `space`/`k`.
- [ ] Skip buttons + `j`/`l` + `←`/`→` seek by the right amounts.
- [ ] Speed button cycles; gear menu speed buttons set rate; rate persists to the next item (reload / next episode keeps it).
- [ ] Volume slider + `↑`/`↓` + `m` mute work; volume persists.
- [ ] Gear menu "Skip silence" toggle persists; with it on, detected silence is skipped (try a podcast with gaps; first play computes ranges — second play uses cache).
- [ ] Profanity mute still works (subscription with `mute_profanity`): volume drops over flagged spans; markers show on scrubber.
- [ ] On end: marks complete; with "Autoplay next" on, advances to the next unplayed item; with it off, stops.
- [ ] Fullscreen (`f`) works for video.
- [ ] MediaSession: OS/lock-screen shows title + subscription + artwork; hardware/media keys play/pause/seek; next-track advances.
- [ ] Controls auto-hide on idle video playback and reappear on movement.

- [ ] **Step 3: Final commit (if any fixes)**

```bash
git add -A
git commit -m "feed: interactive player fixes from manual smoke test"
```

---

## Self-Review (completed during planning)

- **Spec coverage:** Section 1 chrome → Task 8 (markup/CSS) + Task 7 (behavior). Section 2 keyboard/touch → Task 7. Section 3 settings/persistence → Tasks 1, 5, 8. Section 4 unified ranges + silence + auto-advance → Tasks 2, 3, 4, 7 (+ existing MarkComplete). Section 5 MediaSession → Task 7. Data flow + endpoints → Tasks 4, 5, 6. Error handling (no swallowed errors) → wrapped/logged throughout. Testing → Tasks 1–6 unit/handler tests + Task 10 snapshot + Task 12 manual. Non-goals (transcript, normalization-as-filter, mini-player) correctly excluded.
- **Placeholders:** none — all code blocks complete; the few "grep and match the existing pattern" notes are deliberate harness-discovery steps with a concrete fallback assertion, not deferred work.
- **Type consistency:** `Range{Start,End,Action}` defined Task 2, used Tasks 3/4/7. `PlayerPrefs` fields consistent across Tasks 1/5/8. Endpoint paths consistent: `api/ranges/{id}`, `player/prefs`, `static/{file}`. Element IDs in Task 8 templ match the `getElementById`/`querySelector` IDs in Task 7 player.js.
