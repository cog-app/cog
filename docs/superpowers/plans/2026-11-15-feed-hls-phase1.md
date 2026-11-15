# Feed HLS, Phase 1: Downloaded Items over HLS — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A downloaded feed item plays as an HLS presentation (separate video and audio fMP4 tracks, byte-range playlists written per request) from signed URLs that need no cookie, and the web player plays it through hls.js.

**Architecture:** The downloader fetches YouTube video and audio as two files, left as YouTube made them. A new pipeline worker, the packager, makes sure every ready download (new or old) is fragmented MP4 tracks that carry a `sidx`. A file that already is one, such as a YouTube DASH track, is used as it is. Anything else is remuxed with ffmpeg, and podcast audio is transcoded to AAC. The `sidx` inside each track file is the index, so nothing about fragments goes in the database. An unauthenticated route under `/apps/feed/hls/` checks an HMAC signature made with a per-user key. It reads the tracks' `moov` and `sidx` (a few KB) to write master and media playlists, or serves the track files with byte ranges. Mute and skip stay client-side in this phase (phases 3 and 4).

**Tech Stack:** Go 1.26, SQLite (per-user plugin DB), ffmpeg/ffprobe through `internal/sandbox`, yt-dlp, templ, hls.js (vendored), cogtest and webtest.

**Spec:** `DESIGN/plugins/feed_hls.md` (branch `feed-hls-design`, commit cea72dd4 rebased onto master 192733e3). Read it and `CLAUDE.md` first.

## Global Constraints

- Work in the worktree `cog/tmp/feed-hls` (branch `feed-hls-design`, already rebased on `origin/master`). Branch `feed-hls` from it for the implementation. Do not push; ask first.
- Errors are never swallowed: wrap and return, or log. (`CLAUDE.md`)
- POST handlers redirect (PRG) with `setFlash`. (`CLAUDE.md`)
- Tests go through `cogtest`; use `internal/testing/require` and `assert`, never testify.
- Programs run through `sandbox.CommandContext` with the narrowest `Spec` (`Read` the inputs, `Write` the output directory, no network).
- A plugin never builds a path or DSN; files go through `feed.Space` (`Own`, `Cache`, `Keep`).
- Schema changes go in a new `sql/migration005_package.sql`; `go generate ./plugin/feed` regenerates `sql/schema.sql` (`go tool migratesql`).
- Format with `go fmt ./...` and `go tool templ fmt .`; check builds with `go install ./...`, not `go build`.
- Decided, not to reopen: separate video and audio tracks (no merged mp4); podcast audio is transcoded once to AAC in fMP4; external players get pre-signed URLs; a player that can skip silence or change speed does it itself.
- Video tracks are stream-copied, never transcoded. Audio is copied when it is AAC, else encoded `-c:a aac -b:a 128k`.
- Signed links do not expire: a leaked link plays one item, which costs little. Revoking the user's key is what kills links.
- A file is not copied or rewritten when it is already what HLS needs.
- ffmpeg is installed on the dev machine (`/usr/sbin/ffmpeg`), and it writes a `sidx` with Task 1's flags (checked 2026-11-15).

## Decisions this plan makes (record them in the design in Task 9)

- **The index is the track file's own `sidx`. It is not copied into the database or into a pre-baked playlist.** A playlist request reads the track's `moov` and `sidx`: under 25 KB for an hour, at the start of the file. A stored copy could fall out of step with the file. A baked `.m3u8` would also have to change whenever the key is revoked, since its URIs carry the signature, and again for every option set in phases 3 and 4. Phase 5 reads googlevideo's `sidx` with the same parser, over one Range request. If remote reads turn out slow, that is when to add a cache.
- **Packaging is the mp4 muxer with a global `sidx`, not ffmpeg's HLS muxer.** The `sidx` also makes a fragmented MP4 seekable when it is played straight through. Media3 needs that for `items/{id}/media`, which the app plays.
- **Fragments are grouped into segments when the playlist is written.** A fragment is one per keyframe for video and 2 s for audio, and segments are about 6 s. Phase 4 cuts at fragment boundaries.
- **A track that is already single-track fMP4 with a `sidx` is used as it is.** YouTube's DASH tracks qualify when yt-dlp runs with `--fixup never`, which keeps it from remuxing m4a into a plain MP4. This applies when the audio is AAC; a video track's codec is never changed.
- **After packaging, `downloads.local_path` is the audio track and `downloads.video_path` the video track.** These are the downloaded files when they were used as they are, and otherwise `media/<id>.hls-audio.m4a` and `media/<id>.hls-video.mp4`. Silence, transcribe, profanity and `items/{id}/media` keep reading `local_path` unchanged. `Collect` removes a replaced original.
- **URL form** `/apps/feed/hls/{user}/{item}/{file}?sig=<b64url>`. The user is in the path because the route has no session. The signature covers user, item and the option parameters. Every URL inside the playlists carries the same `sig`, so one grant covers one item's presentation. Phase 1 accepts no options, and any other query parameter is a 400.
- **The per-user key** is 32 random bytes in `feed_settings` under `hls_key`, made on first use. "Revoke media links" on the sync settings page replaces it.
- An item whose packaging fails keeps playing as it did before (progressive `items/{id}/media`); its error is shown, not retried every tick.

## Review Focus

