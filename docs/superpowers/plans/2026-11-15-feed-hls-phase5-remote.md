# Feed HLS, Phase 5: Undownloaded YouTube from googlevideo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A YouTube video that Cog has not downloaded plays at once over HLS. Its playlists are written from the `sidx` of the googlevideo tracks, and its segments are fetched from googlevideo, either through a redirect or through Cog's proxy.

**Architecture:**
- One yt-dlp call resolves the video and audio formats that the downloader would pick, with their format ids and URLs. The result is cached in `youtube_streams` until 10 minutes before googlevideo's `expire`.
- A playlist request reads each track's head (the first 256 KB, in one Range request) through `feed.HTTP`. It hands that to `hls.ReadTrack` as an `io.ReaderAt`.
- The segment URIs point at `yt-audio.m4a` and `yt-video.mp4` under the signed HLS path. Those answer in one of two ways:
  - a 302 to the current googlevideo URL (`direct`);
  - the requested byte range proxied, with one re-resolve when googlevideo answers 403 (`proxy`).
- The mode comes from the setting `hls_youtube_mode`. Its default is decided by Task 1's off-Wi-Fi test.

**Tech Stack:** Go 1.26, SQLite, yt-dlp (through `feed.YTDLP`: sandboxed, using the loopback egress proxy), `internal/safehttp` (`feed.HTTP`), `net/http/httptest`, `golang.org/x/sync/singleflight` (already in go.mod).

**Spec:** `DESIGN/plugins/feed_hls.md`, "YouTube, not downloaded". The roadmap `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md` (decision 8, `migration008`) is binding. Read both and `CLAUDE.md` first.

## Global Constraints

- Work in the worktree `cog/tmp/feed-hls`, on branch `feed-hls`. Don't push or merge without asking.
- The roadmap's global constraints apply. In short:
  - Never swallow errors; use PRG; test through cogtest with `require`/`assert`.
  - Run programs through `sandbox` and yt-dlp only through `feed.YTDLP`.
  - Check builds with `go install ./...`; format with `go fmt ./...` and `go tool templ fmt .`; regenerate the schema with `go generate ./plugin/feed`.
- **Format selectors** are the downloader's, unchanged (`pipeline/download.go:136-137`). Keep them in one place:
  - video `bv[height<=1080][vcodec^=avc1]/bv[height<=1080]`
  - audio `ba[ext=m4a]/ba`
- **Outbound fetches** of googlevideo go only through `feed.HTTP` (safehttp: public addresses only). Never use `http.DefaultClient`.
- **No new network path for credentials.** googlevideo URLs carry no Cog secrets, and Cog's signature is never forwarded to googlevideo.
- **Mute and skip need a download.** A remote presentation has neither. If phases 3 and 4 have landed, `skip=silence` on a remote item is ignored and the master says so (`EXT-X-SESSION-DATA` `us.duckfam.cog.skip`=`unavailable`).
- **Undownloaded audio** (`VARIANT_AUDIO`, or an `audio` item) keeps today's path: a resolved stream URL (`resolveStream`), with `ExpiresAt`.
- **A remote presentation that cannot be built falls back to today's behaviour:** queue the download and answer Unavailable (`downloadFirst`). Causes include a format that is not single-track fMP4 with a `sidx` (webm/VP9, opus), a DASH-manifest format, a yt-dlp failure, or googlevideo refusing.

## Decisions this plan makes (recorded in the design in Task 7)

- **Format ids are pinned.** `youtube_streams` also stores `format_id`, which the roadmap's column list doesn't have; this extends it. A refresh asks yt-dlp for exactly those ids (`--format <v>+<a>`), so byte ranges in a playlist a player already holds stay valid. If the pinned ids are gone, the cache row is dropped and the next master request resolves afresh.
- **Resolving happens when ResolveMedia or StreamURL is asked, not when the playlist is first fetched.** The answer must be a URL that will work, so ResolveMedia resolves, reads both heads, and checks both are indexed fMP4 before returning the master. On failure it falls back to `downloadFirst`. The cache makes the master request that follows cheap.
- **Every segment goes through Cog in both modes.** In direct mode Cog costs one round trip per segment, and never bytes. A VOD playlist that a player holds for longer than the ~6 h URL lifetime keeps working, because the redirect always targets a fresh URL.
- **Concurrent resolves for one item are collapsed** with `singleflight`, keyed `user/item`.
- **Playing no longer queues a download** for an undownloaded YouTube video when remote HLS works. A download that is already queued or running is left alone, and the user's explicit Download action is unchanged.

