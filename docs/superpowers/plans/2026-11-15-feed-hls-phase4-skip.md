# Feed HLS, Phase 4: Skip Silence in the Playlist — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `master.m3u8?sig=…&skip=silence` presents an item with its silences left out, so a player that cannot skip silence (mpv) gets a shorter presentation whose audio, video and captions are cut identically. A cut inside a segment comes from a small re-encoded edge segment that starts or ends exactly at the cut.

**Architecture:**
- **`plugin/feed/hls`** gains pure functions:
  - Effective cuts, made from silence ranges and a minimum-length pref.
  - A per-track cut plan: whole segments kept, whole segments dropped, and edge pieces with exact times.
  - The edit-list id.
  - Mapping between presentation time and item time.
- **`pipeline.Edges`** is a worker that runs after Silence and encodes the edge pieces of items whose skip presentation was asked for. It writes them to the cache under `edges/<item>/<edit>/`.
- **The HLS route** accepts a signed `skip=silence` option and writes the cut playlists from the plan:
  - `EXT-X-DISCONTINUITY` at every jump and every change of file.
  - `EXT-X-MAP` per edge file.
  - Subtitles re-segmented to the audio's pieces.
- **Before edges exist:**
  - An audio-only presentation is cut coarsely, dropping whole segments only.
  - A video presentation is served unskipped, marked `pending`.
- **Edit lists:** every edit list served is stored, and `ReportProgress` maps a presentation time back to item time.

**Tech Stack:** Go 1.26, SQLite (per-user plugin DB), ffmpeg (libx264 and aac) through `internal/sandbox`, connect/protobuf (`feedpb`), templ, cogtest.

**Spec:** `DESIGN/plugins/feed_hls.md` (§ Skip, § Timeline, § Options, § Signed URLs). The binding cross-phase decisions and interfaces are in `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md`, decisions 3–7. Read both, and `CLAUDE.md`, first.

## Global Constraints

- **Repo and branch:** work in the worktree `cog/tmp/feed-hls` on branch `feed-hls`, after phases 2 and 3 have landed there. Don't push, merge or open an MR without asking.
- **`CLAUDE.md` rules:**
  - Never swallow errors: wrap and return, or log.
  - POST handlers use PRG with `setFlash`.
  - Tests go through `cogtest`, with `internal/testing/require` and `assert`, never testify.
- **Programs:** run them through `sandbox.CommandContext` with the narrowest `Spec`: read the input, write only the output directory, no network. ffmpeg inputs go through `ffargs.Input`.
- **Files:** go through `feed.Space`. Edges live in `space.Cache` under `edges/<item>/<edit>/`, never under `media/`.
- **Schema:** the new file is `sql/migration007_skip.sql`, and `go generate ./plugin/feed` regenerates `sql/schema.sql`. Protos are regenerated with `go generate ./plugin/feed/feedpb`.
- **Tooling:** check builds with `go install ./...`. Format with `go fmt ./...` and `go tool templ fmt .`. Regenerate templ with `go tool templ generate -log-level warn`.
- **Decided; don't reopen:**
  - `skip=silence` is a query parameter.
  - A cut inside a segment transcodes only that edge segment.
  - A player that can skip silence does it itself, so Cog's clients don't ask for `skip`.
  - Video and audio take the same cuts.
- **Cuts:** a range shorter than `skip_silence_min_sec` is dropped. Ranges whose gap is shorter than that minimum are merged. Each surviving cut is shrunk by **0.1 s** at both ends. The pref defaults to **1.0** and is bounded **[0.5, 10]**.
- **Signing:** the message is `"cog-feed-hls\x00" + user + "\x00" + item`, plus `"\x00" + canonical` only when there are options. `canonical` is `url.Values.Encode()` without `sig`. Phase-1 links stay valid.
- **Edit-list id:** the first **12 hex** characters of SHA-256 over the canonical JSON of the applied cuts and every track's segment layout.
- **Video edges:** re-encoded only for `avc1`, with libx264 at the source's resolution and a profile and level no higher than the source's. Any other codec gets `#EXT-X-SESSION-DATA:DATA-ID="us.duckfam.cog.skip",VALUE="unsupported"` and no skip.
- **ffmpeg never runs in a request.**
- `needFFmpeg(t)` skips locally when ffmpeg is missing and fails when `CI` is set.
- **Commit trailer:** every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX
  ```

## Decisions this plan makes (record them in the design in Task 11)

- **Edges are made on demand, not for the whole library.** A master request with `skip=silence` sets `downloads.edges_wanted = 1` and wakes the orchestrator (`TriggerFetch`). Phase 6a's M3U marks every item it lists, through `Store.WantEdges`. Encoding video edges for every video item would spend CPU on items nobody plays in mpv.
- **Video waits for its edges.** When the full plan's edges are not ready, an audio-only presentation is cut coarsely, dropping only the segments wholly inside a cut. A video presentation is served unskipped, with `#EXT-X-SESSION-DATA:DATA-ID="us.duckfam.cog.skip",VALUE="pending"`. A coarse cut lands on different times in audio and video, because their segment boundaries differ, and that would desync them.
- **Timestamps stay in item time.** Kept segments carry their original `tfdt`. Edge files are encoded with `-output_ts_offset <piece start>`. A discontinuity only tells the player not to wait across the gap, and the cues of a subtitle segment keep item times with phase 2's `X-TIMESTAMP-MAP`.
- **A discontinuity comes before an entry** when its start is not the previous entry's end, or when it comes from a different file than the previous entry.
- **The applied cuts identify an edit list.** For the full plan they are the effective cuts. For a coarse plan they are the spans of the dropped segments. `hls_edits` stores the applied cuts of every edit id it serves, written with `INSERT OR IGNORE` on master requests only.
- **The web player's ranges get the same effective cuts.** `api/ranges/{id}` returns the skip ranges `hls.EffectiveCuts` makes, so the web player's own skipping and the playlist's cuts agree.
- **Stale edges are removed by the worker** when it makes a new edit for an item. **Collect** removes `edges/<item>/` for items that no longer have a ready download.

## Review Focus