1. **A podcast MP3 with cover art** (an `attached_pic` video stream) must package as audio only. An audio item never gets a video track. (Task 5 test `TestPackageMP3WithCover`.)
2. **`Collect` must keep packaged tracks and raw YouTube video** (`video_path`). Deleting them would silently break every packaged item on the next tick. (Task 3 `TestCollectKeepsVideoPath`, Task 5 `TestPackageThenCollect`.)
3. **Packaging that fails leaves the item playable as before**, and is not retried every tick. (Task 5 `TestPackageFailureKeepsMedia`, Task 7 `TestStreamURLPackageError`.)
4. **A player with no cookie** (mpv, hls.js, Media3) fetching master, media playlist, init and segments with `Range` gets 200/206; HEAD works; a signature for alice's item does not open bob's. (Task 6 tests.)
5. **A YouTube track that is not indexed fMP4** must be remuxed, not served as it is. This happens when yt-dlp falls back to a webm, or a format without a `sidx`. Serving it broken would break the playlist. (Task 5 `TestPackageRemuxesUnindexed`.)

---

### Task 1: fMP4 track index (`hls.ReadTrack`) and test media

**Files:**
- Create: `plugin/feed/hls/track.go`, `plugin/feed/hls/boxes.go`
- Create: `plugin/feed/testdata/media/make.sh` and the files it makes: `video.mp4`, `audio.m4a`, `merged.mp4`, `episode.mp3`
- Test: `plugin/feed/hls/track_test.go`

**Interfaces:**
- Produces:
  ```go
  package hls
  type Kind string
  const (Audio Kind = "audio"; Video Kind = "video")
  type Fragment struct {
      Start, Duration float64 // seconds
      Offset, Length  int64   // bytes in the track file
      Keyframe        bool    // sidx starts_with_SAP
  }
  type Track struct {
      Kind                   Kind
      Codecs                 string // RFC 6381; "" when unknown
      Width, Height          int    // video only
      InitOffset, InitLength int64  // ftyp+moov
      Fragments              []Fragment
  }
  func ReadTrack(r io.ReaderAt, size int64) (Track, error)
  var ErrNoIndex = errors.New("hls: no sidx")
  var ErrMultiTrack = errors.New("hls: more than one track")
  ```
  `ReadTrack` reads only what it needs from the start of the file (boxes up to the end of `sidx`), never the media. It is called on every playlist request.

- [ ] **Step 1: Write `make.sh` and make the media** with the local ffmpeg (it has libx264). Commit the outputs; together they must stay under 300 KB.

```sh
#!/bin/sh
# Test media for the feed's HLS packaging. Run from anywhere; rewrites the
# files beside it. The flags for video.mp4 and audio.m4a are the packager's
# (pipeline/package.go packageArgs): change them together.
set -eu
cd "$(dirname "$0")"
ff="ffmpeg -nostdin -y -v error"
# Packaged tracks: 12 s, 2 s GOP, fragmented with a global sidx.
$ff -f lavfi -i testsrc=size=160x90:rate=24:duration=12 \
	-c:v libx264 -profile:v baseline -level 3.0 -g 48 -keyint_min 48 -sc_threshold 0 -pix_fmt yuv420p \
	-movflags +frag_keyframe+empty_moov+default_base_moof+global_sidx -f mp4 video.mp4
$ff -f lavfi -i sine=frequency=440:duration=12 -c:a aac -b:a 32k \
	-movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4 audio.m4a
# Sources the packager reads: a merged download as MR !54 made them, and a
# podcast episode with cover art.
$ff -f lavfi -i testsrc=size=160x90:rate=24:duration=6 -f lavfi -i sine=frequency=440:duration=6 \
	-c:v libx264 -profile:v baseline -g 48 -pix_fmt yuv420p -c:a aac -b:a 32k -movflags +faststart merged.mp4
# A video track that is not fragmented, as a YouTube download might be.
$ff -i merged.mp4 -an -c copy -movflags +faststart plain-video.mp4
$ff -f lavfi -i sine=frequency=440:duration=6 -f lavfi -i color=c=red:size=64x64:duration=1 \
	-map 0:a -map 1:v -c:a libmp3lame -b:a 32k -c:v mjpeg -frames:v 1 -disposition:v attached_pic -id3v2_version 3 episode.mp3
```

These flags were checked on 2026-11-15: both packaged files come out as `ftyp`, `moov`, `sidx`, then six `moof` fragments.

- [ ] **Step 2: Write the failing tests** in `plugin/feed/hls/track_test.go` (package `hls_test`), opening files from `../testdata/media/`:

```go
func TestReadTrackVideo(t *testing.T) {
	tr := readTrack(t, "video.mp4")
	assert.Equal(t, hls.Video, tr.Kind)
	assert.Equal(t, 160, tr.Width)
	assert.Equal(t, 90, tr.Height)
	assert.True(t, strings.HasPrefix(tr.Codecs, "avc1.42") && strings.HasSuffix(tr.Codecs, "1E") && len(tr.Codecs) == 11, tr.Codecs)
	assert.Equal(t, 6, len(tr.Fragments)) // 12 s at a 2 s GOP
	checkContiguous(t, tr)                 // see below
	for _, f := range tr.Fragments { assert.True(t, f.Keyframe) }
	assert.InDelta(t, 12.0, totalDuration(tr), 0.05)
}
func TestReadTrackAudio(t *testing.T) {
	tr := readTrack(t, "audio.m4a")
	assert.Equal(t, hls.Audio, tr.Kind)
	assert.Equal(t, "mp4a.40.2", tr.Codecs)
	assert.InDelta(t, 12.0, totalDuration(tr), 0.05)
	for _, f := range tr.Fragments { assert.True(t, f.Duration <= 2.1, f.Duration) }
	checkContiguous(t, tr)
}
// A merged download has two tracks; an MP3 has no index.
func TestReadTrackNotATrack(t *testing.T) {
	_, err := readTrackErr(t, "merged.mp4")
	assert.True(t, errors.Is(err, hls.ErrMultiTrack), err)
	_, err = readTrackErr(t, "episode.mp3")
	assert.True(t, errors.Is(err, hls.ErrNoIndex), err)
}
```