## Review Focus

1. **googlevideo refuses a URL resolved at home when it is used from another IP** (the open question). In proxy mode every segment must still play. In direct mode the player gets the 403, which is why Task 1's gate decides the default. Test: `TestHLSRemoteProxyRefreshesOn403`.
2. **The URL expires mid-session** (after a ~6 h pause). The next segment must re-resolve with the same format ids, and the byte ranges must stay valid. Tests: `TestYouTubeStreamsRefreshEarly`, `TestYouTubeStreamsRefreshPinsFormats`.
3. **A video whose best format is VP9 webm or opus audio** must fall back to downloading, not serve a broken playlist. Test: `TestResolveMediaRemoteUnindexedFallsBack`.
4. **A cookie-less player sends Range and HEAD to `yt-video.mp4`.** Proxy mode passes Range through and answers 206 with `Content-Range`. Direct mode's 302 keeps the request's Range for the player to resend. Tests: `TestHLSRemoteProxyRange`, `TestHLSRemoteDirectRedirects`.
5. **SSRF.** A resolved URL that points at a private address (a yt-dlp bug, or a hostile extractor) must be refused by `feed.HTTP`, not fetched. Test: `TestRemoteHeadRefusesPrivate`. It runs with `safehttp.SetAllowPrivate(false)` inside the test and restores it afterwards.

---

### Task 1: Resolve both tracks in one call; the off-Wi-Fi gate