1. **A phase-1 link with no options still plays** (the signature is unchanged), and `skip=silence` cannot be added to a link that was signed without it. (Task 4: `TestHLSOptionsSignature`, `TestHLSSkipNeedsItsOwnSignature`)
2. **An item with no silence, or whose silence detection hasn't finished, under `skip=silence`** gets a playlist with no discontinuity, identical to the unskipped one apart from URIs. (Task 8: `TestHLSSkipNoRanges`)
3. **Cuts touching the ends of the item** (a range starting at 0, or reaching the duration) give no leading discontinuity, no zero-length edge, and no entry past the end. (Task 3: `TestPlanCutsAtEnds`)
4. **After the min-length pref changes,** a playlist never points at the edge files of the old edit. It is coarse or pending until the worker catches up. (Task 8: `TestHLSSkipAfterPrefChange`)
5. **The video and audio of a skip presentation have the same length** as the item minus the cuts, measured by ffmpeg reading the playlist the way mpv's demuxer does. (Task 9: `TestHLSSkipPlaysInFFmpeg`)

---

### Task 1: Reconcile with phases 2 and 3

**Files:**
- Read: `plugin/feed/hls/playlist.go`, `plugin/feed/hls/vtt.go`, `plugin/feed/hls_serve.go`, `plugin/feed/hls_links.go`, `plugin/feed/store.go`, `sql/`
- Modify: the ledger only

**Interfaces:**
- Consumes (from the roadmap, phases 2 and 3):
  - `hls.Segment{Start, Duration float64; Offset, Length int64}`
  - `hls.Init{URI string; Offset, Length int64}`
  - `hls.Entry{URI; Offset, Length; Duration; Init; Discontinuity}`
  - `hls.Playlist([]Entry) string`
  - `hls.TrackEntries(Track, string) []Entry`
  - `hls.Rendition`
  - `hls.Presentation{Audio, Video, VideoURI, Subtitles, SessionData [][2]string}`
  - `hls.Master(Presentation) string`
  - `hls.Cue`
  - `hls.VTTSegment(cues []Cue, start, end float64, mpegts int64) string`
  - the file names `subs.m3u8`, `subs-<n>.vtt`, `clean.m3u8`, `clean.m4a`
  - `Download.CleanTrackPath`
  - the highest existing migration number
- Produces: ledger rulings for every mismatch. No code.

- [ ] **Step 1: Check each Consumes item against the code**

Run: `go doc ./plugin/feed/hls` and `ls plugin/feed/sql`
Expected: every listed name exists with the listed signature, and the newest migration is `migration006_clean_track.sql`. For each difference, ledger `Task 1: Ruling: <roadmap name> is <actual> — use <actual> throughout this plan`. If phase 3 took a different migration number, this plan's migration is the next free number.

- [ ] **Step 2: Note how phase 2 computes `mpegts` and names subtitle segments**

Read `hls_serve.go`'s `subs.m3u8` case. Ledger the expression it uses for `mpegts` and the index scheme of `subs-<n>.vtt`. Task 8 reuses both.

---

### Task 2: Effective cuts and time mapping (pure)

**Files:**
- Create: `plugin/feed/hls/cuts.go`
- Test: `plugin/feed/hls/cuts_test.go`

**Interfaces:**
- Produces:
  ```go
  package hls
  // Cut is a span of item time, in seconds, a skip presentation leaves out.
  type Cut struct{ Start, End float64 }
  // CutMargin is how much of a silence is kept at each end of a cut.
  const CutMargin = 0.1
  // EffectiveCuts turns silence ranges into the cuts a presentation applies:
  // ranges shorter than minLen dropped, ranges closer than minLen merged,
  // each shrunk by CutMargin at both ends, clamped to [0, duration], sorted.
  func EffectiveCuts(silences []Cut, minLen, duration float64) []Cut
  // PresentationDuration is duration less the cuts.
  func PresentationDuration(cuts []Cut, duration float64) float64
  // ItemTime maps a time in a presentation with cuts to item time; a time
  // past the end maps to the item's end of the last kept span.
  func ItemTime(cuts []Cut, presentation float64) float64
  // PresentationTime maps item time to presentation time; an item time
  // inside a cut maps to where the cut is in the presentation.
  func PresentationTime(cuts []Cut, item float64) float64
  ```

- [ ] **Step 1: Write the failing tests**

```go
func TestEffectiveCuts(t *testing.T) {
	got := hls.EffectiveCuts([]hls.Cut{{2.5, 4.2}, {5.0, 5.6}, {7.0, 9.6}, {9.9, 11.5}}, 1.0, 12)
	// 5.0–5.6 is under 1 s: dropped. 9.6→9.9 is a gap under 1 s: merged.
	assertCuts(t, []hls.Cut{{2.6, 4.1}, {7.1, 11.4}}, got)
}

func TestEffectiveCutsDropsBeforeMerging(t *testing.T) {
	// Two short silences close together are not merged into a long one.
	assertCuts(t, nil, hls.EffectiveCuts([]hls.Cut{{1, 1.6}, {1.8, 2.4}}, 1.0, 12))
}

func TestEffectiveCutsClampsToTheItem(t *testing.T) {
	assertCuts(t, []hls.Cut{{0, 1.9}, {10.1, 12}}, hls.EffectiveCuts([]hls.Cut{{-1, 2}, {10, 13}}, 1.0, 12))
}

func TestEffectiveCutsUnsorted(t *testing.T) {
	assertCuts(t, []hls.Cut{{1.1, 2.9}, {5.1, 6.9}}, hls.EffectiveCuts([]hls.Cut{{5, 7}, {1, 3}}, 1.0, 12))
}

func TestTimeMapping(t *testing.T) {
	cuts := []hls.Cut{{2, 4}, {6, 7}}
	assert.InDelta(t, 9.0, hls.PresentationDuration(cuts, 12), 1e-9)
	assert.InDelta(t, 1.0, hls.ItemTime(cuts, 1), 1e-9)
	assert.InDelta(t, 5.0, hls.ItemTime(cuts, 3), 1e-9)  // after the first cut
	assert.InDelta(t, 8.0, hls.ItemTime(cuts, 5), 1e-9)  // after both
	assert.InDelta(t, 2.0, hls.PresentationTime(cuts, 3), 1e-9) // inside a cut
	assert.InDelta(t, 5.0, hls.PresentationTime(cuts, 8), 1e-9)
	for _, p := range []float64{0, 1.5, 2, 3.25, 4, 8.9} {
		assert.InDelta(t, p, hls.PresentationTime(cuts, hls.ItemTime(cuts, p)), 1e-9, "round trip %v", p)
	}
}
```