`checkContiguous` asserts `InitOffset == 0`, `Fragments[0].Offset >= InitOffset+InitLength`, each `Offset+Length == next.Offset`, the last ends at the file size, and each `Start` equals the previous `Start+Duration` within 1 ms. If `assert.InDelta` does not exist in `internal/testing/assert`, add it there (with a test).

- [ ] **Step 3: Run** `go test ./plugin/feed/hls/` — FAIL (package does not exist).

- [ ] **Step 4: Implement `ReadTrack` in `track.go` and the box reader in `boxes.go`.** Walk top-level boxes (32-bit size, `size==1` → 64-bit largesize, `size==0` → to end of file). Init is `[0, end of moov)`. More than one `trak` → `ErrMultiTrack`. From `moov/trak/mdia`: `hdlr` handler type `vide`/`soun` → Kind (other → error); `mdhd` timescale is not needed because `sidx` carries its own. From `stsd`'s first sample entry: `avc1`/`avc3` → width/height (uint16 at entry offset 24/26 after the 8-byte box header... per ISO 14496-12 VisualSampleEntry) and `avcC` bytes 1–3 → `fmt.Sprintf("avc1.%02X%02X%02X", ...)`; `mp4a` → `esds` audioObjectType → `"mp4a.40.<aot>"`; anything else → `Codecs = ""`. `sidx` v0/v1: timescale, earliest_presentation_time, first_offset, then per reference `referenced_size` (31 bits; a reference_type of 1, a nested sidx, is an error), `subsegment_duration`, `starts_with_SAP`. First fragment offset = end of the `sidx` box + first_offset. No `sidx` → `ErrNoIndex`; truncated or inconsistent boxes → a wrapped error naming the box.

- [ ] **Step 5: Run** `go test ./plugin/feed/hls/` — PASS.

- [ ] **Step 6: Commit** `feed: read an fMP4 track's fragment index from its sidx` (track.go, boxes.go, tests, testdata/media).

---

### Task 2: Playlists (`hls.Segments`, `hls.MediaPlaylist`, `hls.MasterPlaylist`)

**Files:**
- Create: `plugin/feed/hls/playlist.go`
- Test: `plugin/feed/hls/playlist_test.go`

**Interfaces:**
- Consumes: `hls.Track`, `hls.Fragment` (Task 1).
- Produces:
  ```go
  const SegmentTarget = 6.0 // seconds
  type Segment struct { Duration float64; Offset, Length int64 }
  func Segments(frags []Fragment) []Segment
  func MediaPlaylist(t Track, fileURI string) string
  func MasterPlaylist(audio Track, video *Track, audioURI, videoURI string) string
  ```

- [ ] **Step 1: Write the failing tests.**