**Files:**
- Create: `plugin/feed/youtube_tracks.go`, `plugin/feed/youtube_tracks_test.go`
- Create: `plugin/feed/internal/ytprobe/main.go` (a dev command; `go run ./plugin/feed/internal/ytprobe <watch-url>`)
- Modify: `plugin/feed/pipeline/download.go:136-137`, so it uses the shared selectors
- Modify: `DESIGN/plugins/feed_hls.md`, "Open questions" (the gate's result)

**Interfaces:**
- Produces:
  ```go
  package feed
  // YouTube format selectors, shared by the downloader and remote HLS.
  const (
      YouTubeVideoFormat = "bv[height<=1080][vcodec^=avc1]/bv[height<=1080]"
      YouTubeAudioFormat = "ba[ext=m4a]/ba"
  )
  type YouTubeTrack struct {
      Kind      hls.Kind
      FormatID  string
      URL       string
      ExpiresAt time.Time // from the URL's expire=, else now+1h (streamExpiry)
  }
  // ResolveYouTubeTracks resolves the video and audio formats of watchURL that
  // format names ("<video>+<audio>"), with one yt-dlp call.
  func ResolveYouTubeTracks(ctx context.Context, watchURL, format string) (video, audio YouTubeTrack, err error)
  // parseRequestedFormats reads yt-dlp's --print "%(requested_formats)j".
  func parseRequestedFormats(out []byte) (video, audio YouTubeTrack, err error)
  ```
  The yt-dlp args are `--skip-download --no-warnings --no-playlist --format <format> --print "%(requested_formats)j"`. A format whose `protocol` is not `https` (for example `http_dash_segments`) is an error, and so is one whose `vcodec`/`acodec` doesn't fit its role.

- [ ] **Step 1: Write the failing tests** in `youtube_tracks_test.go`. Use a captured JSON fixture `testdata/ytdlp-requested-formats.json`, a trimmed real output with two entries.
  - `TestParseRequestedFormats`:
    - video FormatID `"137"`, Kind `hls.Video`, URL prefix `https://rr`.
    - audio FormatID `"140"`, Kind `hls.Audio`.
    - `ExpiresAt` equals the `expire=` in the URL.
  - `TestParseRequestedFormatsRejectsManifest`: one entry has `"protocol":"http_dash_segments"`, so `err != nil`.
  - `TestParseRequestedFormatsNeedsBoth`: a single combined entry (`requested_formats` is `null`, printed as `NA`), so `err != nil`.
- [ ] **Step 2: Run** `go test -run TestParseRequestedFormats ./plugin/feed/`
  Expected: FAIL, undefined `parseRequestedFormats`.
- [ ] **Step 3: Implement** `youtube_tracks.go`, and switch `pipeline/download.go` to the constants.
- [ ] **Step 4: Run** `go test ./plugin/feed/ ./plugin/feed/pipeline/`
  Expected: PASS.
- [ ] **Step 5: Write `internal/ytprobe`.**
  - It calls `feed.ResolveYouTubeTracks(ctx, os.Args[1], feed.YouTubeVideoFormat+"+"+feed.YouTubeAudioFormat)`.
  - For each track it prints the format id, the expiry, and `curl -sS -o /dev/null -w '%{http_code} %{size_download}\n' -r 0-262143 '<url>'`.
  - Run it: `go run ./plugin/feed/internal/ytprobe 'https://www.youtube.com/watch?v=jNQXAC9IVRw'`
  - Expected: two curl lines. Running one at home prints `206 262144`.
- [ ] **Step 6: GATE (the user).**
  - Stop and ask the user to run one printed curl line from another IP: a phone off Wi-Fi in Termux, a hotspot, or a VPN. Also open the audio URL in the phone's browser, off Wi-Fi.
  - Record the result and the date under "Open questions" in `DESIGN/plugins/feed_hls.md`, and move it to "Decided".
  - **206 from the other IP:** the default `hls_youtube_mode` is `direct`.
  - **403, or anything else:** the default is `proxy`.
  - Write the chosen default into Task 2's `settingYouTubeMode`. Don't continue until the user has answered.
- [ ] **Step 7: Commit**
  ```bash
  git add plugin/feed/youtube_tracks.go plugin/feed/youtube_tracks_test.go plugin/feed/testdata/ytdlp-requested-formats.json plugin/feed/internal/ytprobe plugin/feed/pipeline/download.go DESIGN/plugins/feed_hls.md
  git commit -m "feed: resolve a YouTube video's two tracks in one yt-dlp call

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

### Task 2: `youtube_streams` cache and the mode setting

**Files:**
- Create: `plugin/feed/sql/migration008_youtube_streams.sql`; regenerate `sql/schema.sql`
- Create: `plugin/feed/store_youtube.go`, `plugin/feed/store_youtube_test.go`
- Modify: `plugin/feed/store.go` (the settings var block, near `:1973`)
- Modify: `plugin/feed/export_test.go` (resolver hook)

**Interfaces:**
- Consumes: `YouTubeTrack`, `ResolveYouTubeTracks` (Task 1).
- Produces:
  ```sql
  CREATE TABLE youtube_streams (
      item_id    INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
      kind       TEXT    NOT NULL CHECK (kind IN ('audio','video')),
      format_id  TEXT    NOT NULL,
      url        TEXT    NOT NULL,
      expires_at INTEGER NOT NULL, -- unix seconds
      PRIMARY KEY (item_id, kind)
  );
  ```
  ```go
  func (s *Store) YouTubeStreams(itemID int64) (video, audio YouTubeTrack, ok bool, err error) // ok: both rows present
  func (s *Store) SaveYouTubeStreams(itemID int64, video, audio YouTubeTrack) error           // replaces both, one tx
  func (s *Store) ForgetYouTubeStreams(itemID int64) error

  type YouTubeMode string
  const (YouTubeDirect YouTubeMode = "direct"; YouTubeProxy YouTubeMode = "proxy")
  var settingYouTubeMode = StringSetting[YouTubeMode]("hls_youtube_mode", <Task 1 gate result>)
  func (s *Store) YouTubeMode() (YouTubeMode, error)

  // resolveTracks is ResolveYouTubeTracks; tests replace it (SetTrackResolver).
  var resolveTracks = ResolveYouTubeTracks
  // youtubeTracks returns the item's cached tracks, resolving when none are
  // cached, when either expires within 10 minutes of now, or when refresh is
  // set. A refresh pins the cached format ids. Concurrent calls for one
  // user/item share one resolve.
  func (p *Plugin) youtubeTracks(ctx context.Context, store *Store, username string, itemID int64, watchURL string, now time.Time, refresh bool) (video, audio YouTubeTrack, err error)
  ```
  `export_test.go` gains `SetTrackResolver(f func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error)) func()`.

- [ ] **Step 1: Write the failing tests** in `store_youtube_test.go`.
  - `TestYouTubeStreamsRoundTrip`: save, then read back with equal fields and `ok`. Forget, then `ok == false`.
  - `TestYouTubeStreamsCached`: the stub counts calls. Two `youtubeTracks` calls at `now` (expiry now+6h) mean 1 resolve.
  - `TestYouTubeStreamsRefreshEarly`: expiry now+9m means the second call resolves again (2 resolves). At now+11m it doesn't.
  - `TestYouTubeStreamsRefreshPinsFormats`: with cached ids 137/140 and `refresh=true`, the stub sees format `"137+140"`.
  - `TestYouTubeStreamsSingleflight`: 5 goroutines with a stub that blocks until released mean 1 resolve.
  - `TestYouTubeModeDefault`: `YouTubeMode()` returns the gate's default.
- [ ] **Step 2: Run** `go test -run 'TestYouTube(Streams|Mode)' ./plugin/feed/`
  Expected: FAIL, undefined.
- [ ] **Step 3: Implement.** Write the migration, run `go generate ./plugin/feed`, then the store methods and `youtubeTracks`. The `singleflight.Group` is a field on `Plugin`.
- [ ] **Step 4: Run** `go test ./plugin/feed/`
  Expected: PASS, and `TestSchema*` still passes with the regenerated `schema.sql`.
- [ ] **Step 5: Commit**: `feed: cache resolved YouTube tracks until they near expiry`, with the trailers.

### Task 3: Read a remote track's index over one Range request

**Files:**
- Create: `plugin/feed/remote_track.go`, `plugin/feed/remote_track_test.go`

**Interfaces:**
- Consumes: `hls.ReadTrack(io.ReaderAt, size)`, `feed.HTTP`.
- Produces:
  ```go
  const remoteHead = 256 << 10
  // remoteFile is an io.ReaderAt over an HTTP resource. It fetches
  // bytes=0-(remoteHead-1) once, learns the size from Content-Range, and
  // answers reads past the head with further Range requests.
  type remoteFile struct{ /* ctx, client, url, size, head */ }
  func openRemote(ctx context.Context, client *http.Client, url string) (*remoteFile, error)
  func (f *remoteFile) ReadAt(p []byte, off int64) (int, error)
  func (f *remoteFile) Size() int64
  // RemoteTrack reads the index of the track at url.
  func RemoteTrack(ctx context.Context, client *http.Client, url string) (hls.Track, error)
  // errRefused is wrapped when the origin answers 403 or 410: its URL has
  // expired or is bound elsewhere, and a fresh one may work.
  var errRefused = errors.New("feed: remote refused")
  ```
  A non-206 answer is an error. A 200 to a Range request is also refused, since the whole body would be read. Wrap `errRefused` on 403/410.

- [ ] **Step 1: Write the failing tests.** The httptest server serves `testdata/media/video.mp4` through `http.ServeContent` and counts requests.
  - `TestRemoteTrackOneRequest`: `RemoteTrack` equals `hls.ReadTrack` on the local file (Kind, Codecs, InitLength, len(Fragments)), and the server saw exactly 1 request.
  - `TestRemoteTrackPastHead`: a padded fixture whose `sidx` ends past 256 KB. Build it in the test by writing a `free` box of 300 KB between `ftyp` and `moov`; boxes.go skips it. The result is correct, and there are more than 1 requests.
  - `TestRemoteTrackRefused`: a 403 gives `errors.Is(err, errRefused)`.
  - `TestRemoteTrackIgnoresRange`: a server answering 200 to a Range request gives an error.
  - `TestRemoteHeadRefusesPrivate`: `safehttp.SetAllowPrivate(false)`, with a deferred restore to `true`. Fetching the httptest URL gives an error wrapping `safehttp.ErrBlockedIP`.
- [ ] **Step 2: Run** `go test -run 'TestRemote(Track|Head)' ./plugin/feed/`
  Expected: FAIL, undefined.
- [ ] **Step 3: Implement** `remote_track.go`.
- [ ] **Step 4: Run** `go test -run 'TestRemote' ./plugin/feed/`
  Expected: PASS.
- [ ] **Step 5: Commit**: `feed: read a remote fMP4 track's index over one Range request`, with the trailers.