`assertCuts` compares lengths and each `Start`/`End` with `assert.InDelta(…, 1e-9)`.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed/hls -run 'TestEffectiveCuts|TestTimeMapping'`
Expected: FAIL to compile (`undefined: hls.EffectiveCuts`).

- [ ] **Step 3: Implement `cuts.go`**

Steps, in this order:
1. Sort.
2. Drop any range shorter than `minLen`.
3. Merge ranges whose gap is under `minLen`.
4. Shrink each by `CutMargin`, and drop any that is now empty.
5. Clamp to `[0, duration]`, without shrinking an edge that was clamped at 0 or at the duration.

`TestEffectiveCutsClampsToTheItem` pins step 5.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed/hls`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/hls/cuts.go plugin/feed/hls/cuts_test.go
git commit -m "feed: effective silence cuts and presentation time mapping"
```

---

### Task 3: Cut plans and the edit-list id (pure)

**Files:**
- Create: `plugin/feed/hls/plan.go`
- Test: `plugin/feed/hls/plan_test.go`

**Interfaces:**
- Consumes: `Cut` (Task 2); `Segment`, `Entry`, `Init`, `Playlist` (phase 2)
- Produces:
  ```go
  // Piece is a span of one segment a cut presentation keeps.
  type Piece struct {
      Start, End float64 // item time
      Segment    int     // index into the track's segments
      Edge       bool    // a strict part of its segment: played from an edge file
  }
  // TrackSegments is segments(t) exported: audio fragments all count as keyframes.
  func TrackSegments(t Track) []Segment
  // PlanCuts keeps every part of every segment outside the cuts: a whole
  // segment as it is, a part of one as an edge piece.
  func PlanCuts(t Track, cuts []Cut) []Piece
  // CoarseCuts is the cuts that drop only the segments wholly inside one,
  // merged where dropped segments are adjacent.
  func CoarseCuts(t Track, cuts []Cut) []Cut
  // EditID names a cut presentation: the applied cuts and every track's segment layout.
  func EditID(applied []Cut, tracks ...Track) string
  // CutEntries turns a plan into playlist entries: a kept whole segment is a
  // byte range of fileURI under the track's init; edge piece n is edgeURI(n),
  // a byte range under that file's own init, given by edges[n].
  func CutEntries(t Track, pieces []Piece, fileURI string, edgeURI func(n int) string, edges []Track) []Entry
  // Edges are the pieces of a plan that must be encoded, in order; CutEntries numbers them the same way.
  func Edges(pieces []Piece) []Piece
  ```

- [ ] **Step 1: Write the failing tests**

They use a synthetic track: 9 fragments of 2 s, every one a keyframe, so the segments are `[0,6) [6,12) [12,18)`.

```go
func synthetic() hls.Track // 9 × 2 s audio fragments at offsets 1000+100·i, length 100, InitLength 1000

func TestPlanCutsInsideAndAcross(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{5, 13}})
	assertPieces(t, []hls.Piece{{0, 5, 0, true}, {13, 18, 2, true}}, got)
}

func TestPlanCutsOnBoundaries(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{6, 12}})
	assertPieces(t, []hls.Piece{{0, 6, 0, false}, {12, 18, 2, false}}, got)
}

func TestPlanCutsTwoInOneSegment(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{1, 2}, {3, 4}})
	assertPieces(t, []hls.Piece{{0, 1, 0, true}, {2, 3, 0, true}, {4, 6, 0, true}, {6, 12, 1, false}, {12, 18, 2, false}}, got)
}

func TestPlanCutsAtEnds(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{0, 2}, {16, 18}})
	assertPieces(t, []hls.Piece{{2, 6, 0, true}, {6, 12, 1, false}, {12, 16, 2, true}}, got)
	e := hls.CutEntries(synthetic(), got, "audio.m4a", func(n int) string { return fmt.Sprint("e", n) }, edgeTracks(2))
	assert.False(t, e[0].Discontinuity, "no discontinuity before the first entry")
}

func TestCoarseCuts(t *testing.T) {
	assertCuts(t, []hls.Cut{{6, 12}}, hls.CoarseCuts(synthetic(), []hls.Cut{{5, 13}}))
	assertCuts(t, []hls.Cut{{0, 12}}, hls.CoarseCuts(synthetic(), []hls.Cut{{0, 4}, {4, 12.5}}))
	assertCuts(t, nil, hls.CoarseCuts(synthetic(), []hls.Cut{{1, 5}}))
}

func TestCutEntriesDiscontinuities(t *testing.T) {
	tr := synthetic()
	pieces := hls.PlanCuts(tr, []hls.Cut{{5, 13}})
	e := hls.CutEntries(tr, pieces, "audio.m4a", func(n int) string { return fmt.Sprint("edge-", n) }, edgeTracks(2))
	assert.Equal(t, 2, len(e))
	assert.Equal(t, "edge-0", e[0].URI)
	assert.Equal(t, "edge-0", e[0].Init.URI)
	assert.InDelta(t, 5.0, e[0].Duration, 1e-9)
	assert.True(t, e[1].Discontinuity)
	// A whole segment kept between two others needs no discontinuity.
	e = hls.CutEntries(tr, hls.PlanCuts(tr, nil), "audio.m4a", nil, nil)
	for _, x := range e {
		assert.False(t, x.Discontinuity)
	}
	assert.Equal(t, hls.MediaPlaylist(tr, "audio.m4a"), hls.Playlist(e))
}