```go
// Fragments join into segments of at least SegmentTarget, each starting on a keyframe.
func TestSegments(t *testing.T) {
	f := func(d float64, key bool) hls.Fragment { return hls.Fragment{Duration: d, Length: 10, Keyframe: key} }
	frags := offsets(f(2, true), f(2, true), f(2, true), f(2, true), f(3, false), f(2, true), f(1, true))
	got := hls.Segments(frags)
	// 2+2+2 reaches the target; the 3 s fragment is no keyframe, so it joins
	// 2 and then 2 more (5 s is short of the target); the last starts anew.
	assert.Equal(t, []float64{6, 7, 1}, durations(got))
	assert.Equal(t, int64(30), got[0].Length)
	assert.Equal(t, got[0].Offset+got[0].Length, got[1].Offset)
}
func TestMediaPlaylist(t *testing.T) {
	tr := hls.Track{Kind: hls.Audio, InitOffset: 0, InitLength: 700, Fragments: offsetsFrom(800, /* 2 s × 4 */)}
	pl := hls.MediaPlaylist(tr, "audio.m4a?sig=x")
	for _, want := range []string{
		"#EXTM3U\n", "#EXT-X-VERSION:7\n", "#EXT-X-PLAYLIST-TYPE:VOD\n", "#EXT-X-TARGETDURATION:6\n",
		`#EXT-X-MAP:URI="audio.m4a?sig=x",BYTERANGE="700@0"` + "\n",
		"#EXTINF:6.000,\n#EXT-X-BYTERANGE:", "#EXT-X-ENDLIST\n",
	} { assert.True(t, strings.Contains(pl, want), want) }
}
// EXTINF rounded to the nearest integer must not exceed the target duration (RFC 8216 4.3.3.1).
func TestMediaPlaylistTargetDuration(t *testing.T) { /* segments 6.4 and 2.0 → TARGETDURATION:6; 6.6 → 7 */ }
func TestMasterPlaylistVideo(t *testing.T) {
	// video with Codecs "avc1.42C01E", 160x90; audio "mp4a.40.2"
	pl := hls.MasterPlaylist(audio, &video, "audio.m3u8?q", "video.m3u8?q")
	// wants: EXT-X-INDEPENDENT-SEGMENTS; one EXT-X-MEDIA TYPE=AUDIO,GROUP-ID="audio",NAME="Original",DEFAULT=YES,AUTOSELECT=YES,URI="audio.m3u8?q";
	// a STREAM-INF with CODECS="avc1.42C01E,mp4a.40.2",RESOLUTION=160x90,AUDIO="audio" followed by video.m3u8?q;
	// an audio-only STREAM-INF with CODECS="mp4a.40.2" and no AUDIO attribute, followed by audio.m3u8?q;
	// BANDWIDTH of the video variant = video peak + audio peak.
}
func TestMasterPlaylistAudioOnly(t *testing.T) { /* video nil: one STREAM-INF, no EXT-X-MEDIA */ }
func TestMasterPlaylistUnknownCodec(t *testing.T) { /* video Codecs "" → that STREAM-INF has no CODECS attribute */ }
```

Write the elided bodies out; the comments state their assertions.

- [ ] **Step 2: Run** `go test ./plugin/feed/hls/ -run 'Segments|Playlist'` — FAIL.

- [ ] **Step 3: Implement** in `playlist.go`. Segment grouping: start a new segment at a keyframe fragment once the current one has reached `SegmentTarget`. Peak BANDWIDTH is the max over segments of `ceil(Length*8/Duration)`; AVERAGE-BANDWIDTH is total bytes ×8 / total duration. EXTINF is `%.3f`. TARGETDURATION is the max of `math.Round(duration)` over segments, at least 1.

- [ ] **Step 4: Run** `go test ./plugin/feed/hls/` — PASS.

- [ ] **Step 5: Commit** `feed: write HLS master and media playlists from a track index`.

---

### Task 3: Schema and store

**Files:**
- Create: `plugin/feed/sql/migration005_package.sql`
- Modify: `plugin/feed/sql/schema.sql` (generated), `plugin/feed/store.go` (Download type, download methods, `DownloadFiles`, `GetItemPlayback`)
- Create: `plugin/feed/store_media.go` (package and key methods)
- Test: `plugin/feed/store_media_test.go`, `plugin/feed/pipeline/download_test.go` (Collect)

**Interfaces:**
- Produces:
  ```go
  // Download gains:
  VideoPath    string // YouTube's video track, as downloaded or as packaged
  PackageState string // "" pending, "done", "error"
  PackageError string
  // ItemPlayback gains:
  Packaged  bool // PackageState == "done"
  HasVideo  bool // VideoPath != ""
  HLSURL    string // set by handlers, not the store (Task 7)

  func (s *Store) MarkVideoDownloadReady(itemID int64, audioPath, videoPath string, bytes int64) error
  func (s *Store) PendingPackage(limit int) ([]int64, error)
  func (s *Store) SavePackage(itemID int64, audioPath, videoPath string, bytes int64) error
  func (s *Store) MarkPackageError(itemID int64, msg string) error
  func (s *Store) HLSKey() ([]byte, error)    // creates on first use
  func (s *Store) RotateHLSKey() error
  ```

- [ ] **Step 1: Write the migration.**

```sql
-- IF COLUMN downloads.video_path NOT EXIST
ALTER TABLE downloads ADD COLUMN video_path TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN package_state TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN package_error TEXT NOT NULL DEFAULT '';
```

Check the guard comment syntax in `internal/migrate` (it guards the whole file). Run `go generate ./plugin/feed` and commit the regenerated `schema.sql`.

- [ ] **Step 2: Write the failing tests** (`store_media_test.go`, package `feed_test`, with the helpers in `rpc_media_test.go`):

```go
func TestSavePackage(t *testing.T)                     // SavePackage → GetDownload has LocalPath/VideoPath/Bytes/PackageState "done"; PendingPackage no longer lists it; GetItemPlayback Packaged and HasVideo
func TestPendingPackage(t *testing.T)                  // lists ready downloads with package_state ''; not queued, not error, not done
func TestMarkPackageError(t *testing.T)                // PackageState "error", PackageError the message; not pending; LocalPath unchanged
func TestMarkDownloadReadyResetsPackage(t *testing.T)  // after SavePackage, MarkDownloadReady (and MarkVideoDownloadReady) → PackageState "", VideoPath as given ("" for MarkDownloadReady), pending again
func TestHLSKeyStableAndRotates(t *testing.T)          // two HLSKey calls equal, 32 bytes; after RotateHLSKey a different 32 bytes
```

And in `pipeline/download_test.go`:

```go
// Collect keeps the video a row names.
func TestCollectKeepsVideoPath(t *testing.T) // Put media/7.hls-video.mp4, a row naming it as video_path (MarkVideoDownloadReady) → Collect → Stat succeeds
```

- [ ] **Step 3: Run** `go test ./plugin/feed/ -run 'Package|HLSKey'` and `go test ./plugin/feed/pipeline/ -run Collect` — FAIL.

- [ ] **Step 4: Implement.** `SavePackage` is one UPDATE of `local_path`, `video_path`, `bytes`, `package_state='done'`, `package_error=''`. `MarkDownloadReady` and `MarkVideoDownloadReady` set `package_state=''` and `package_error=''` too. `GetDownload`, `scanDownloads`/`PendingDownloads` and `DownloadsByItems` select the new columns. `DownloadFiles` adds `video_path`. `GetItemPlayback` selects `Packaged` and `HasVideo`. `HLSKey` inserts 32 bytes from `crypto/rand` as hex with `INSERT ... ON CONFLICT DO NOTHING`, then reads it back.

- [ ] **Step 5: Run** `go test ./plugin/feed/...` — PASS (existing tests included).

- [ ] **Step 6: Commit** `feed: record packaged tracks and a key for media links`.

---

### Task 4: Downloader fetches YouTube video and audio apart

**Files:**
- Modify: `plugin/feed/pipeline/download.go` (`processOne`, `youtubeFormat`, `youtubeExt`)
- Modify: `plugin/feed/pipeline/testdata/yt-dlp` only if needed
- Test: `plugin/feed/pipeline/download_test.go`

**Interfaces:**
- Consumes: `Store.MarkVideoDownloadReady` (Task 3).
- Produces: a ready YouTube video download has `LocalPath = "media/<id>.m4a"` (audio) and `VideoPath = "media/<id>.video.mp4"`, `Bytes` their sum, `PackageState ""`.

- [ ] **Step 1: Replace `TestDownloadYouTube`'s expectations** (the stub writes its arguments into the file it is told to write):

```go
func TestDownloadYouTube(t *testing.T) {
	// ... as now ...
	assert.Equal(t, "media/"+id+".m4a", d.LocalPath)
	assert.Equal(t, "media/"+id+".video.mp4", d.VideoPath)
	video := ytdlpArgs(t, space, d.VideoPath)
	assert.True(t, strings.Contains(video, "bv[height<=1080][vcodec^=avc1]/bv[height<=1080]"), video)
	audio := ytdlpArgs(t, space, d.LocalPath)
	assert.True(t, strings.Contains(audio, "ba[ext=m4a]/ba"), audio)
	for _, args := range []string{video, audio} {
		assert.False(t, strings.Contains(args, "merge-output-format"), args)
		assert.True(t, strings.Contains(args, "--fixup never"), args) // keep YouTube's fMP4 and its sidx
	}
}
// A failed second fetch fails the download and leaves nothing in storage or cache.
func TestDownloadYouTubeAudioFails(t *testing.T) // stub variant: exit 1 when args contain "ba[ext=m4a]" (env var read by the stub, e.g. YTDLP_FAIL_AUDIO=1) → State "error", no media/ files, cache downloads/ empty
```

`TestDownloadYouTubeAudio` (audio item) stays as it is.

- [ ] **Step 2: Run** `go test ./plugin/feed/pipeline/ -run DownloadYouTube` — FAIL.

- [ ] **Step 3: Implement.** For a YouTube item of type `video`, run yt-dlp twice, each with `--fixup never` and `-o` its own cache file. The video uses `--format "bv[height<=1080][vcodec^=avc1]/bv[height<=1080]"` and the audio `--format "ba[ext=m4a]/ba"`. `Keep` both, then `MarkVideoDownloadReady`. On any failure remove what was kept or cached (`errors.Join` the removal errors) and `fail`. YouTube audio items also get `--fixup never`. Non-YouTube items are unchanged. Update the `youtubeFormat` doc comment: tracks stay apart as YouTube serves them, and the packager makes them HLS.

- [ ] **Step 4: Run** `go test ./plugin/feed/pipeline/` — PASS.

- [ ] **Step 5: Check a real download.** yt-dlp is not on the dev machine. Install the pinned one (`2026.10.26`, with deno `v2.9.8`) into the project's `tmp/`, or run it in the Cog image. Download one public video's two tracks with the exact arguments above, then confirm with `ffprobe` and `grep -obUa sidx` that each file is single-track fragmented MP4 with a `sidx`. If either is not, record what yt-dlp gives in the report. The packager still remuxes such files (Task 5), so only the "used as it is" saving is lost.

- [ ] **Step 6: Commit** `feed: download YouTube video and audio as separate tracks`.

---

### Task 5: Packager worker

**Files:**
- Create: `plugin/feed/pipeline/package.go`
- Modify: `plugin/feed/orchestrator/orchestrator.go` (run after the downloader, before silence)
- Modify: `Dockerfile` (`ci` stage: `apk add --no-cache gcc musl-dev chromium git ffmpeg`)
- Test: `plugin/feed/pipeline/package_test.go`

**Interfaces:**
- Consumes: `hls.ReadTrack` (Task 1), `Store.PendingPackage/SavePackage/MarkPackageError/GetItem/GetDownload` (Task 3), `feed.Space`.
- Produces:
  ```go
  type Packager struct{ store *feed.Store; space feed.Space }
  func NewPackager(s *feed.Store, space feed.Space) *Packager
  func (p *Packager) RunOnce(ctx context.Context) error
  // internal, tested directly from an internal test file:
  type probeStream struct { Index int; Type, Codec string; AttachedPic bool }
  func parseProbe(out []byte) ([]probeStream, error)            // ffprobe -of json
  func packageArgs(in, out string, kind hls.Kind, transcode bool) []string
  ```

- [ ] **Step 1: Write the failing pure tests** (`package_internal_test.go`, package `pipeline`):

```go
func TestPackageArgsVideo(t *testing.T) {
	got := strings.Join(packageArgs("in.mp4", "out.mp4", hls.Video, false), " ")
	// -nostdin -y -v error -i in.mp4 -map 0:v:0 -c copy -movflags +frag_keyframe+empty_moov+default_base_moof+global_sidx -f mp4 out.mp4
}
func TestPackageArgsAudioCopy(t *testing.T)      // -map 0:a:0 -c copy -movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4
func TestPackageArgsAudioTranscode(t *testing.T) // as above with -c:a aac -b:a 128k in place of -c copy
func TestParseProbe(t *testing.T)                // a literal ffprobe JSON with an mp3 audio stream and an mjpeg stream with disposition.attached_pic 1 → two streams, the second AttachedPic
```

The flags must match `testdata/media/make.sh`'s for `video.mp4` and `audio.m4a`.

- [ ] **Step 2: Write the failing ffmpeg tests** (`package_test.go`, package `pipeline_test`). They need real ffmpeg: a helper `needFFmpeg(t)` skips when `exec.LookPath("ffmpeg")` fails, unless `CI` is set, in which case it fails. These tests must not put `testdata/` (the ffmpeg stub) on `PATH`. Copy fixtures from `../testdata/media/` into the user's storage with `space.Own.Put`.

```go
// An old merged download becomes a video and an audio track.
func TestPackageMerged(t *testing.T)        // video item, LocalPath media/1.mp4 = merged.mp4 → RunOnce → PackageState "done"; LocalPath "media/<id>.hls-audio.m4a" (ReadTrack: Audio, "mp4a.40.2"), VideoPath "media/<id>.hls-video.mp4" (ReadTrack: Video, "avc1." prefix); Bytes = their sizes
// Tracks that are already indexed fMP4, as YouTube's are, are used as they are.
func TestPackageUsesIndexedTracksAsIs(t *testing.T) // LocalPath media/<id>.m4a = audio.m4a, VideoPath media/<id>.video.mp4 = video.mp4 (MarkVideoDownloadReady) → done; paths unchanged; files byte-identical; no files under media/ but those two; cache package/ empty
// A downloaded track that is not indexed fMP4 is remuxed.
func TestPackageRemuxesUnindexed(t *testing.T) // VideoPath = a non-fragmented single-track mp4 (make.sh adds `plain-video.mp4`: merged.mp4 with -an -movflags +faststart) → done, VideoPath "media/<id>.hls-video.mp4", ReadTrack succeeds
func TestPackageMP3WithCover(t *testing.T)   // audio item, episode.mp3 → done; VideoPath ""; LocalPath ReadTrack: Audio, "mp4a.40.2"
func TestPackageFailureKeepsMedia(t *testing.T) // LocalPath names a file of junk bytes → PackageState "error", PackageError non-empty, LocalPath unchanged and file still there; a second RunOnce does not try again (PendingPackage empty)
func TestPackageThenCollect(t *testing.T)    // after TestPackageMerged's RunOnce, Collect removes media/1.mp4 and keeps both hls files
```

- [ ] **Step 3: Run** `go test ./plugin/feed/pipeline/ -run Package` — FAIL.

- [ ] **Step 4: Implement `package.go`.** Per pending item: `GetItem` and `GetDownload`. First, for each of `LocalPath` (wanting Audio) and `VideoPath` (wanting Video), `ReadTrack` the stored file. If it succeeds with the wanted kind, and for audio `Codecs` starts with `mp4a.40.`, the file is used as it is: no copy, no ffmpeg. When every needed track is like that, `SavePackage` with the same paths and sizes, and stop. Otherwise probe each input that still needs work (`ffprobe -v error -show_entries stream=index,codec_type,codec_name:stream_disposition=attached_pic -of json`), sandboxed with `Read: [input]`. The video source is the first non-attached-pic video stream of `VideoPath`, else of `LocalPath`, and only when the item's type is `video`. The audio source is the first audio stream of `LocalPath`, else of `VideoPath`. No audio stream is a packaging error. Transcode audio when its codec is not `aac`. Run ffmpeg with `packageArgs` into the cache (`package/<id>.hls-audio.m4a`, `package/<id>.hls-video.mp4`), sandboxed with `Read: inputs, Write: [cache package dir]`. `ReadTrack` each output, so a file without an index never reaches storage. `Keep` each as `media/<id>.hls-audio.m4a` / `media/<id>.hls-video.mp4`, then `SavePackage` with the sum of the tracks' sizes. Any failure: remove what this run cached or kept, then `MarkPackageError` with the error, and return the error, which RunOnce logs like the other workers. `ctx` cancellation returns without recording an error, as `Silence.processOne` does.

- [ ] **Step 5: Wire it** in `orchestrator.processUser` after the downloader: `pipeline.NewPackager(store, space).RunOnce(ctx)`, logging its error as `"feed orchestrator: package"`. Add `ffmpeg` to the `ci` stage in the `Dockerfile` with a comment that the packager's tests run it.

- [ ] **Step 6: Run** `go test ./plugin/feed/...` — PASS, with no skips (ffmpeg is installed).

- [ ] **Step 7: Commit** `feed: package downloads as fragmented MP4 tracks with an index`.

---

### Task 6: Signed media links and the HLS route

**Files:**
- Create: `plugin/feed/hls_links.go` (sign, verify, URL), `plugin/feed/hls_serve.go` (handler)
- Modify: `plugin/feed/feed.go` (register the route in the `PrivilegedContext` block), `plugin/feed/pages.go` + `pages.templ` (revoke action on the sync settings page)
- Test: `plugin/feed/hls_test.go`

**Interfaces:**
- Consumes: `Store.HLSKey/RotateHLSKey/GetDownload`, `hls.ReadTrack/MediaPlaylist/MasterPlaylist`, `PluginContext.UserLookup()`.
- Produces:
  ```go
  // HLSMasterURL is the signed path of an item's master playlist. It works until the user revokes media links.
  func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64) (string, error)
  ```
  Route: `GET /apps/feed/hls/{user}/{item}/{file}` with `file` one of `master.m3u8`, `audio.m3u8`, `video.m3u8`, `audio.m4a`, `video.mp4`.
  Action: `POST /apps/feed/settings/media-links/revoke`.

- [ ] **Step 1: Write the failing tests.** Seed a packaged item without ffmpeg: copy `testdata/media/audio.m4a` and `video.mp4` into storage, then `MarkVideoDownloadReady` and `SavePackage` with those paths. Requests carry no auth.

```go
func TestHLSMasterNoCookie(t *testing.T)       // GET HLSMasterURL path → 200, Content-Type application/vnd.apple.mpegurl, body has "#EXT-X-STREAM-INF" and `URI="audio.m3u8?sig=` and "video.m3u8?sig="
func TestHLSMediaAndRanges(t *testing.T)       // GET video.m3u8 (same sig) → every BYTERANGE lies inside video.mp4's size; GET video.mp4 with Range: bytes=<first segment> → 206 and the body equals that slice of the fixture; HEAD master.m3u8 → 200
func TestHLSAudioOnlyItem(t *testing.T)        // package with audio only → master has no EXT-X-MEDIA; video.m3u8 → 404
func TestHLSRejects(t *testing.T)              // table: bad sig, sig of another item, alice's sig on bob's path, an extra query parameter (400), unknown file (404), unknown user → 403 unless noted; none of them sets a cookie or redirects to login
func TestHLSLinkDoesNotExpire(t *testing.T)    // env.Clock.Advance(10 * 365 * 24 * time.Hour) → the same URL still 200
func TestHLSRevoke(t *testing.T)               // POST settings/media-links/revoke as alice (with CSRF, as other settings tests do) → 303 with flash; the old URL → 403; a new HLSMasterURL → 200
func TestHLSNotPackaged(t *testing.T)          // a signed URL for an item whose PackageState is not "done" → 404
```

- [ ] **Step 2: Run** `go test ./plugin/feed/ -run HLS` — FAIL.

- [ ] **Step 3: Implement signing** in `hls_links.go`. The MAC is HMAC-SHA256 over `"cog-feed-hls\x00" + user + "\x00" + item` (decimal), base64url without padding. Phase 1 has no options; phases 3 and 4 append the canonical (sorted) option parameters to the message. Compare with `hmac.Equal`. The URL is `"/apps/feed/hls/" + url.PathEscape(user) + "/" + item + "/master.m3u8?sig=" + sig`.

- [ ] **Step 4: Implement the handler** in `hls_serve.go`. Reject any query key other than `sig` with 400 before anything else. Look up the user through `UserLookup().GetByUsername` (a nil `UserLookup` is a logged 500); an unknown user is 403, so the route never reveals who has an account. Open the store, and treat a missing key as 403. Verify the signature. Then `GetDownload`; anything but `PackageState == "done"` → 404. For a playlist, open the track file(s) from storage and `ReadTrack` them; this reads only the head of each file. If the opened file is not an `io.ReaderAt`, that is a wrapped error, not a fallback that reads the whole file. Playlists are written with relative URIs carrying the request's `sig`. They are sent with `Cache-Control: private, no-cache`. Track files are served with `http.ServeContent` from `space.Own.Open(Name(path))`, with `Content-Type` `video/mp4` or `audio/mp4` and `Cache-Control: private, max-age=86400`. Register it with `priv.RegisterRoute("GET /apps/feed/hls/{user}/{item}/{file}", ...)`. If the server's global middleware still redirects this unauthenticated route to login, find where `internal/web` exempts ncapi/gpodder, and exempt this prefix the same way.

- [ ] **Step 5: Implement revoke.** Add a "Media links" section to the sync settings page with one POST button. It says that links handed to other players (mpv, the app) stop working. The handler calls `RotateHLSKey`, then `setFlash(w, "success", "Media links revoked.")` and redirects 303 to `settings/sync`.

- [ ] **Step 6: Run** `go test ./plugin/feed/` — PASS. Then refresh the snapshot of the sync settings page if one exists (`WEBTEST_UPDATE=1 go test ./plugin/feed/ -run TestBrowser`), and look at the `.png`.

- [ ] **Step 7: Commit** `feed: signed HLS playlists and byte-range tracks for downloaded items`.

---

### Task 7: Where items play from: ResolveMedia, stream-url, the player fragment

**Files:**
- Modify: `plugin/feed/rpc_media.go`, `plugin/feed/pages.go` (`StreamURL`, `renderPlayerFragment`, `ItemDetail`)
- Test: `plugin/feed/rpc_media_test.go`, `plugin/feed/pages_player_test.go`

**Interfaces:**
- Consumes: `Plugin.HLSMasterURL` (Task 6), `ItemPlayback.Packaged` (Task 3), `Download.PackageState/PackageError/VideoPath`.
- Produces:
  - `ResolveMedia`, VIDEO wanted, packaged with a video track → `MediaSource{Url: <signed master path>, Mime: "application/vnd.apple.mpegurl", Variant: VIDEO}`, with no `ExpiresAt`.
  - `ResolveMedia`, ready download → `items/{id}/media` with `Variant: AUDIO` once packaged (the route now serves the audio track); before packaging, as today.
  - `GET api/stream-url/{id}` (video) → `{"hls": "<signed master path>"}` when packaged; `{"downloading": true, "message": "Preparing the video"}` while ready but pending; `{"error": "Couldn't prepare the video: <PackageError>"}` when packaging failed and there is no progressive file to fall back to (`VideoPath != ""`); a legacy merged download whose packaging failed keeps answering `{"url": ".../media"}`.
  - `ItemPlayback.HLSURL` set by `renderPlayerFragment` and `ItemDetail` when `Packaged`; the templ renders `data-hls={ ip.HLSURL }` and omits `src`.

- [ ] **Step 1: Write the failing tests.**

```go
func TestResolveMediaPackagedVideo(t *testing.T)   // VARIANT_VIDEO → Mime "application/vnd.apple.mpegurl", Url has prefix "/apps/feed/hls/alice/<id>/master.m3u8?sig=", ExpiresAt nil; fetching that Url with no auth → 200
func TestResolveMediaPackagedAudio(t *testing.T)   // VARIANT_AUDIO on the same item → Url ".../items/<id>/media", Variant AUDIO
func TestStreamURLPackaged(t *testing.T)           // {"hls": ...} prefix as above
func TestStreamURLPreparing(t *testing.T)          // ready, package_state '' → downloading true, message "Preparing the video"
func TestStreamURLPackageError(t *testing.T)       // two-file YouTube download with MarkPackageError → {"error": "Couldn't prepare the video: <msg>"}; a single merged LocalPath with MarkPackageError → {"url": ".../media"}
func TestPlayerFragmentHLS(t *testing.T)           // packaged podcast video item: the player HTML has data-hls="/apps/feed/hls/..." and no src= on #player-el; an unpackaged one keeps src
```

Existing tests in `rpc_media_test.go` and `pages_player_test.go` must keep passing unchanged.

- [ ] **Step 2: Run** `go test ./plugin/feed/ -run 'ResolveMedia|StreamURL|PlayerFragment'` — FAIL.

- [ ] **Step 3: Implement** the answers listed under Produces. Keep `queueVideo` for downloads that are not ready. Add the `data-hls` attribute and the conditional `src` in `pages.templ`, then `go generate ./plugin/feed` (or `go tool templ generate`) and `go tool templ fmt .`.

- [ ] **Step 4: Run** `go test ./plugin/feed/...` — PASS.

- [ ] **Step 5: Commit** `feed: packaged items play from their HLS playlist`.

---

### Task 8: Web player on hls.js

**Files:**
- Create: `ui/static/vendor/hls.min.js` (vendored, banner line intact)
- Modify: `plugin/feed/static/player.js`, `plugin/feed/pages.templ` (script tag)
- Test: `plugin/feed/browser_test.go`

**Interfaces:**
- Consumes: `data-hls` (Task 7), `{"hls": ...}` from stream-url (Task 7).
- Produces: `#feed-player` gets `data-hls-state="loaded"` once the manifest is parsed (hls.js `MANIFEST_PARSED`, or `loadedmetadata` when the browser plays HLS natively), and `"error"` with the note shown on a fatal error.