### Task 4: Serve an undownloaded YouTube item as a remote presentation

**Files:**
- Modify: `plugin/feed/hls_serve.go`
- Create: `plugin/feed/hls_remote.go`, `plugin/feed/hls_remote_test.go`
- Modify: `plugin/feed/hls_test.go` (shared helpers)

**Interfaces:**
- Consumes:
  - `youtubeTracks` (Task 2), `RemoteTrack`, `openRemote`, `errRefused` (Task 3).
  - `hls.Master`/`hls.Presentation` and `hls.MediaPlaylist` from phase 2. If phase 2 hasn't landed, use phase 1's `hls.MasterPlaylist` and record a ruling.
- Produces:
  ```go
  // remotePresentation is an undownloaded YouTube item's tracks and where they live.
  type remotePresentation struct{ Video, Audio YouTubeTrack; VideoTrack, AudioTrack hls.Track }
  func (p *Plugin) remoteYouTube(ctx context.Context, store *Store, username string, itemID int64, watchURL string, refresh bool) (remotePresentation, error)
  // serveRemoteHLS answers master.m3u8, audio.m3u8, video.m3u8, yt-audio.m4a, yt-video.mp4.
  func (p *Plugin) serveRemoteHLS(w http.ResponseWriter, r *http.Request, store *Store, username string, itemID int64, watchURL, file, query string)
  ```