func TestEditID(t *testing.T) {
	a := hls.EditID([]hls.Cut{{5, 13}}, synthetic())
	assert.Equal(t, 12, len(a))
	assert.Equal(t, a, hls.EditID([]hls.Cut{{5, 13}}, synthetic()))
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13.5}}, synthetic()))
	other := synthetic()
	other.Fragments = other.Fragments[:8]
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13}}, other))
}
```

`edgeTracks(n)` returns `n` tracks, each with `InitLength` 500 and one fragment at offset 500 of length 50.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed/hls -run 'TestPlanCuts|TestCoarseCuts|TestCutEntries|TestEditID'`
Expected: FAIL to compile (`undefined: hls.PlanCuts`).

- [ ] **Step 3: Implement `plan.go`**

- `PlanCuts` intersects each segment's `[Start, Start+Duration)` with the kept spans (the complement of the cuts). It discards any intersection shorter than 1 ms.
- `CutEntries` gives each edge entry the fragment byte range of `edges[n]`, with `Init{edgeURI(n), edges[n].InitOffset, edges[n].InitLength}`.
- **Discontinuity rule** (Decisions): a discontinuity comes before an entry when its start is not the previous end (within 1 ms), or when its URI's file differs from the previous entry's.
- `EditID` hashes `json.Marshal(struct{Cuts []Cut; Layouts [][]Segment})`, with cut times rounded to the millisecond, and takes the first 12 hex characters.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed/hls`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/hls/plan.go plugin/feed/hls/plan_test.go
git commit -m "feed: cut plans, edge pieces and edit-list ids for skip playlists"
```

---

### Task 4: Signed options

**Files:**
- Modify: `plugin/feed/hls_links.go`, `plugin/feed/hls_serve.go` (parameter check), and every `HLSMasterURL` caller (`rpc_media.go`, `pages.go`)
- Test: `plugin/feed/hls_test.go`, `plugin/feed/hls_links_internal_test.go` (new)

**Interfaces:**
- Produces (roadmap):
  ```go
  type hlsOptions struct{ Skip bool }
  func parseHLSOptions(q url.Values) (hlsOptions, error)  // only sig and skip=silence allowed
  func (o hlsOptions) Values() url.Values                  // without sig; empty for zero options
  func hlsSignature(key []byte, user string, itemID int64, o hlsOptions) string
  func validHLSSignature(key []byte, user string, itemID int64, o hlsOptions, sig string) bool
  func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64, o hlsOptions) (string, error)
  // HLSSkipMasterURL is HLSMasterURL with skip=silence, for phase 6a and tests.
  func (p *Plugin) HLSSkipMasterURL(store *Store, username string, itemID int64) (string, error)
  ```
  Inside the route, the canonical query suffix for child URIs is `"?" + vals.Encode()`, where `vals` is `o.Values()` with `sig` set. The sorted keys put `sig` before `skip`.

- [ ] **Step 1: Write the failing tests**

```go
// hls_links_internal_test.go
func TestHLSOptionsSignature(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	// A phase-1 link: the message is unchanged when there are no options.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("cog-feed-hls\x00alice\x0042"))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), hlsSignature(key, "alice", 42, hlsOptions{}))
	assert.NotEqual(t, hlsSignature(key, "alice", 42, hlsOptions{}), hlsSignature(key, "alice", 42, hlsOptions{Skip: true}))
}

func TestParseHLSOptions(t *testing.T) {
	o, err := parseHLSOptions(url.Values{"sig": {"x"}, "skip": {"silence"}})
	require.NoError(t, err)
	assert.True(t, o.Skip)
	for _, bad := range []url.Values{{"skip": {"pauses"}}, {"skip": {"silence", "silence"}}, {"mute": {"profanity"}}, {"sig": {"a", "b"}}} {
		_, err := parseHLSOptions(bad)
		assert.Error(t, err, "%v", bad)
	}
}

// hls_test.go
func TestHLSSkipNeedsItsOwnSignature(t *testing.T) {
	// alice's plain master URL with &skip=silence appended → 403;
	// HLSSkipMasterURL → 200, and every URI in it carries "&skip=silence".
}
```

Also update the existing `TestHLSRejects` case for an unknown parameter so it still expects 400.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed -run 'TestHLSOptionsSignature|TestParseHLSOptions|TestHLSSkipNeedsItsOwnSignature|TestHLSRejects'`
Expected: FAIL to compile (`undefined: hlsOptions`).

- [ ] **Step 3: Implement**

1. Add the options to the signature.
2. `ServeHLS` calls `parseHLSOptions` in place of its own loop, and answers 400 with the error's text.
3. The signature check passes the parsed options.
4. Every child URI uses the canonical suffix.
5. Existing callers pass `hlsOptions{}`.

The 403 must not reveal whether `skip` was the reason.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed -run 'TestHLS|TestParseHLSOptions|TestResolveMedia|TestStreamURL'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/hls_links.go plugin/feed/hls_links_internal_test.go plugin/feed/hls_serve.go plugin/feed/hls_test.go plugin/feed/rpc_media.go plugin/feed/pages.go
git commit -m "feed: media links sign a skip=silence option"
```

---

### Task 5: The minimum skip length pref

**Files:**
- Modify:
  - `plugin/feed/store.go`: the settings `var` block, the bounds consts, `PlayerPrefs`, and `Load`/`SavePlayerPrefs`
  - `plugin/feed/feedpb/feed.proto`, field `double skip_silence_min_sec = 8;` in `PlayerPrefs`
  - generated `feedpb`
  - `plugin/feed/rpc.go`: `SetPlayerPrefs` validation and `playerPrefsPB`
  - `plugin/feed/pages.go`: the `SavePlayerPrefs` form field `skip_silence_min_sec`, and `Ranges`, which applies `hls.EffectiveCuts` to skip ranges
  - `plugin/feed/pages.templ`: the gear menu
  - `plugin/feed/static/player.js`: the `savePrefs` key map
- Test: `plugin/feed/rpc_test.go` (or wherever `TestSetPlayerPrefs*` lives), `plugin/feed/settings_test.go`, `plugin/feed/pages_player_test.go`

**Interfaces:**
- Produces:
  ```go
  settingSkipSilenceMinSec = FloatSetting("skip_silence_min_sec", 1.0)
  const (MinSkipSilenceSec = 0.5; MaxSkipSilenceSec = 10.0)
  PlayerPrefs.SkipSilenceMinSec float64 `json:"skipSilenceMinSec"`
  ```