- [ ] **Step 1: Vendor hls.js.** Fetch the latest 1.x `dist/hls.min.js` from `https://cdn.jsdelivr.net/npm/hls.js@1/dist/hls.min.js`. Use the full build, not `hls.light`, because phase 2 needs subtitles. Check that its first line is the version banner (add `/*! hls.js <version> - Apache-2.0 */` if it has none), and name the exact version in the commit message.

- [ ] **Step 2: Write the failing browser test** in `TestBrowser`: a user with a packaged *podcast video* item (fixtures as in Task 6). Navigate to the item, wait for `#feed-player[data-hls-state="loaded"]`, and assert `document.querySelector('#player-el').src` starts with `blob:` (MSE) or contains `master.m3u8` (native). Write no snapshot; the player's DOM is covered elsewhere. The state is the assertion because Chromium builds differ in which codecs they decode.

- [ ] **Step 3: Run** `go test ./plugin/feed/ -run TestBrowser` — FAIL.

- [ ] **Step 4: Implement** in `player.js` a `playHLS(url)` used both for `cfg.hls` (from `data-hls`) and for a stream-url answer with `d.hls`. If `el.canPlayType("application/vnd.apple.mpegurl")`, set `el.src` to the URL. Otherwise, if `window.Hls && Hls.isSupported()`, create `new Hls({ startPosition: cfg.resume > 0 ? cfg.resume : -1 })`, then `loadSource` and `attachMedia`, and `play()` on `MANIFEST_PARSED`. Failing both, `showNote("This browser can't play this video.")`. On a fatal `Hls.Events.ERROR`, show `"Couldn't play: " + data.details` and set the state `"error"`. Destroy the Hls instance when the player is swapped (`swapPlayer`) so a second item does not leak a MediaSource. Move `showNote` out of the YouTube branch so both paths use it. Add a `"Preparing the video"` case: the existing `d.downloading` retry already shows `d.message`. In `pages.templ`, load `ui.Static.URL("vendor/hls.min.js")` with `defer` before `playerAssetURL()`. Ranges (mute and skip markers, seeking past silence) stay as they are; item time and presentation time are the same in this phase.

