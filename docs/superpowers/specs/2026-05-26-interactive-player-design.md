# Interactive Media Player — Design

**Date:** 2026-05-26
**Status:** Approved (design)
**Scope:** `plugin/feed` — the audio/video player in the item viewer.

## Problem

The current feed player (`feedPlayer` / `feedPlayerScript` in `pages.templ`) is a
minimal stub: native controls are hidden, leaving only play/pause, fixed ±15/30
skip buttons, and a `current / duration` text span. There is **no scrubber** — the
user cannot see or set position on a timeline. It also lacks keyboard control,
touch affordances, playback-speed control, persistent preferences, silence
skipping, and auto-advance.

This design replaces the stub with a full custom player chrome (YouTube-style),
built in vanilla JS to fit the templ/HTMX codebase (no JS build pipeline).

## Goals

- Custom control bar with a real, draggable **scrubber** (the headline feature).
- Click-to-play/pause on the video surface; auto-hiding controls.
- Keyboard controls (YouTube-style).
- Touch controls (tap, double-tap-seek, drag).
- Playback **speed** control.
- **Skip-silence** toggle (trim quiet gaps).
- **Auto-advance** to the next unplayed item on end.
- **MediaSession** integration: device OS media controls (lock screen,
  notification, hardware/Bluetooth media keys) with metadata + artwork.
- Per-user persisted preferences.

## Non-Goals

- No JS player library (Plyr/Vidstack/video.js) — wrong fit for the codebase.
- No per-subscription preference overrides — global per-user defaults only.
- No new file/data migration — greenfield additions only.
- No persistent/cross-navigation mini-player — per-page player only.
- **Follow-ups, separate specs:**
  - Clickable transcript / caption subtitle track (reuses existing VTT).
  - Volume normalization via `ffmpeg loudnorm` — designed as a **filter
    action** (alongside `mute_profanity` / `download` / `transcribe`), applied
    at ingest, not as a player toggle.

## Architecture Decision

**Vanilla JS, single shared player module.** Extract the player logic out of the
inline `<script>` in `pages.templ` into one embedded static asset
(`plugin/feed/static/player.js`) served via a plugin route (e.g.
`GET /apps/feed/static/player.js`), with a far-future cache header and a
content-hash or build-version query string for cache busting. The templ
`feedPlayer` template renders only markup + a small config blob (data
attributes); all behavior lives in the testable static file. This keeps a large
script out of generated templ Go and gives one place to reason about player
state.

Rationale for vanilla over a library: the repo has no JS bundler; the
range-based mute/skip behavior (below) is bespoke and awkward to bolt onto a
third-party player; full control over the scrubber and overlays is required.

## Section 1 — Player chrome & layout

A single custom control bar, YouTube-style. For video it overlays a bottom
gradient on the surface; for audio it is a plain horizontal bar (no surface).

**Scrubber** (full bar width, top of the control bar):
- Thin when idle, thickens on hover.
- Played portion in the accent color; buffered portion lighter; draggable handle
  appears on hover.
- Click or drag to seek; hover shows a time tooltip at the cursor position.
- **Range markers**: mute ranges (profanity) and skip ranges (silence) drawn as
  faint tick/segment overlays on the track (see Section 4).

**Buttons row** (below scrubber):
- Left: play/pause, back (skip-back interval), forward (skip-forward interval),
  `current / duration` time (tabular numerals).
- Right: speed (e.g. `1.0×`, opens a small menu), volume + mute, gear menu,
  fullscreen.

**Center overlay** (video only): large play/pause button on the surface;
clicking the surface toggles play/pause. During playback the controls auto-hide
after ~2.5s of pointer inactivity and reappear on movement/tap.

Audio player is the same control bar minus the video surface and center overlay.

## Section 2 — Keyboard & touch

Keyboard bindings active when the player has focus and no text input is focused
(guard against typing in search/forms):

| Key | Action |
|-----|--------|
| `space` / `k` | play / pause |
| `←` / `→` | seek ∓5s |
| `j` / `l` | seek ∓10s |
| `↑` / `↓` | volume ±5% |
| `m` | mute toggle |
| `f` | fullscreen toggle |
| `0`–`9` | jump to 0%–90% of duration |
| `<` / `>` (or `,` / `.`) | speed down / up |

