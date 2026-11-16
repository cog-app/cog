# Feed HLS, Phases 2–6: Roadmap

**Spec:** `DESIGN/plugins/feed_hls.md`. Phase 1 is done on branch `feed-hls` (`docs/superpowers/plans/2026-11-15-feed-hls-phase1.md`).

This file fixes what the phase plans share: the order, the decisions each phase relies on, and the interfaces one phase produces and a later one consumes. The phase plans were written on 2026-11-15, before phases 2–5 exist. Each one starts with a task that compares its **Consumes** blocks with the code as it stands and records every mismatch as a ruling before any code is written. **If a plan disagrees with this file, this file is the authority. If this file disagrees with the spec, the spec is.**

| Phase | Plan | Repo / branch | Depends on |
|---|---|---|---|
| 2 Captions | `2026-11-15-feed-hls-phase2-captions.md` | cog, `feed-hls` | 1 |
| 3 Mute | `2026-11-15-feed-hls-phase3-mute.md` | cog, `feed-hls` | 2 |
| 4 Skip | `2026-11-15-feed-hls-phase4-skip.md` | cog, `feed-hls` | 3 |
| 5 Remote YouTube | `2026-11-15-feed-hls-phase5-remote.md` | cog, `feed-hls` | 1 (2–4 if landed) and the off-Wi-Fi test |
| 6a M3U for external players | `2026-11-15-feed-hls-phase6a-m3u.md` | cog, `feed-hls` | 4 |
| 6b Android on Media3 HLS | `2026-11-15-feed-hls-phase6b-android.md` | outer repo `cog-feed-sngl` | 3 merged into the outer repo's `cog/` |

Phases 2–4 touch the same files (`hls/playlist.go`, `hls_serve.go`) and run strictly in order. Phase 5 is independent of 2–4 except where it rebases onto them. 6a needs 4's options, and 6b needs 3 on the server.

## Decisions (each plan records its own in the design doc as its last task)

1. **Clean audio is a whole track, not spliced segments (phase 3). This departs from the spec's "small file of re-encoded segments", for these reasons:**
   - The profanity worker already re-encodes the whole audio (`volume=0` per range, or TTS mixed in through `amix`). Audio encoding takes seconds per hour.
   - Splicing re-encoded AAC into copied AAC brings encoder priming and `tfdt` alignment problems at every seam. A whole track has no seams.
   - A whole track carries TTS replacements as they are.

   So the clean audio becomes a second fMP4 audio track with a `sidx`, `media/<id>.hls-audio.clean.m4a`. The master lists it as a second `EXT-X-MEDIA TYPE=AUDIO` rendition. **Cost if wrong:** about 60 MB per hour per cleaned item, and the clean file gets rebuilt whenever the word list changes (it already is).
2. **No `mute` query option (phase 3).** No user pref for clean audio exists. Having a clean track is the switch: the `mute_profanity` filter action makes one. When a clean track exists, it is the `DEFAULT=YES` rendition and the captions are masked (`profanity.FilterText`); the original stays listed as a second rendition. This is how things work today, where clean audio always wins. **Cost if wrong:** one option to add later.
3. **`skip=silence` is the only option (phase 4).** It is signed:

   `message = "cog-feed-hls\x00" + user + "\x00" + item [+ "\x00" + canonical]`

   `canonical` is `url.Values.Encode()` of the options with `sig` removed, so keys are sorted. With no options the message is exactly phase 1's, so existing links keep working.
4. **Skip cuts come from ranges that pass a minimum length.**
   - A range is dropped when it is shorter than the pref `skip_silence_min_sec` (`FloatSetting`, default 1.0, bounds 0.5–10).
   - Ranges whose gap is shorter than that minimum are merged.
   - Each surviving range is shrunk by 0.1 s at both ends, so speech is never clipped.
5. **Edge segments are made by a pipeline worker (`pipeline.Edges`), never in a request, and only for items someone asked to skip.** A master request with `skip=silence`, or an M3U listing it (6a), calls `Store.WantEdges`, which sets `downloads.edges_wanted` and wakes the worker. Until the edges exist:
   - Audio-only items get a **coarse** cut, which drops only the segments wholly inside a range.
   - **Video items play unskipped**, marked with `us.duckfam.cog.skip`=`pending`. A coarse cut lands on different boundaries in audio and video, which would put them out of sync.

   The **edit-list id** is the first 12 hex characters of SHA-256 over the canonical JSON of the cuts and the segment layout. Edges are stored in the cache under `edges/<item>/<edit>/`, and a new edit id removes the old directory.

   The master pins its plan for the playlists and files it links to with an **unsigned `edit=<id>`** query parameter. A stale or forged value gets a 404. `hls_edits` is keyed by `(item_id, id)`.