- **The edge reset:** saving a different `SkipSilenceMinSec` calls `Store.ResetEdges()` (Task 6) in the same request. Task 6 lands after this one, so this task adds the call in Task 6, Step 3, and pins it here with the test below, which is marked `t.Skip("Task 6")` until then.

- [ ] **Step 1: Write the failing tests**

```go
func TestSetPlayerPrefsSkipSilenceMin(t *testing.T) {
	// SetPlayerPrefs with SkipSilenceMinSec 2.5 → GetPlayerPrefs returns 2.5.
	// 0.4 and 10.5 → CodeInvalidArgument.
	// A default user's GetPlayerPrefs → 1.0.
}

func TestSavePlayerPrefsFormSkipSilenceMin(t *testing.T) {
	// POST player/prefs skip_silence_min_sec=3 → LoadPlayerPrefs().SkipSilenceMinSec == 3;
	// skip_silence_min_sec=99 → clamped to 10, as the other fields clamp.
}

func TestRangesApplyMinSkip(t *testing.T) {
	// silence ranges {1,1.5},{3,6}; pref 1.0 → api/ranges/{id} returns one skip range {3.1, 5.9}.
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed -run 'SkipSilenceMin|TestRangesApplyMinSkip'`
Expected: FAIL to compile (`unknown field SkipSilenceMinSec`).

- [ ] **Step 3: Implement**

1. Add the proto field, then run `go generate ./plugin/feed/feedpb`.
2. Wire the field through store, RPC, form and `Ranges`.
3. Add a `<select data-pref="skip_silence_min_sec">` labelled "Shortest silence skipped", with options 0.5, 1, 2, 3 and 5 seconds, beside the "Skip silence" toggle.
4. In `player.js`, map `skipSilenceMinSec` to `skip_silence_min_sec` and reload ranges after it changes.
5. Run `go tool templ generate -log-level warn` and `go tool templ fmt .`.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed -run 'PlayerPrefs|SkipSilenceMin|TestRanges|TestBrowser/Player'`
Expected: PASS. If a gear-menu snapshot changed, rerun with `WEBTEST_UPDATE=1`, then read the `.html` diff and check that only the new select was added.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/store.go plugin/feed/feedpb plugin/feed/rpc.go plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/static/player.js plugin/feed/*_test.go plugin/feed/testdata/snapshots
git commit -m "feed: a pref for the shortest silence skipped"
```

---

### Task 6: Edge state and edit lists in the store

**Files:**
- Create: `plugin/feed/sql/migration007_skip.sql`
- Modify:
  - `plugin/feed/sql/schema.sql`, regenerated
  - `plugin/feed/store.go`: `Download`, `GetDownload`/`FindDownload` columns, `SetSilenceRanges`, `markReady`, `SavePackage`, `SavePlayerPrefs`
  - `plugin/feed/store_media.go`
- Test: `plugin/feed/store_media_test.go`

**Interfaces:**
- Produces:
  ```sql
  ALTER TABLE downloads ADD COLUMN edges_wanted INTEGER NOT NULL DEFAULT 0;
  ALTER TABLE downloads ADD COLUMN edges_edit TEXT NOT NULL DEFAULT '';   -- edit id whose edges exist; '' = none
  ALTER TABLE downloads ADD COLUMN edges_error TEXT NOT NULL DEFAULT '';
  CREATE TABLE hls_edits (id TEXT NOT NULL, item_id INTEGER NOT NULL, cuts TEXT NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY (item_id, id));
  ```
  ```go
  Download.EdgesWanted bool; Download.EdgesEdit, Download.EdgesError string
  func (s *Store) WantEdges(itemIDs ...int64) error          // sets edges_wanted=1; returns whether any row changed via (bool, error)
  func (s *Store) PendingEdges(limit int) ([]int64, error)   // wanted, packaged, silence done, edges_edit='' and edges_error=''
  func (s *Store) SetEdges(itemID int64, edit string) error  // edit "-" = nothing to encode
  func (s *Store) MarkEdgesError(itemID int64, msg string) error
  func (s *Store) ResetEdges(itemIDs ...int64) error         // none given: every item; clears edges_edit and edges_error
  func (s *Store) RecordEdit(itemID int64, edit string, applied []hls.Cut) error // INSERT OR IGNORE
  func (s *Store) EditCuts(itemID int64, edit string) ([]hls.Cut, error)        // ErrEditNotFound when unknown
  var ErrEditNotFound = errors.New("feed: unknown edit list")
  ```
  The final signature of `WantEdges` is `(bool, error)`. The route uses the bool to call `TriggerFetch` only when something changed.

- [ ] **Step 1: Write the failing tests**

```go
func TestEdgesPending(t *testing.T) {
	// A packaged item with silence done is not pending until WantEdges;
	// then it is; SetEdges(id,"abc") → not pending; SetSilenceRanges → pending again (edges_edit cleared).
}
func TestEdgesResetOnRepackage(t *testing.T)   // SavePackage clears edges_edit
func TestEdgesResetOnPrefChange(t *testing.T)  // SavePlayerPrefs with a new SkipSilenceMinSec clears every edges_edit; the same value does not
func TestEdgesErrorNotRetried(t *testing.T)    // MarkEdgesError → not pending; ResetEdges(id) → pending
func TestWantEdgesReportsChange(t *testing.T)  // first call true, second false
func TestRecordEdit(t *testing.T) {
	// RecordEdit twice with the same id keeps the first cuts; EditCuts round-trips;
	// an unknown id → errors.Is(err, feed.ErrEditNotFound).
}
```

Remove the `t.Skip` from Task 5's reset test.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed -run 'TestEdges|TestWantEdges|TestRecordEdit|PrefChange'`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