Touch:
- Tap center: play / pause.
- Double-tap left third / right third: seek ∓10s, with a brief ripple feedback.
- Drag the scrubber to seek.
- Tap shows controls; they auto-hide as above.

## Section 3 — Settings & persistence

Per-user defaults stored in the existing `feed_settings` table via the typed
`Setting[T]` infrastructure in `store.go`:

| Setting key | Type | Default |
|-------------|------|---------|
| `playback_speed` | float (0.5–3.0) | 1.0 |
| `skip_back_sec` | int | 15 |
| `skip_fwd_sec` | int | 30 |
| `volume` | float (0–1) | 1.0 |
| `autoplay_next` | bool | true |
| `skip_silence` | bool | false |

The gear menu edits speed, skip-silence, and autoplay live. A change persists via
a small `POST /apps/feed/player/prefs` (form-encoded, CSRF-protected) so the
preference carries to the next item and across sessions. The player reads these
defaults from the config blob rendered into `feedPlayer`.

Resume position is unchanged: it continues to use the existing per-item
`playback.progress_sec` mechanism (`POST items/{id}/progress`), and
play-complete continues to use `POST items/{id}/complete`.

## Section 4 — Skip-silence & auto-advance (unified range model)

### Unified ranges

Profanity-mute and silence-skip are different *detectors* but converge on one
client mechanism.

- **Profanity** is text-based: `ParseVTT` → `profanity.AudioRanges` over the
  item's captions; opt-in per subscription via the `mute_profanity` filter
  action. Already implemented.
- **Silence** is waveform-based: `ffmpeg silencedetect` decodes the media and
  reports quiet gaps; universal, gated only by the `skip_silence` player toggle.

Detection stays independent, but results are served as one list. Define:

```
Range { Start float64, End float64, Action string }   // Action: "mute" | "skip"
```

A single endpoint `GET /apps/feed/api/ranges/{id}` returns the merged list:
- `mute` ranges from the existing profanity path (when captions + profanity
  plugin are present), replacing today's `api/clean-audio/{id}` shape.
- `skip` ranges from cached silence detection (only computed/returned when
  relevant; the client requests them and applies them only if `skip_silence` is
  on).

The client runs one loop on `timeupdate`:
- Inside a `mute` range → drop volume to 0 (restore after) — current behavior.
- Inside a `skip` range → seek to `End`.

Both range kinds render as markers on the scrubber (distinct styles).

`api/clean-audio/{id}` is removed and its single caller updated to the new
endpoint (greenfield, no back-compat needed).

### Silence detection & caching

- Computed lazily on first request for an item, via `ffmpeg silencedetect`
  (ffmpeg is already a dependency, used by the profanity TTS path). Parse the
  `silence_start` / `silence_end` lines from stderr into ranges.