- Dispatch in `ServeHLS`, after the signature check:
  - A packaged download is served locally, as now.
  - Otherwise, an item whose `GetItemPlayback(id).SourceKind == "youtube"` goes to `serveRemoteHLS`.
  - Otherwise 404.
- In the remote presentation's playlists, segment URIs are `yt-audio.m4a` and `yt-video.mp4` plus the query. Playlists are sent `private, no-cache`.
- `yt-*` in **direct** mode is a 302 to the track's URL, with `Cache-Control: no-store`.
- `yt-*` in **proxy** mode:
  1. Forward `Range` (and `If-Range`) through `feed.HTTP`, with GET or HEAD as received.
  2. Copy status (200/206/416), `Content-Type`, `Content-Length`, `Content-Range` and `Accept-Ranges`. Set `Cache-Control: private, max-age=86400`.
  3. On 403/410 from upstream, call `remoteYouTube(..., refresh=true)` once and retry. Any other non-2xx is a 502.
- When resolving fails, the master is a 502 with the cause logged (`httperr.Log`), never a playlist.

- [ ] **Step 1: Write the failing tests** in `hls_remote_test.go`.
  - Add a helper `googlevideo(t) (srv *httptest.Server, hits *atomic.Int32, refuse *atomic.Bool)`. It serves `/video` and `/audio` from `testdata/media/video.mp4` and `audio.m4a` with `ServeContent`, and answers 403 while `refuse` is set.
  - Add a helper `remoteItem(t, env, username) int64`: a YouTube subscription and item with no download row, plus `SetTrackResolver` returning the srv URLs with ids 137/140 and expire now+6h.
  - `TestHLSRemoteMaster`, with no cookie:
    - master 200, containing `video.m3u8?sig=` and `audio.m3u8?sig=`, and `RESOLUTION=160x90`.
    - `video.m3u8` contains `#EXT-X-BYTERANGE:` and `yt-video.mp4?sig=`.
  - `TestHLSRemoteDirectRedirects`: mode direct. `yt-video.mp4` with `Range: bytes=0-99` gives 302, with `Location` equal to the srv `/video` URL. Following it with the Range gives 206.
  - `TestHLSRemoteProxyRange`: mode proxy. `Range: bytes=10-109` gives 206, `Content-Range: bytes 10-109/<size>`, and a body equal to the file bytes 10..109. HEAD gives 200 with Content-Length equal to the size.
  - `TestHLSRemoteProxyRefreshesOn403`: mode proxy. Set `refuse` after the master is fetched. The resolver stub's second answer points at `/video2`, which is served normally. A segment request gives 206, and the resolver was called twice with `"137+140"` the second time.
  - `TestHLSRemoteBadSignature`: a wrong sig gives 403 and the resolver is never called.
  - `TestHLSRemoteNotYouTube`: an undownloaded podcast item gives 404 and the resolver is never called.
  - `TestHLSRemoteResolveFails`: the resolver returns an error, so the master is a 502.