6. **Video edges are re-encoded only for `avc1` video**, with libx264 at the source's resolution, no profile above the source's, and software encoding (VAAPI stays an open question). For any other video codec the skip presentation keeps the video unskipped and says so: the master gets `EXT-X-SESSION-DATA` `us.duckfam.cog.skip`=`unsupported`, and the skip is not applied. **Cost if wrong:** mpv plays the silences of a VP9 or AV1 video, which only happens when no H.264 format is available.
7. **The edit list identifies the timeline.** The master of a skip presentation carries `#EXT-X-SESSION-DATA:DATA-ID="us.duckfam.cog.edits",VALUE="<edit>"`. The cuts of every edit id that was served are stored in `hls_edits(id TEXT PRIMARY KEY, item_id, cuts TEXT JSON, created_at)`. `ReportProgressRequest` gains `string edit_list = 6`, and the server maps presentation time back to item time. **Cog's own clients never set it**: they skip natively and report item time.
8. **Remote YouTube (phase 5): every segment URI points at Cog.**
   - Segment URIs are `yt-audio-<format>.m4a` and `yt-video-<format>.mp4` under the same signed path (410 when the format is no longer the item's). That route answers either with a 302 to the current googlevideo URL (**direct**) or by proxying the byte range (**proxy**).
   - The mode is the setting `hls_youtube_mode` (`direct` or `proxy`). Its default is decided by the off-Wi-Fi test, the plan's Task 1 gate.
   - Resolved URLs are cached in `youtube_streams(item_id, kind, url, expires_at)` until 10 minutes before they expire.
   - The `sidx` is read over HTTP Range with the phase-1 parser, through a `ReaderAt` that fetches the first 256 KB in one request.
   - Mute and skip need a download, so a remote presentation has neither. Captions come from YouTube auto-subs only once they have been fetched.
9. **The M3U route (phase 6a)** is `GET /apps/feed/m3u/{user}/{kind}/{file}`.
   - `kind` is `playlist`, `tag`, `folder`, `sub`, `starred` or `unread`, and `file` is `<id>.m3u` (`all.m3u` for the kinds that take no id).
   - It has its own signature: `"cog-feed-m3u\x00" + user + "\x00" + kind + "\x00" + id [+ "\x00" + canonical]`, with the same options as HLS.
   - Each entry is an absolute signed master URL, or the origin media URL for an item that is not packaged. The list holds at most 500 items.
10. **Android (6b):**
   - Ask `ResolveMedia` for `VARIANT_UNSPECIFIED`, and pass `MediaSource.mime` through to `MediaItem.setMimeType`.
   - On `Unavailable`, retry with `VARIANT_AUDIO`.
   - `SetVideo(bool)` drives `setTrackTypeDisabled(C.TRACK_TYPE_VIDEO, …)`. Video stays disabled until a surface exists. Backgrounding disables it again.
   - Silence skipping is Media3's `skipSilenceEnabled`, driven by `PlayerPrefs.skip_silence`.

## Shared interfaces

### `plugin/feed/hls`: phase 2 Task 1 produces these and every later phase consumes them

```go
// Segment gains Start (seconds, presentation time of its first fragment).
type Segment struct {
    Start, Duration float64
    Offset, Length  int64
}

// Init is an EXT-X-MAP: a byte range of a file.
type Init struct {
    URI            string
    Offset, Length int64
}

// Entry is one media segment of a media playlist.
type Entry struct {
    URI            string
    Offset, Length int64   // Length 0: the whole resource, no EXT-X-BYTERANGE
    Duration       float64
    Init           Init    // EXT-X-MAP written when it differs from the previous entry's
    Discontinuity  bool    // EXT-X-DISCONTINUITY before this entry
}

func Playlist(entries []Entry) string          // VOD media playlist, TARGETDURATION = the longest EXTINF as printed, rounded to nearest (RFC 8216 4.3.3.1)
func TrackSegments(t Track) []Segment          // audio fragments all count as keyframes
func TrackEntries(t Track, fileURI string) []Entry
func MediaPlaylist(t Track, fileURI string) string // = Playlist(TrackEntries(t, fileURI)); kept

type Rendition struct {
    Name, Language, URI string
    Default             bool
    Track               *Track // nil for subtitles
}

type Presentation struct {
    Audio       []Rendition // at least one; the one with Default is the default
    Video       *Track
    VideoURI    string
    Subtitles   []Rendition
    SessionData [][2]string // DATA-ID, VALUE pairs (phase 4)
}

func Master(p Presentation) string // MasterPlaylist(audio, video, aURI, vURI) is kept as a wrapper until no caller is left
```

Phase 2 also produces:
- `hls.Cue{Start, End float64; Text string}`.
- `hls.ParseVTT(r io.Reader) ([]Cue, error)`, which keeps cue text and folds YouTube's rolling auto-captions.
- `hls.VTTSegment(cues []Cue, start, end float64, mpegts int64) string`.
- `hls.SegmentCaptionBounds(audio Track, n int) (start, end float64, ok bool)`, in caption time.

There is no `TimestampMap`.

**As built (phase 2), later phases must know these:**
- **Captions are written with `X-TIMESTAMP-MAP=MPEGTS:0`, not the track's first `Start`.** hls.js 1.7.4 rebases the timeline so the first sample plays at 0. `TestBrowser/HLSCaptionsOffset` proves this only for a timeline with no discontinuities.
- **Phase 4 must re-prove caption timing after a cut**, with a browser test of a cue after a dropped run. Cues are in item time; after an `EXT-X-DISCONTINUITY`, each VTT segment's cues must be shifted into presentation time, or given a per-piece MPEGTS.
- **Phase 2's `Master` writes no `AUDIO` renditions without video**, and the audio-only variant has no `AUDIO=`. Phase 3's audio-only clean presentation changes that branch. Its Task 1 rules on it, and phase 2's goldens and `TestMasterSubtitlesAudioOnly` are updated then. *(Done in phase 3. With two or more audio renditions, `Master` writes the group, and the audio-only variant names `AUDIO="audio"` and points at the default rendition.)*

**As built (phase 3):**
- **A cleaned item has two audio tracks.** `Original` (`local_path`) is listed first. `Clean` (`clean_track_path`, `media/<id>.hls-audio.clean.m4a`) is listed second, with `DEFAULT=YES`.
- **The two tracks' segment grids differ.**
  - The Clean track is cut into fragments of about 2 s (`-frag_duration 2000000`). YouTube's original audio is cut into fragments of about 10 s.
  - **Phase 4 therefore plans cuts and edge transcodes per rendition**, for Original, Clean and video alike.
  - Its edge cache key includes the rendition, not only the item and the ranges.
  - The playlist for each rendition is built at `ServeHLS`'s per-file switch (`stored, media`).
- **Clean captions are masked.** When a clean track exists, captions pass through `profanity.FilterText`. If the profanity plugin is unavailable, they are withheld.
- **A clean track that is pending or failed means no HLS for that video.** `videoPlayOf` answers `playFile`, and `setHLSURL` skips it.
- **Phase 5 has no Clean rendition and no masking**, because a remote presentation has no download row. `ServeHLS` reads `CleanTrackPath` and `CaptionsPath` directly from `Download`. Phase 5 should build the `Presentation` (renditions and captions policy) in one function that both the local and the remote source feed.

### `plugin/feed`: file names served under `/apps/feed/hls/{user}/{item}/`

| File | Phase |
|---|---|
| `master.m3u8`, `audio.m3u8`, `video.m3u8`, `audio.m4a`, `video.mp4` | 1 |
| `subs.m3u8`, `subs-<n>.vtt` (n = segment index) | 2 |
| `clean.m3u8`, `clean.m4a` | 3 |
| `edge-<edit>-<track>-<n>.mp4` (track `a` or `v`) | 4 |
| `yt-audio-<format>.m4a`, `yt-video-<format>.mp4` | 5 |

Every URI written into a playlist is relative and carries `?sig=…` plus the presentation's canonical options.

### Options and signing: phase 4 produces, 6a consumes

```go
type hlsOptions struct{ Skip bool }
func parseHLSOptions(q url.Values) (hlsOptions, error) // a key other than sig/skip/edit, or a skip value other than "silence", is an error (400)
func (o hlsOptions) Values() url.Values                 // without sig
func hlsSignature(key []byte, user string, itemID int64, o hlsOptions) string
func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64, o hlsOptions) (string, error)
```

### Store

- **Phase 3:** `Download.CleanTrackPath` and `CleanTrackError`, in migration `migration006_clean_track.sql` (columns `clean_track_path` and `clean_track_error`).
- **Phase 4:** the `skip_silence_min_sec` `FloatSetting`; the `hls_edits` table; `downloads.edges_wanted`, `edges_edit` and `edges_error`; `Store.WantEdges(ids ...int64) (bool, error)`. All in migration `migration007_skip.sql`.
- **Phase 5:** the `youtube_streams` table, in migration `migration008_youtube_streams.sql`.

Migration numbers are taken in phase order. A plan that runs out of order takes the next free number and records a ruling.

## Global constraints (every plan)

- **Work in the worktree `cog/tmp/feed-hls`, on branch `feed-hls`**, except 6b, which works in the outer repo. Don't push, merge or open an MR without asking.
- **Follow `CLAUDE.md`.**
  - Never swallow errors.
  - POST handlers use PRG with `setFlash`.
  - Tests go through `cogtest`, with `internal/testing/require` and `assert`, never testify.
  - Programs run through `sandbox.CommandContext` with the narrowest `Spec`.
  - Files go through `feed.Space`.
  - ffmpeg inputs go through `ffargs.Input`.
- **Tooling:**
  - Check builds with `go install ./...`, not `go build`.
  - Format with `go fmt ./...` and `go tool templ fmt .`.
  - Regenerate templ with `go tool templ generate -log-level warn`, and the schema with `go generate ./plugin/feed`.
- **Decided; don't reopen:**
  - Separate tracks.
  - A cut inside a segment transcodes only that edge segment.
  - Podcast audio becomes AAC fMP4 once.
  - Captions travel as WebVTT.
  - External players get pre-signed links that don't expire.
  - `skip=silence` is a query parameter.
  - A player that can skip silence or change speed does it itself.
- `needFFmpeg(t)` skips locally when ffmpeg is missing and fails when `CI` is set.