- Cache the parsed ranges so detection runs once per item. Storage: a
  `silence_ranges` TEXT (JSON) column on the `downloads` table (added with
  `ALTER TABLE ... ADD COLUMN`, guarded for idempotency consistent with the
  schema's `IF NOT EXISTS` style), plus a `needs_silence`-style ready flag if a
  three-state (unknown / computing / done) is needed to avoid recompute on
  empty results. Detection requires a local media file (`downloads.local_path`);
  for not-yet-downloaded or remote-only items, skip-silence is unavailable and
  the toggle is a no-op for that item.
- **Opportunistic shared decode**: when the profanity pipeline is already
  decoding a file (`CleanAudioFile` / `muteSegments`), add `silencedetect` to the
  same `ffmpeg filter_complex`/invocation so the large media file is decoded
  once rather than twice. When profanity is not running for an item,
  silencedetect runs standalone on demand. The two detectors must not be coupled
  such that silence only runs when profanity does.

### Auto-advance

On the media element's `ended` event:
1. Mark complete via the existing `POST items/{id}/complete`.
2. If `autoplay_next` is on, fetch the next unplayed item in the current view or
   playlist (reuse `Store.NextUnplayedMedia`) and load it into the player
   in place — an HTMX swap of `#feed-player` (`hx-swap=outerHTML`) re-rendering
   `feedPlayer` for the next item, preserving the control bar.
3. Show a brief "Up next: <title>" affordance with a cancel option before/while
   advancing.

If there is no next item, stop on the completed item (current behavior).

## Section 5 — MediaSession (device OS controls)

Client-side `navigator.mediaSession` integration so the device's OS surfaces the
playing item: lock screen / notification controls, hardware and Bluetooth/headset
media keys. Entirely in `player.js`; no server OS involvement.

- **Metadata** (`navigator.mediaSession.metadata = new MediaMetadata(...)`):
  `title` = item title, `artist` = subscription title, `artwork` = item image /
  subscription icon (reuse the same URL the row icons use; provide it in the
  config blob). Updated on each item load, including after auto-advance.
- **Action handlers** (`setActionHandler`):
  - `play` / `pause` → element play/pause (mirror the existing toggle).
  - `seekbackward` / `seekforward` → use the configured skip intervals
    (`skip_back_sec` / `skip_fwd_sec`).
  - `seekto` → seek to `details.seekTime` (scrubbing from the OS UI).
  - `nexttrack` → reuse the auto-advance path (load next unplayed item).
  - `previoustrack` → restart current item, or seek to 0 (no history queue in v1).
- **Position state** (`setPositionState({ duration, position, playbackRate })`)
  updated on `timeupdate`/seek so the OS scrubber and elapsed time stay accurate.
- Guard all calls behind an `if ('mediaSession' in navigator)` feature check;
  degrade silently where unsupported.

## Data flow

```
feedPlayer (templ)               player.js (static)
  markup + config blob   ──────▶  reads data-* config
  #feed-player                     binds controls, keyboard, touch
                                   on load: GET api/ranges/{id}
                                   timeupdate: mute|skip ranges,
                                               POST items/{id}/progress (15s)
                                   gear change: POST player/prefs
                                   ended: POST items/{id}/complete
                                          + (autoplay) HTMX swap to next
```

Server endpoints (feed plugin):
- `GET  /apps/feed/static/player.js` — cached static module (new).
- `GET  /apps/feed/api/ranges/{id}` — unified mute+skip ranges (replaces
  `api/clean-audio/{id}`).
- `POST /apps/feed/player/prefs` — persist per-user player prefs (new).
- existing: `api/stream-url/{id}`, `items/{id}/media`, `items/{id}/progress`,
  `items/{id}/complete`.

## Error handling

- Per project rule, no swallowed errors: server handlers wrap+return or log via
  `slog`; never silently `_`.
- `ffmpeg silencedetect` failure → log, cache an empty/"done" result so it isn't
  retried in a tight loop, and serve zero skip ranges (player still works).
- Ranges endpoint failure on the client → player proceeds without ranges (degrade
  gracefully); log to console.
- Prefs POST failure → keep the in-session value, log; do not block playback.
- Missing local file for silence → return no skip ranges; toggle is a no-op.

## Testing

- **store**: silence-range get/set round-trip on `downloads`; prefs `Setting[T]`
  round-trip and defaults.
- **silence parser**: unit test parsing representative `ffmpeg silencedetect`
  stderr into `[]Range` (table-driven; include no-silence and malformed lines).
- **ranges endpoint**: returns merged mute+skip; mute-only when no local file;
  empty when neither applies. Use `cogtest` server + `env` helpers.
- **prefs endpoint**: POST persists and is reflected in the rendered config blob.
- **player.js**: keep it pure-function-friendly where possible (e.g. a
  `rangeAt(t, ranges)` helper, time formatting) so logic is unit-testable; DOM
  wiring verified via the existing browser snapshot test
  (`TestBrowserMin`, update snapshots with `WEBTEST_UPDATE=1`).
- Full `go tool verify` green.

## Open implementation notes

- Keep `feedPlayer` markup minimal; all behavior in `player.js`. Pass config
  (item id, type, source kind, resume sec, prefs, csrf token, title,
  subscription title, artwork URL) via `data-*`.
- `<` / `>` speed step and `,` / `.` may both map to speed; pick one canonical
  pair and document in the gear menu tooltip.
- Audio vs video share the module; branch only on presence of the surface.