- [ ] **Step 2: Run** `go test -run TestHLSRemote ./plugin/feed/`
  Expected: FAIL. The master is 404 (not packaged).
- [ ] **Step 3: Implement** `hls_remote.go` and the dispatch in `hls_serve.go`. Set the mode in tests with `settingYouTubeMode.Set` through an `export_test.go` helper `SetYouTubeMode(store, mode)`.
- [ ] **Step 4: Run** `go test -run 'TestHLS' ./plugin/feed/`
  Expected: PASS, with phase 1's `TestHLS*` unchanged.
- [ ] **Step 5: Commit**: `feed: an undownloaded YouTube video plays over HLS from googlevideo`, with the trailers.

### Task 5: ResolveMedia and StreamURL hand out the remote master

**Files:**
- Modify: `plugin/feed/rpc_media.go` (the not-downloaded YouTube video branch, and the doc comment)
- Modify: `plugin/feed/pages.go` (`StreamURL`, around `:1515-1572`)
- Modify: `plugin/feed/rpc_media_test.go`, `plugin/feed/pages_player_test.go`

**Interfaces:**
- Consumes: `remoteYouTube` (Task 4), `HLSMasterURL`, `queueVideo`/`downloadFirst`.
- Produces:
  ```go
  // remoteMaster answers the signed master of an undownloaded YouTube video
  // after checking both tracks resolve to indexed fMP4; ok is false (err nil)
  // when they don't, and the caller falls back to downloading.
  func (p *Plugin) remoteMaster(ctx context.Context, store *Store, username string, itemID int64, watchURL string) (master string, ok bool, err error)
  ```
  `ok == false` covers `hls.ErrNoIndex`, `hls.ErrMultiTrack`, a parse error, a resolve error and `errRefused`, each logged at Info with the item id. `err` is only for a store or key failure.
- New behaviour: this branch applies to an undownloaded YouTube video, or one whose download is not ready, when video is wanted.
  - It returns `MediaSource{Url: master, Mime: hlsMIME, Variant: VIDEO}` with no `ExpiresAt`.
  - StreamURL returns `{"hls": master}`.
  - A queued or running download is not touched.
  - When `ok` is false it does today's `downloadFirst` / `queueVideo`.

- [ ] **Step 1: Write the failing tests.**
  - `TestResolveMediaRemoteYouTube`, reusing `googlevideo`/`remoteItem`:
    - Variant VIDEO gives Url prefix `/apps/feed/hls/alice/`, Mime `application/vnd.apple.mpegurl`, and no ExpiresAt.
    - `GetDownload` shows no download queued (`State == ""`).
  - `TestResolveMediaRemoteUnindexedFallsBack`: the resolver points at `testdata/media/plain-video.mp4` (no `sidx`). The answer is CodeUnavailable and the download is `queued`.
  - `TestResolveMediaRemoteKeepsDownload`: the download is already `downloading`. The answer is HLS and the state is still `downloading`.
  - Update `TestResolveMediaYouTubeVideoDownloads`: the resolver stub returns an error, so the existing assertions (queued, then retried with the reason) hold through the fallback. Rename it `TestResolveMediaYouTubeVideoDownloadsWhenRemoteFails`.
  - `TestStreamURLRemoteYouTube`: `{"hls": "/apps/feed/hls/..."}`, and nothing queued.
  - Update `TestStreamURLVideoDownloadsFirst` to stub the resolver with an error.
  - `TestResolveMediaYouTube` (audio) is unchanged and must still pass.