1. Write the migration, then run `go generate ./plugin/feed` and check that `schema.sql` gained exactly the new columns and table.
2. Add the store methods.
3. Clear `edges_edit` and `edges_error` in `SetSilenceRanges`, `markReady` and `SavePackage`.
4. Make `SavePlayerPrefs` call `ResetEdges()` when the value changed. It reads the old value first, through `settingSkipSilenceMinSec.Get`.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/sql plugin/feed/store.go plugin/feed/store_media.go plugin/feed/*_test.go
git commit -m "feed: record wanted edges and served edit lists"
```

---

### Task 7: The edges worker

**Files:**
- Create: `plugin/feed/pipeline/edges.go`
- Modify: `plugin/feed/orchestrator/orchestrator.go` (after Silence), `plugin/feed/pipeline/collect.go`, `plugin/feed/space.go` (a helper, if one is needed)
- Test: `plugin/feed/pipeline/edges_test.go`, `plugin/feed/pipeline/edges_internal_test.go`

**Interfaces:**
- Consumes: `hls.EffectiveCuts`, `PlanCuts`, `Edges`, `EditID`, `TrackSegments` (Tasks 2–3); the store methods from Task 6; `Store.LoadPlayerPrefs().SkipSilenceMinSec`; `GetSilenceRanges`.
- Produces:
  ```go
  type Edges struct{ store *feed.Store; space feed.Space }
  func NewEdges(s *feed.Store, space feed.Space) *Edges
  func (w *Edges) RunOnce(ctx context.Context) error
  // EdgePlan is what a presentation's skip needs, shared by the worker and the route.
  // Audio pieces always; Video pieces only when the video is avc1; VideoUnsupported otherwise.
  // Lives in package feed (plugin/feed/hls_skip.go) so ServeHLS uses the same computation.
  type EdgePlan struct {
      Edit             string
      Cuts             []hls.Cut
      Audio, Video     []hls.Piece
      VideoUnsupported bool
  }
  func SkipPlan(store *Store, d Download, audio hls.Track, video *hls.Track) (EdgePlan, error) // in package feed
  func EdgeName(item int64, edit, track string, n int) string // "edges/<item>/<edit>/<track>-<n>.mp4", track "a" or "v"
  // in pipeline:
  func edgeArgs(in, out string, piece hls.Piece, kind hls.Kind, codecs string) ([]string, error)
  func x264Profile(codecs string) (profile, level string, ok bool) // avc1.42xxyy → baseline; 4D → main; 64 → high; level yy/10 as "3.0"
  ```
  - **Audio args:** `-nostdin -y -v error -ss S -to E` + `ffargs.Input(in)` + `-map 0:a:0 -c:a aac -b:a 128k -output_ts_offset S -movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4 out`
  - **Video args:** `-nostdin -y -v error -ss S -to E` + `ffargs.Input(in)` + `-map 0:V:0 -an -c:v libx264 -preset veryfast -crf 20 -profile:v P -level L -pix_fmt yuv420p -output_ts_offset S -movflags +frag_keyframe+empty_moov+default_base_moof+global_sidx -f mp4 out`
  - **Output:** to `space.Cache` `edges/<item>/<edit>/`, through `Cache.LocalPath`.
  - **Afterwards:** each output is checked with `hls.ReadTrack`. Then `SetEdges(item, edit)`, or `"-"` when the plan has no edge pieces, and `RemoveAll` on each sibling edit directory.
  - **On error:** `MarkEdgesError`, and the partial directory is removed.

- [ ] **Step 1: Write the failing tests**

```go
// edges_internal_test.go
func TestX264Profile(t *testing.T) {
	for codecs, want := range map[string][2]string{"avc1.42C01E": {"baseline", "3.0"}, "avc1.4D401F": {"main", "3.1"}, "avc1.640028": {"high", "4.0"}} {
		p, l, ok := x264Profile(codecs)
		assert.True(t, ok); assert.Equal(t, want[0], p); assert.Equal(t, want[1], l)
	}
	_, _, ok := x264Profile("vp09.00.40.08")
	assert.False(t, ok)
}
func TestEdgeArgsAudio(t *testing.T) // contains "-ss","2.600","-to","6.000", "-output_ts_offset","2.600", "-c:a","aac"; no "-c:v"
func TestEdgeArgsVideo(t *testing.T) // "-profile:v","baseline","-level","3.0", "-map","0:V:0", frag_keyframe in movflags

// edges_test.go (needFFmpeg)
func TestEdgesMakesExactPieces(t *testing.T) {
	// The packaged video item (video.mp4 + audio.m4a fixtures), silence {2.5,4.2},{7.0,9.6}, pref 1.0, WantEdges.
	// RunOnce → GetDownload().EdgesEdit == SkipPlan(...).Edit;
	// 4 audio edges and 4 video edges exist in the cache; each passes hls.ReadTrack;
	// audio edge 0 spans 0–2.6 s: first fragment Start ≈ 0 and total duration ≈ 2.6 (±0.05);
	// video edge 1 spans 4.1–6.0: first fragment Start ≈ 4.1 (±0.05), Keyframe true.
}
func TestEdgesNothingToCut(t *testing.T)      // no silence → EdgesEdit "-", no edges/ dir
func TestEdgesRemovesStale(t *testing.T)      // run, change pref (ResetEdges), run → only the new edit dir under edges/<item>/
func TestEdgesNonAVCVideo(t *testing.T)       // video Codecs not avc1 (patch the fixture as TestReadTrackOtherVideoCodec does) → only audio edges made; EdgesEdit set
func TestEdgesFailureNotRetried(t *testing.T) // audio track removed from storage → EdgesError set; second RunOnce does not try again
func TestCollectRemovesEdgesOfGoneItems(t *testing.T) // edges/<id>/ of an item whose download was deleted is removed by Collect; a ready item's stays
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed/pipeline -run 'TestX264|TestEdge|TestCollectRemovesEdges'`
Expected: FAIL to compile (`undefined: NewEdges`).

- [ ] **Step 3: Implement**

1. Write `SkipPlan` in `plugin/feed/hls_skip.go`:
   1. Get the ranges with `GetSilenceRanges`.
   2. Turn them into cuts with `EffectiveCuts(…, prefs.SkipSilenceMinSec, duration)`, where `duration` is the sum of the audio fragment durations.
   3. Plan each track with `PlanCuts`.
   4. Compute `EditID(cuts, audio, video…)`.
   5. Video counts as avc1 when `strings.HasPrefix(Codecs, "avc1.")` or `"avc3."`.
2. Write the worker.
3. In `Collect`, list `edges/` with `space.Cache.ReadDir` and `RemoveAll` every directory whose item id has no ready download (use `store.DownloadFiles` or a new `ReadyDownloadIDs`). A cache with no `edges/` directory is not an error.
4. Wire `NewEdges(store, space).RunOnce` after Silence in `orchestrator.processUser`, logging as its neighbours do.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed/...`
Expected: PASS, with no ffmpeg tests skipped (check `-v` for `SKIP` lines).

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/pipeline plugin/feed/hls_skip.go plugin/feed/orchestrator/orchestrator.go plugin/feed/space.go
git commit -m "feed: encode the edge segments of skip presentations"
```

---

### Task 8: Skip playlists and edge files

**Files:**
- Modify: `plugin/feed/hls_serve.go`, `plugin/feed/hls_skip.go`
- Test: `plugin/feed/hls_skip_test.go` (new)

**Interfaces:**
- Consumes: Tasks 3, 4, 6 and 7. Phase 2's subtitle cases (Task 1 ledger: `mpegts` and the `subs-<n>.vtt` index). Phase 3's `clean.m3u8`.
- Produces: behaviour, when `o.Skip`:
  - **`master.m3u8`:**
    1. Compute `SkipPlan`.
    2. Choose the **full** plan when `d.EdgesEdit == plan.Edit` and every edge file is in the cache (`Cache.Open` and `Stat`, closing it and logging any Close error).
    3. Otherwise use **coarse** for audio-only (`CoarseCuts` applied through `PlanCuts`, which then has no edges, with `EditID(coarse, audio)`), or **pending** for video: unskipped, with `SessionData {"us.duckfam.cog.skip","pending"}`.
    4. When not full, `WantEdges(item)`, and `TriggerFetch` when it changed.
    5. With cuts applied: `RecordEdit(item, edit, applied)` and `SessionData {"us.duckfam.cog.edits", edit}`.
    6. For unsupported video, `{"us.duckfam.cog.skip","unsupported"}` with no cuts.
    7. Rendition URIs carry the canonical suffix. **The edit id is also passed as `edit=<id>` in child URIs.** That is an unsigned parameter `parseHLSOptions` accepts, used only to pick the same plan again: a media playlist or edge request whose `edit` no longer matches is answered 404 (decision for Review Focus 4).
  - **`audio.m3u8`, `clean.m3u8`, `video.m3u8`:** `hls.Playlist(hls.CutEntries(...))`, with the plan the `edit` parameter names. `edgeURI(n)` is `edge-<edit>-<a|v>-<n>.mp4` with the suffix. The clean rendition uses the audio plan against the clean track; its edges are the clean track's own `edges/<item>/<edit>/c-<n>.mp4`, made by Task 7's worker when `CleanTrackPath != ""`.
  - **`subs.m3u8`:** one VTT entry per audio piece, URI `subs-<n>.vtt`, with the same discontinuities. **`subs-<n>.vtt`:** `hls.VTTSegment(cues, piece.Start, piece.End, mpegts)`, masked when the presentation is clean (phase 3).
  - **`edge-<edit>-<t>-<n>.mp4`:** served from `space.Cache` with `ServeContent`, `video/mp4` or `audio/mp4`, and `private, max-age=86400`. A 404 when the edit is not `d.EdgesEdit`.
- **Ruling recorded here:** `edit` is accepted unsigned. It cannot widen the grant, because it only selects among plans the signed options already allow. **Cost if wrong:** a forged `edit` gets a 404.
  - This changes Task 4's `parseHLSOptions` to also allow `edit=<12 hex>`, which is excluded from the signature. Add the case `{"edit": {"zz"}}` → error to `TestParseHLSOptions`.

- [ ] **Step 1: Write the failing tests**

Shared setup: alice's `packagedItem(withVideo)`, silence `{2.5,4.2},{7.0,9.6}`, pref 1.0, and `skip := HLSSkipMasterURL`.

```go
func TestHLSSkipVideoPendingThenFull(t *testing.T) {
	// GET skip master (no edges yet) → 200, contains `DATA-ID="us.duckfam.cog.skip",VALUE="pending"`, no EXT-X-DISCONTINUITY in video.m3u8;
	// the download now has EdgesWanted.
	// Run pipeline.NewEdges(...).RunOnce (needFFmpeg).
	// GET skip master again → contains `DATA-ID="us.duckfam.cog.edits",VALUE="<plan.Edit>"`;
	// video.m3u8 and audio.m3u8 each have 4 entries, 3 EXT-X-DISCONTINUITY, EXT-X-MAP lines naming edge files;
	// the sum of EXTINF in each is 12 − 3.9 = 8.1 (±0.01);
	// every edge URI → 206 for a Range request, Content-Type video/mp4 or audio/mp4.
}
func TestHLSSkipAudioCoarse(t *testing.T) {
	// audio-only item, silence {5.5,13} on a 12 s item: coarse drops only whole segments;
	// without edges, audio.m3u8's EXTINF sum equals 12 − the coarse cut, and no edge URIs.
}
func TestHLSSkipNoRanges(t *testing.T) // silence done with none → audio.m3u8 has no DISCONTINUITY and the same EXTINF/BYTERANGE lines as the unskipped one
func TestHLSSkipSilencePending(t *testing.T) // silence_state '' → served unskipped, no edits SESSION-DATA
func TestHLSSkipAfterPrefChange(t *testing.T) {
	// full edges made; then the pref changes to 3.0 → master is pending again (video),
	// and an old video.m3u8 URL (old edit=) → 404; an old edge URL → 404.
}
func TestHLSSkipSubtitles(t *testing.T) // item with captions: subs.m3u8 has as many entries as audio.m3u8, with the same DISCONTINUITY positions; subs-0.vtt holds no cue that starts inside a cut
func TestHLSSkipRecordsEdit(t *testing.T) // after a full master, store.EditCuts(item, edit) returns the cuts {2.6,4.1},{7.1,9.5}
func TestHLSSkipUnsupportedVideo(t *testing.T) // video track patched to vp09 → `VALUE="unsupported"`, unskipped
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed -run TestHLSSkip`
Expected: FAIL. The masters carry no SESSION-DATA and the edge routes are 404.

- [ ] **Step 3: Implement**

Extend `ServeHLS`:
- A presentation-building helper, `skipPresentation(...)`, returns the chosen plan for both master and child requests.
- The edge-file case.
- The `subs.m3u8` and `subs-<n>.vtt` cases under skip.

Keep the request free of ffmpeg and free of writes, except `WantEdges` and `RecordEdit` on master requests.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed -run 'TestHLS'`
Expected: PASS. Phase 1–3 `TestHLS*` tests are unchanged and still pass.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/hls_serve.go plugin/feed/hls_skip.go plugin/feed/hls_links.go plugin/feed/hls_skip_test.go plugin/feed/hls_links_internal_test.go
git commit -m "feed: skip=silence presentations with edge segments and discontinuities"
```

---

### Task 9: ffmpeg plays a skip presentation end to end

**Files:**
- Test: `plugin/feed/hls_skip_play_test.go` (new)

**Interfaces:**
- Consumes: Task 8 over a real HTTP listener. `httptest.NewServer(env.Handler)`; check `cogtest` for an existing listener helper first.

- [ ] **Step 1: Write the test**

```go
// TestHLSSkipPlaysInFFmpeg reads a skip presentation as mpv does (ffmpeg's
// HLS demuxer) and checks its audio and video run as long as the item less
// its cuts.
func TestHLSSkipPlaysInFFmpeg(t *testing.T) {
	// needFFmpeg; setup as Task 8 with edges made (RunOnce).
	// ffmpeg -nostdin -v error -protocol_whitelist http,tcp,hls,file,crypto -i <server>+skipMaster
	//   -map 0:a:0 -ac 1 -ar 8000 -f s16le pipe:1      → audio seconds = len(stdout)/16000
	// ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames -of csv=p=0 <same URL>
	//                                                  → video seconds = frames/24
	assert.InDelta(t, 8.1, audioSecs, 0.1)
	assert.InDelta(t, 8.1, videoSecs, 0.1)
}
```

ffmpeg runs directly with `exec.CommandContext`, not through the sandbox: it is the test's client, standing in for mpv.

- [ ] **Step 2: Run it**

Run: `go test ./plugin/feed -run TestHLSSkipPlaysInFFmpeg -v`
Expected: PASS. If it fails on a discontinuity or timestamp problem, use superpowers:systematic-debugging. **Do not widen the delta.** Ledger what ffmpeg did, and fix `CutEntries` or `edgeArgs`.

- [ ] **Step 3: Commit**

```bash
git add plugin/feed/hls_skip_play_test.go
git commit -m "feed: test that ffmpeg plays a skip presentation at its cut length"
```

---

### Task 10: Progress in presentation time

**Files:**
- Modify: `plugin/feed/feedpb/feed.proto` (`ReportProgressRequest`: `string edit_list = 6;`), generated `feedpb`, `plugin/feed/rpc.go` (`ReportProgress`)
- Test: `plugin/feed/rpc_test.go` (near the existing `TestReportProgress*`)

**Interfaces:**
- Consumes: `Store.EditCuts`, `hls.ItemTime`
- Produces: when `edit_list` is set, `progress_sec` is taken as presentation time and stored as `hls.ItemTime(cuts, progress_sec)`. An unknown edit list is `CodeInvalidArgument` "unknown edit list". With `edit_list` empty, behaviour is unchanged.

- [ ] **Step 1: Write the failing tests**

```go
func TestReportProgressEditList(t *testing.T) {
	// RecordEdit(item, "abc123abc123", {{2,4},{6,7}}); ReportProgress{ItemId, ProgressSec: 5, EditList: "abc123abc123"}
	// → returned Playback.ProgressSec == 8 (±1e-9), and GetPlayback agrees.
}
func TestReportProgressUnknownEditList(t *testing.T) // EditList "000000000000" → CodeInvalidArgument
func TestReportProgressNoEditList(t *testing.T)      // ProgressSec 5 stored as 5 (the existing behaviour, pinned)
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./plugin/feed -run TestReportProgress`
Expected: FAIL to compile (`unknown field EditList`).

- [ ] **Step 3: Implement**

1. Add the proto field and run `go generate ./plugin/feed/feedpb`.
2. Map the time in `ReportProgress` before `SetPlayback`. `getItem` already checks that the item exists.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./plugin/feed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/feedpb plugin/feed/rpc.go plugin/feed/rpc_test.go
git commit -m "feed: progress reported in a skip presentation's time maps back to the item"
```

---

### Task 11: Design and TODO

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`, `TODO.md`

- [ ] **Step 1: Update the design**

1. Status: "phases 1–4 done (<date>)".
2. In § Skip, record this plan's Decisions, each with its why:
   - edges on demand
   - video waits for its edges
   - item-time timestamps
   - the discontinuity rule
   - applied cuts as the edit id's input
   - the web ranges using the same effective cuts
   - the unsigned `edit` selector
   - the 0.1 s margin
   - stale edges removed per item, and by Collect
3. In § Timeline, record `ReportProgressRequest.edit_list`.
4. Move to Open questions:
   - Whether mpv's position display follows the item's timestamps or the presentation's, which decides whether an mpv script needs the mapping at all.
   - VAAPI for video edges.
   - Whether YouTube's 10 s audio fragments make audio edges too long to encode on demand. They don't, but measure.

- [ ] **Step 2: Tick Phase 4 in `TODO.md`**, naming the tests: `TestHLSSkip*`, `TestEdges*` and `TestHLSSkipPlaysInFFmpeg`.

- [ ] **Step 3: Run the whole suite**

Run: `go tool verify`
Expected: everything passes. If verify's `go fix` rewrites files this branch never touched, revert them and ledger it, as phase 1 did.

- [ ] **Step 4: Commit**

```bash
git add DESIGN/plugins/feed_hls.md TODO.md
git commit -m "feed: HLS phase 4 in the design and TODO"
```