- [ ] **Step 5: Run** `go test ./plugin/feed/...` — PASS. Then `go tool dev`, play a packaged YouTube video and a podcast in desktop Chrome, and check seek, resume and next. Report what was checked.

- [ ] **Step 6: Commit** `feed: web player plays HLS through hls.js <version>`.

---

### Task 9: Design and TODO

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`, `TODO.md`

- [ ] **Step 1:** In the design, set Status to "phase 1 done (date)". Record this plan's decisions: the track file's `sidx` is the index (nothing in the database), packaging with the mp4 muxer and a global `sidx` in place of the HLS muxer line, indexed tracks used as they are, `local_path` as the audio track, and the URL form with the user in the path and one grant per item presentation. Signed URLs stop being "expiring": links last until the user revokes them (the key in `feed_settings`, the revoke button), so the Signed URLs section drops expiry and the re-signing redirect. Under Open questions, add any new ones the work turned up.
- [ ] **Step 2:** In `TODO.md`, tick phase 1 under "Feed media over HLS" and leave phases 2 to 6 as open sub-items.
- [ ] **Step 3: Run** `go tool verify`. It must pass, or the report says which part did not and why.
- [ ] **Step 4: Commit** `feed: HLS phase 1 in the design and TODO`.

---

## Later plans

Phases 2–6 (captions rendition; mute; skip and the time mapping; undownloaded YouTube from googlevideo after the off-Wi-Fi test; M3U for external players and Android on Media3 HLS) get their own plans once this one has landed, each starting from what this plan recorded in the design.