- [ ] **Step 2: Run** `go test -run 'TestResolveMedia|TestStreamURL' ./plugin/feed/`
  Expected: the new tests FAIL (Unavailable or downloading).
- [ ] **Step 3: Implement** `remoteMaster` and both call sites.
- [ ] **Step 4: Run** `go test ./plugin/feed/...`
  Expected: PASS.
- [ ] **Step 5: Commit**: `feed: play an undownloaded YouTube video from googlevideo instead of downloading it`, with the trailers.

### Task 6: Choose the mode in Sync settings

**Files:**
- Modify: `plugin/feed/pages.templ` (the "Media links" card, near `:1080`), `plugin/feed/pages.go`, `plugin/feed/feed.go:176`
- Test: `plugin/feed/settings_test.go`

**Interfaces:**
- Produces: `POST settings/media-links/youtube-mode` with form `mode=direct|proxy`, handled by `(h *Pages) SetYouTubeMode`.
  - It uses PRG: flash "YouTube playback set to direct." / "… through this server.", then redirects to the Sync settings page.
  - Any other value gets an error flash and changes nothing.
  - The card shows two radio buttons: "Straight from YouTube" (direct) and "Through this server" (proxy).

- [ ] **Step 1: Write the failing tests.**
  - `TestSettingsYouTubeMode`: POST `mode=proxy` gives 303, the setting is `proxy`, and the page contains `value="proxy" checked`.
  - `TestSettingsYouTubeModeRejectsUnknown`: `mode=x` gives 303 with an error flash, and the setting is unchanged.
- [ ] **Step 2: Run** `go test -run TestSettingsYouTubeMode ./plugin/feed/`
  Expected: FAIL, 404 or 405.
- [ ] **Step 3: Implement.** Then run `go tool templ generate -log-level warn` and `go tool templ fmt .`.
- [ ] **Step 4: Run** `go test ./plugin/feed/`
  Expected: PASS. Update any Sync settings browser snapshot with `WEBTEST_UPDATE=1 go test -run TestBrowser ./plugin/feed/` and review the HTML diff.
- [ ] **Step 5: Commit**: `feed: choose whether YouTube media is redirected or proxied`, with the trailers.

### Task 7: Design, TODO, full verification

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`, `TODO.md`

- [ ] **Step 1: Update the design.**
  - Change the status line to "phase 5 done (date)".
  - Rewrite "YouTube, not downloaded" to cover:
    - one resolve per item, cached in `youtube_streams` until 10 min before `expire`, with format ids pinned;
    - every segment through `yt-*.m4a/mp4` (a 302 in direct mode, a proxy in proxy mode, re-resolve on 403);
    - the fallback to downloading;
    - no mute or skip without a download.
  - Move the googlevideo IP question to Decided with Task 1's result.
  - Add an open question: chunked requests, in case googlevideo throttles segment ranges.
- [ ] **Step 2: Update TODO.md.** Tick Phase 5, and name the tests (`TestHLSRemote*`, `TestResolveMediaRemote*`, `TestRemoteTrack*`).
- [ ] **Step 3: Run** `go tool verify -continue`
  Expected: everything passes. A browser-test flake under load gets rerun alone, and the run is reported either way. Revert any `go fix` rewrites of files this branch didn't touch.
- [ ] **Step 4: Manual check (the user, or the executor with `go tool dev`).**
  - Subscribe to a YouTube channel and play an undownloaded video in desktop Chrome: it starts within a few seconds and seeks.
  - Repeat on the phone off Wi-Fi, in the mode the gate chose.
- [ ] **Step 5: Commit**: `feed: HLS phase 5 in the design and TODO`, with the trailers.
