# Feed HLS, Phase 2: The Captions Rendition — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an item has captions, they become part of its HLS presentation as a WebVTT rendition: a `SUBTITLES` group in the master, a subtitles playlist, and one VTT segment for each audio segment. Any HLS player can then show them, and the web player gets a CC button.

**Architecture:**
- `plugin/feed/hls` gets a general media-playlist writer (`Entry`, `Playlist`) and master writer (`Presentation`, `Master`). Phases 3–5 build on these. Phase 1's output is pinned byte for byte by goldens before the refactor.
- A new VTT reader in `hls` keeps each cue's text and cleans up YouTube's rolling auto-captions.
- `ServeHLS` serves `subs.m3u8` and `subs-<n>.vtt`. Each VTT segment covers one segment of the audio track and is written per request from `downloads.captions_path`.
- An `X-TIMESTAMP-MAP` ties caption time 0 to the audio track's first fragment.

**Tech Stack:** Go 1.26, `plugin/feed/hls`, cogtest, webtest (Chromium), hls.js 1.7.4 (vendored).

**Spec:** `DESIGN/plugins/feed_hls.md`. The binding cross-phase decisions and interfaces are in `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md`. Read both, and `CLAUDE.md`.

## Global Constraints

- Everything in the roadmap's "Global constraints" applies: worktree `cog/tmp/feed-hls`, branch `feed-hls`, no push. Follow `CLAUDE.md`: errors are never swallowed, tests go through cogtest/`require`/`assert`, check builds with `go install ./...`, format with `go fmt ./...`, and use `go tool templ fmt .` with `go tool templ generate -log-level warn`.
- Captions travel as WebVTT (decided; don't reopen).
- Every URI written into a playlist is relative and carries `?sig=…`. Phase 2 adds no query option.
- Playlists and VTT segments are written per request and never stored. Both are sent `private, no-cache`, because captions can be replaced (re-transcribed) under the same URL.
- Phase-1 playlists must not change by a byte. The only exception is `TARGETDURATION` in the case Task 2 fixes.
- No schema change in this phase.

## Decisions this plan makes (record them in the design in Task 7)

- **One VTT segment per audio segment.** Its duration is the audio segment's, and it holds every cue that overlaps it, with the cue's own times (not clipped). A cue that crosses a boundary appears in both segments. hls.js and Safari drop a cue they already hold (same start, end and text).
- **`X-TIMESTAMP-MAP=MPEGTS:<m>,LOCAL:00:00:00.000`**, where `m = round(audio.Fragments[0].Start × 90000)`. Caption time 0 is the first audio sample, because whisper decodes the audio track and YouTube's captions start at the video's 0. Task 6's browser test proves this in hls.js.
- **YouTube auto-captions are cleaned when they are read, not when they are stored.**
  - Cues shorter than 50 ms are dropped (YouTube's 10 ms "hold" cues).
  - Inline `<…>` tags are stripped.
  - Blank lines are dropped.
  - A cue's leading lines that repeat the end of the previous kept cue are dropped, since rolling captions carry the last line forward.
  - A cue with no text left is dropped.

  The stored file stays as yt-dlp wrote it, because profanity reads it.
- **The rendition is `NAME="Captions"`, `DEFAULT=NO`, `AUTOSELECT=YES`, with no `LANGUAGE`.** Neither source records a language reliably. The SUBTITLES group is listed only when `captions_path != ""`, and then every variant (video and audio-only) names it.
- **`TARGETDURATION` is the largest EXTINF *as printed* (`%.3f`), rounded to the nearest integer.** This fixes phase 1's deferred minor (6.4996 printed as 6.500 against a target of 6) and leaves every other phase-1 playlist unchanged. The roadmap said "ceil"; see Task 1.
- **The web player's CC button appears only on the video player**, once hls.js (or Safari's native text tracks) reports a subtitle track. It is off by default and not saved as a pref.

## Review Focus

1. **The YouTube rolling-caption cleanup must not lose words.**
   - Expected: every word of the fixture shows exactly once, in order, across the cleaned cues.
   - Covered by Task 3 `TestParseVTTYouTubeRolling` (it asserts on the joined text).
2. **The caption timing offset must hold for a track whose first fragment does not start at 0.** AAC priming, or a YouTube track with an `elst`, can do that.
   - Expected: a cue at item time 3 s shows at media time 3 s.
   - Covered by Task 4 `TestVTTSegmentTimestampMap` (Start 1.5 → `MPEGTS:135000`) and Task 6 `TestBrowser/HLSCaptions`.
3. **A segment number that is out of range, negative or not a number** (`subs-99.vtt`, `subs--1.vtt`, `subs-x.vtt`).
   - Expected: 404, not 500 and not a panic.
   - Covered by Task 5 `TestHLSSubsBadSegment`.
4. **A captions file that is missing on disk while `captions_path` is set** (Collect raced, or the cache was wiped).
   - Expected: the master is still served without the SUBTITLES group, a warning is logged, and video keeps playing.
   - Covered by Task 5 `TestHLSCaptionsFileMissing`.
5. **A VTT with Windows line endings, a BOM, cue identifiers, `NOTE` blocks, or `MM:SS.mmm` times** (whisper writes those for files under an hour).
   - Expected: it parses.
   - Covered by Task 3 `TestParseVTTForms`.

---

### Task 1: Reconcile with the code

**Files:** none changed. Rulings go in the ledger.

**Interfaces:**
- Consumes (phase 1, as the roadmap and this plan assume):
  - `hls.Fragment{Start, Duration float64; Offset, Length int64; Keyframe bool}`
  - `hls.Track{Kind; Codecs; Width, Height int; InitOffset, InitLength int64; Fragments []Fragment}`
  - `hls.Segment{Duration float64; Offset, Length int64}`, `hls.Segments(frags []Fragment) []Segment`, unexported `segments(t Track) []Segment` (every audio fragment is a keyframe)
  - `hls.MediaPlaylist(t Track, fileURI string) string`
  - `hls.MasterPlaylist(audio Track, video *Track, audioURI, videoURI string) string`
  - `feed.Space.Track(stored string) (hls.Track, int64, error)`
  - `Space.Own.Open(Name(stored))`
  - `Store.FindDownload(itemID) (Download, error)` with `Download.CaptionsPath`, `PackageState`, `LocalPath`, `VideoPath`
  - `ServeHLS`'s `switch file` and `writePlaylist(w, r, body string)` in `plugin/feed/hls_serve.go`
  - `packagedItem(t, env, username, withVideo) int64`, `masterURL(t, env, username, id) string` and `sibling(master, file) string` in `plugin/feed/hls_test.go`
  - `TestBrowser/HLS` in `plugin/feed/browser_test.go`
  - `playHLS(url)` and `activeHls` in `plugin/feed/static/player.js`

- [ ] **Step 1: Check each Consumes item against the code.** For each one, run `go doc` on the symbol or grep for it, e.g.:

  Run: `go doc ./plugin/feed/hls` and `grep -n "case \"\|func writePlaylist\|func packagedItem\|func masterURL\|func sibling" plugin/feed/hls_serve.go plugin/feed/hls_test.go`
  Expected: every name is present with the signature above.

  Ledger one line per mismatch: `Task 1: Ruling: <expected> vs <found> — <what later tasks use instead>`.

- [ ] **Step 2: Record the roadmap's `TARGETDURATION` conflict.**

  The roadmap writes `TARGETDURATION = ceil of the longest EXTINF as printed`. Ceiling would turn phase 1's `TestMediaPlaylistTargetDuration` (6.4 s → 6) into 7. RFC 8216 §4.3.3.1 only requires the EXTINF *rounded to the nearest integer* to be ≤ the target.

  Ledger: `Task 1: Ruling: roadmap says ceil; use round(printed EXTINF) — keeps phase 1 output, satisfies RFC 8216, fixes the 6.4996 minor — cost if wrong: one strict validator warning`.

---

### Task 2: General playlist writers (`Entry`, `Playlist`, `Presentation`, `Master`) with phase-1 output pinned

**Files:**
- Modify: `plugin/feed/hls/playlist.go`
- Create: `plugin/feed/hls/testdata/golden/{media-audio,media-video,master-video,master-audio}.m3u8`
- Test: `plugin/feed/hls/playlist_test.go`

**Interfaces:**
- Produces (roadmap, binding):
  ```go
  type Segment struct {
      Start, Duration float64 // Start: the first fragment's Start
      Offset, Length  int64
  }
  type Init struct {
      URI            string
      Offset, Length int64
  }
  type Entry struct {
      URI            string
      Offset, Length int64   // Length 0: whole resource, no EXT-X-BYTERANGE
      Duration       float64
      Init           Init    // EXT-X-MAP when it differs from the previous entry's; none while URI == ""
      Discontinuity  bool    // EXT-X-DISCONTINUITY before this entry
  }
  func Playlist(entries []Entry) string
  func TrackEntries(t Track, fileURI string) []Entry
  func TrackSegments(t Track) []Segment // exported form of segments(t); Task 4 and phase 4 need segment times
  func MediaPlaylist(t Track, fileURI string) string // = Playlist(TrackEntries(t, fileURI))

  type Rendition struct {
      Name, Language, URI string
      Default             bool
      Track               *Track // nil for subtitles
  }
  type Presentation struct {
      Audio       []Rendition
      Video       *Track
      VideoURI    string
      Subtitles   []Rendition
      SessionData [][2]string
  }
  func Master(p Presentation) string
  func MasterPlaylist(audio Track, video *Track, audioURI, videoURI string) string // wrapper over Master
  ```
- `Master` rules:
  - Header: `#EXTM3U`, `#EXT-X-VERSION:7`, `#EXT-X-INDEPENDENT-SEGMENTS`.
  - Then one `#EXT-X-SESSION-DATA:DATA-ID=%q,VALUE=%q` per `SessionData` pair.
  - With video, every `Audio` rendition is written as `EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME=…[,LANGUAGE=…],DEFAULT=YES|NO,AUTOSELECT=YES,URI=…`.
  - Every `Subtitles` rendition is written as `EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME=…,DEFAULT=YES|NO,AUTOSELECT=YES,URI=…`.
  - The video `STREAM-INF` adds `,SUBTITLES="subs"` when there are subtitles.
  - The audio-only variant points at the default audio rendition's URI. It names `SUBTITLES="subs"` too, but never `AUDIO=`.
  - Bandwidth and codecs come from the default audio rendition's `Track`.
  - With one audio rendition, no subtitles and no session data, `Master` writes exactly what `MasterPlaylist` wrote in phase 1.

- [ ] **Step 1: Write the golden test, and run it against phase-1 code**

  ```go
  // Phase 1's playlists are pinned byte for byte; WriteGolden=1 rewrites them.
  func TestPhase1Golden(t *testing.T) {
      video := videoTrack()
      for name, got := range map[string]string{
          "media-audio":  hls.MediaPlaylist(audioTrack(), "audio.m4a?sig=x"),
          "media-video":  hls.MediaPlaylist(video, "video.mp4?sig=x"),
          "master-video": hls.MasterPlaylist(audioTrack(), &video, "audio.m3u8?sig=x", "video.m3u8?sig=x"),
          "master-audio": hls.MasterPlaylist(audioTrack(), nil, "audio.m3u8?sig=x", "video.m3u8?sig=x"),
      } {
          golden(t, name, got) // testdata/golden/<name>.m3u8; written when os.Getenv("WRITE_GOLDEN") != ""
      }
  }
  ```

  Run: `WRITE_GOLDEN=1 go test ./plugin/feed/hls -run TestPhase1Golden`, then `go test ./plugin/feed/hls -run TestPhase1Golden`
  Expected: four files are written, then PASS. This is a characterisation test; it passes before the refactor by design. It is the gate for Step 5.

- [ ] **Step 2: Write the failing tests for the new writers**

  ```go
  func TestPlaylistEntries(t *testing.T) {
      pl := hls.Playlist([]hls.Entry{
          {URI: "a.m4a", Offset: 800, Length: 30, Duration: 6, Init: hls.Init{URI: "a.m4a", Length: 700}},
          {URI: "e.mp4", Offset: 0, Length: 0, Duration: 1.5, Init: hls.Init{URI: "e.mp4", Length: 600}, Discontinuity: true},
          {URI: "a.m4a", Offset: 900, Length: 10, Duration: 2, Init: hls.Init{URI: "a.m4a", Length: 700}, Discontinuity: true},
          {URI: "s-3.vtt", Duration: 2},
      })
      assert.Equal(t, 3, strings.Count(pl, "#EXT-X-MAP:"), pl)        // a, e, a again
      assert.Equal(t, 2, strings.Count(pl, "#EXT-X-DISCONTINUITY\n"), pl)
      assert.True(t, strings.Contains(pl, "#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\"e.mp4\",BYTERANGE=\"600@0\"\n#EXTINF:1.500,\ne.mp4\n"), pl) // no BYTERANGE line for Length 0
      assert.True(t, strings.Contains(pl, "#EXTINF:2.000,\ns-3.vtt\n"), pl)
  }

  func TestPlaylistNoInit(t *testing.T) {
      pl := hls.Playlist([]hls.Entry{{URI: "subs-0.vtt", Duration: 6}, {URI: "subs-1.vtt", Duration: 2}})
      assert.False(t, strings.Contains(pl, "#EXT-X-MAP"), pl)
      assert.True(t, strings.Contains(pl, "#EXT-X-TARGETDURATION:6\n"), pl)
  }

  // 6.4996 prints as 6.500, which rounds to 7: the target must be 7.
  func TestMediaPlaylistTargetDurationAsPrinted(t *testing.T) {
      tr := hls.Track{Kind: hls.Video, InitLength: 10, Fragments: offsetsFrom(10, frag(6.4996, true), frag(2, true))}
      assert.True(t, strings.Contains(hls.MediaPlaylist(tr, "v"), "#EXT-X-TARGETDURATION:7\n"))
  }

  func TestTrackSegmentsStart(t *testing.T) {
      segs := hls.TrackSegments(audioTrack()) // 4 × 2 s audio fragments from time 0
      assert.Equal(t, []float64{0, 6}, []float64{segs[0].Start, segs[1].Start})
  }

  func TestMasterSubtitles(t *testing.T) {
      video := videoTrack()
      a := audioTrack()
      pl := hls.Master(hls.Presentation{
          Audio:     []hls.Rendition{{Name: "Original", URI: "audio.m3u8?q", Default: true, Track: &a}},
          Video:     &video, VideoURI: "video.m3u8?q",
          Subtitles: []hls.Rendition{{Name: "Captions", URI: "subs.m3u8?q"}},
      })
      assert.True(t, strings.Contains(pl, `#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Captions",DEFAULT=NO,AUTOSELECT=YES,URI="subs.m3u8?q"`+"\n"), pl)
      assert.Equal(t, 2, strings.Count(pl, `SUBTITLES="subs"`), pl) // video and audio-only variants
  }

  func TestMasterSessionData(t *testing.T) {
      a := audioTrack()
      pl := hls.Master(hls.Presentation{
          Audio:       []hls.Rendition{{Name: "Original", URI: "audio.m3u8", Default: true, Track: &a}},
          SessionData: [][2]string{{"us.duckfam.cog.edits", "abc"}},
      })
      assert.True(t, strings.Contains(pl, `#EXT-X-SESSION-DATA:DATA-ID="us.duckfam.cog.edits",VALUE="abc"`+"\n"), pl)
  }

  // Two audio renditions (phase 3's clean track): both listed, the default
  // one drives the audio-only variant.
  func TestMasterTwoAudio(t *testing.T) {
      video := videoTrack()
      a := audioTrack()
      pl := hls.Master(hls.Presentation{
          Audio: []hls.Rendition{
              {Name: "Original", URI: "audio.m3u8", Track: &a},
              {Name: "Clean", URI: "clean.m3u8", Default: true, Track: &a},
          },
          Video: &video, VideoURI: "video.m3u8",
      })
      assert.Equal(t, 2, strings.Count(pl, "TYPE=AUDIO"), pl)
      assert.True(t, strings.Contains(pl, `NAME="Original",DEFAULT=NO`), pl)
      assert.True(t, strings.Contains(pl, `NAME="Clean",DEFAULT=YES`), pl)
      assert.True(t, strings.HasSuffix(pl, "\nclean.m3u8\n"), pl)
  }
  ```

  Run: `go test ./plugin/feed/hls`
  Expected: FAIL to compile with `undefined: hls.Playlist`, `hls.Entry`, `hls.Master`, `hls.TrackSegments`, `hls.Presentation`.

- [ ] **Step 3: Implement the produced interfaces in `plugin/feed/hls/playlist.go`.**

  - `Segments` sets `Start` from the segment's first fragment.
  - `TrackSegments` is today's `segments`.
  - `TrackEntries` makes one `Entry` per segment, all with `Init{fileURI, t.InitOffset, t.InitLength}`.
  - `Playlist` keeps phase 1's header lines and order.
  - `MasterPlaylist` becomes `Master(Presentation{Audio: []Rendition{{Name: "Original", URI: audioURI, Default: true, Track: &audio}}, Video: video, VideoURI: videoURI})`.

- [ ] **Step 4: Run the package tests**

  Run: `go test ./plugin/feed/hls`
  Expected: PASS, with `TestPhase1Golden` unchanged and every phase-1 test green.

- [ ] **Step 5: Run the feed suite and commit**

  Run: `go test ./plugin/feed/...`
  Expected: ok for every package.

  ```bash
  git add plugin/feed/hls
  git commit -m "feed: HLS playlists from entries and a presentation

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

---

### Task 3: WebVTT reader with YouTube rolling-caption cleanup

**Files:**
- Create: `plugin/feed/hls/vtt.go`
- Create: `plugin/feed/testdata/captions/youtube-auto.vtt` and `plugin/feed/testdata/captions/whisper.vtt`
- Test: `plugin/feed/hls/vtt_test.go`

**Interfaces:**
- Produces:
  ```go
  type Cue struct {
      Start, End float64 // seconds, caption time
      Text       string  // lines joined by "\n", tags stripped
  }
  // ParseVTT reads a WebVTT file. Times are HH:MM:SS.mmm or MM:SS.mmm.
  // An error only for input that is not WebVTT (no "WEBVTT" header) or a
  // read failure; a malformed cue is skipped.
  func ParseVTT(r io.Reader) ([]Cue, error)
  ```
  `plugin/profanity.ParseVTT` stays as it is. Profanity wants whole cues, uncleaned. Leave a note in the code; no change there.

- **Fixture `youtube-auto.vtt`**, modelled on `yt-dlp --write-auto-sub --sub-format vtt` output:

  ```
  WEBVTT
  Kind: captions
  Language: en

  00:00:00.160 --> 00:00:02.270 align:start position:0%
   
  hello<00:00:00.480><c> world</c><00:00:00.960><c> this</c>

  00:00:02.270 --> 00:00:02.280 align:start position:0%
  hello world this
   

  00:00:02.280 --> 00:00:04.590 align:start position:0%
  hello world this
  is<00:00:02.800><c> a</c><00:00:03.120><c> test</c>

  00:00:04.590 --> 00:00:04.600 align:start position:0%
  is a test
   

  00:00:04.600 --> 00:00:07.000 align:start position:0%
  is a test
  of<00:00:05.000><c> captions</c>
  ```

  The blank-looking lines hold a single space, as YouTube writes them.

- **Fixture `whisper.vtt`:**
  - Written with CRLF line endings and a UTF-8 BOM.
  - Has a cue identifier line `1` before the first cue, a `NOTE generated` block, and `MM:SS.mmm` times.
  - Holds two cues: `00:01.000 --> 00:03.000` "First line." and `00:03.000 --> 00:05.500` "Second line."
  - Generate it from a Go test helper or `printf` once and commit it. The CRLF must survive, so add `plugin/feed/testdata/captions/*.vtt -text` to `.gitattributes`.

- [ ] **Step 1: Write the failing tests**

  ```go
  func readCues(t *testing.T, name string) []hls.Cue {
      t.Helper()
      f, err := os.Open("../testdata/captions/" + name)
      require.NoError(t, err)
      t.Cleanup(func() { f.Close() })
      cues, err := hls.ParseVTT(f)
      require.NoError(t, err)
      return cues
  }

  func TestParseVTTYouTubeRolling(t *testing.T) {
      cues := readCues(t, "youtube-auto.vtt")
      texts := make([]string, len(cues))
      for i, c := range cues {
          texts[i] = c.Text
      }
      assert.Equal(t, []string{"hello world this", "is a test", "of captions"}, texts)
      assert.InDelta(t, 0.16, cues[0].Start, 0.0005)
      assert.InDelta(t, 2.27, cues[0].End, 0.0005)
      assert.InDelta(t, 2.28, cues[1].Start, 0.0005)
      // No word lost or doubled.
      assert.Equal(t, "hello world this is a test of captions", strings.Join(strings.Fields(strings.Join(texts, " ")), " "))
  }

  func TestParseVTTForms(t *testing.T) {
      cues := readCues(t, "whisper.vtt")
      assert.Equal(t, 2, len(cues))
      assert.Equal(t, "First line.", cues[0].Text)
      assert.InDelta(t, 1.0, cues[0].Start, 0.0005)
      assert.InDelta(t, 5.5, cues[1].End, 0.0005)
  }

  func TestParseVTTNotVTT(t *testing.T) {
      _, err := hls.ParseVTT(strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nSRT\n"))
      assert.True(t, err != nil, "an SRT file is not WebVTT")
  }

  // A two-line cue that is not rolling keeps both lines.
  func TestParseVTTMultiLine(t *testing.T) {
      cues, err := hls.ParseVTT(strings.NewReader("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nTop\nBottom\n"))
      require.NoError(t, err)
      assert.Equal(t, "Top\nBottom", cues[0].Text)
  }
  ```

  Run: `go test ./plugin/feed/hls -run TestParseVTT`
  Expected: FAIL to compile, `undefined: hls.ParseVTT`.

- [ ] **Step 2: Implement `ParseVTT` in `plugin/feed/hls/vtt.go`.**

  - Strip the BOM, then read with `bufio.Scanner`, trimming `\r`.
  - Skip the header block, any `NOTE`, `STYLE` and `REGION` blocks, and cue identifiers.
  - Drop everything after the end time on the timing line.
  - Apply the cleanup rules from "Decisions": the < 50 ms drop, tag strip, blank-line drop, and the rolling-prefix drop against the previous kept cue's last line.

- [ ] **Step 3: Run the tests**

  Run: `go test ./plugin/feed/hls`
  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add plugin/feed/hls/vtt.go plugin/feed/hls/vtt_test.go plugin/feed/testdata/captions .gitattributes
  git commit -m "feed: read WebVTT cues, folding YouTube's rolling captions

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

---

### Task 4: Segmented WebVTT (`VTTSegment`, subtitle entries)

**Files:**
- Modify: `plugin/feed/hls/vtt.go`
- Test: `plugin/feed/hls/vtt_test.go`

**Interfaces:**
- Consumes: `Cue` and `ParseVTT` (Task 3); `TrackSegments`, `Entry` and `Playlist` (Task 2).
- Produces:
  ```go
  // VTTSegment is one WebVTT segment: the cues overlapping [start, end),
  // with their own times, under an X-TIMESTAMP-MAP that puts caption time 0
  // at MPEG-TS time mpegts (90 kHz).
  func VTTSegment(cues []Cue, start, end float64, mpegts int64) string
  // TimestampMap is the MPEGTS value for a track: its first fragment's Start × 90000, rounded.
  func TimestampMap(t Track) int64
  // SubtitleEntries lists one VTT segment per segment of the audio track; uri(n) names segment n.
  func SubtitleEntries(audio Track, uri func(n int) string) []Entry
  ```
- `VTTSegment` output:
  - It opens `WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:<m>,LOCAL:00:00:00.000\n\n`, then each cue as `HH:MM:SS.mmm --> HH:MM:SS.mmm\n<text>\n\n`.
  - A cue that would write `-->` inside its text writes `--&gt;`.
  - A segment with no cues is the header alone, which is valid.

- [ ] **Step 1: Write the failing tests**

  ```go
  func TestVTTSegmentOverlap(t *testing.T) {
      cues := []hls.Cue{{Start: 1, End: 2, Text: "a"}, {Start: 5.5, End: 7, Text: "b"}, {Start: 8, End: 9, Text: "c"}}
      s0 := hls.VTTSegment(cues, 0, 6, 0)
      s1 := hls.VTTSegment(cues, 6, 12, 0)
      assert.True(t, strings.HasPrefix(s0, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n"), s0)
      assert.True(t, strings.Contains(s0, "00:00:01.000 --> 00:00:02.000\na\n"), s0)
      assert.True(t, strings.Contains(s0, "00:00:05.500 --> 00:00:07.000\nb\n"), s0)  // crosses into s1, unclipped
      assert.True(t, strings.Contains(s1, "00:00:05.500 --> 00:00:07.000\nb\n"), s1)
      assert.False(t, strings.Contains(s0, "\nc\n"), s0)
  }

  func TestVTTSegmentEmpty(t *testing.T) {
      assert.Equal(t, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n", hls.VTTSegment(nil, 0, 6, 0))
  }

  func TestVTTSegmentTimestampMap(t *testing.T) {
      tr := audioTrack()
      for i := range tr.Fragments {
          tr.Fragments[i].Start += 1.5
      }
      assert.Equal(t, int64(135000), hls.TimestampMap(tr))
      assert.True(t, strings.Contains(hls.VTTSegment(nil, 0, 6, hls.TimestampMap(tr)), "MPEGTS:135000,"))
  }

  func TestSubtitleEntries(t *testing.T) {
      es := hls.SubtitleEntries(audioTrack(), func(n int) string { return fmt.Sprintf("subs-%d.vtt?sig=x", n) })
      assert.Equal(t, 2, len(es)) // the 4 × 2 s audio track makes segments of 6 s and 2 s
      assert.Equal(t, "subs-1.vtt?sig=x", es[1].URI)
      assert.InDelta(t, 2.0, es[1].Duration, 1e-9)
      assert.Equal(t, int64(0), es[0].Length)
      assert.Equal(t, "", es[0].Init.URI)
  }
  ```

  Run: `go test ./plugin/feed/hls -run 'TestVTTSegment|TestSubtitleEntries'`
  Expected: FAIL to compile, `undefined: hls.VTTSegment`.

- [ ] **Step 2: Implement the three functions.**

- [ ] **Step 3: Run the tests**

  Run: `go test ./plugin/feed/hls`
  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add plugin/feed/hls
  git commit -m "feed: WebVTT segments cut to the audio track's segments

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

---

### Task 5: Serve the captions rendition

**Files:**
- Modify: `plugin/feed/hls_serve.go`
- Test: `plugin/feed/hls_test.go`

**Interfaces:**
- Consumes: `Master`, `Presentation`, `SubtitleEntries`, `Playlist`, `ParseVTT`, `VTTSegment` and `TimestampMap` (Tasks 2–4).
- Produces:
  - Files `subs.m3u8` and `subs-<n>.vtt` under `/apps/feed/hls/{user}/{item}/`.
  - `master.m3u8` gains the SUBTITLES group when captions exist.
  - Test helper `captionedItem(t, env, username string, withVideo bool, vtt string) int64` in `hls_test.go`. It calls `packagedItem`, puts `vtt` at `captions/<id>.vtt` through `space.Own.Put`, then `store.SetCaptionsPath`.
  - Unexported in `hls_serve.go`: `readCaptions(space Space, stored string) ([]hls.Cue, error)`.
- Behaviour:
  - `master.m3u8` builds `Presentation{Audio: [Original], Video, VideoURI, Subtitles: [{Name: "Captions", URI: "subs.m3u8"+query}]}` when `d.CaptionsPath != ""` and the captions file reads.
  - If that read fails, it logs `slog.Warn("feed hls: captions", "item", id, "err", err)` and leaves `Subtitles` empty.
  - `subs.m3u8` returns 404 when there are no captions.
  - `subs-<n>.vtt` is parsed with `strconv.Atoi` on the suffix. `n < 0`, `n ≥ len(segments)` or a parse error gives 404.
  - The VTT body is served with `Content-Type: text/vtt; charset=utf-8` and `Cache-Control: private, no-cache`. HEAD writes no body.
  - A failed captions read on `subs*` is a 500 through `httperr.Internal`.

- [ ] **Step 1: Write the failing tests**

  ```go
  const testVTT = "WEBVTT\n\n00:00:03.000 --> 00:00:05.000\nHello there\n\n00:00:08.000 --> 00:00:09.000\nLater\n"

  func TestHLSSubtitlesInMaster(t *testing.T) {
      env := cogtest.New(t, feed.New())
      env.CreateUser(t, "alice", "pw")
      plain := packagedItem(t, env, "alice", true)
      captioned := captionedItem(t, env, "alice", true, testVTT)
      body := env.Get(t, masterURL(t, env, "alice", plain)).Status(http.StatusOK).ReadBody()
      assert.False(t, strings.Contains(body, "TYPE=SUBTITLES"), body)
      body = env.Get(t, masterURL(t, env, "alice", captioned)).Status(http.StatusOK).ReadBody()
      assert.True(t, strings.Contains(body, `TYPE=SUBTITLES,GROUP-ID="subs",NAME="Captions",DEFAULT=NO,AUTOSELECT=YES,URI="subs.m3u8?sig=`), body)
      assert.Equal(t, 2, strings.Count(body, `SUBTITLES="subs"`), body)
  }

  func TestHLSSubsPlaylistAndSegments(t *testing.T) {
      env := cogtest.New(t, feed.New())
      env.CreateUser(t, "alice", "pw")
      id := captionedItem(t, env, "alice", false, testVTT)
      m := masterURL(t, env, "alice", id)
      pl := env.Get(t, sibling(m, "subs.m3u8")).Status(http.StatusOK).ReadBody()
      assert.True(t, strings.Contains(pl, "\nsubs-0.vtt?sig="), pl)
      assert.False(t, strings.Contains(pl, "#EXT-X-MAP"), pl)
      r := env.Get(t, sibling(m, "subs-0.vtt")).Status(http.StatusOK)
      assert.Equal(t, "text/vtt; charset=utf-8", r.Header.Get("Content-Type"))
      assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
      seg := r.ReadBody()
      assert.True(t, strings.Contains(seg, "X-TIMESTAMP-MAP=MPEGTS:"), seg)
      assert.True(t, strings.Contains(seg, "00:00:03.000 --> 00:00:05.000\nHello there\n"), seg)
      // The 12 s audio fixture has 2 s fragments: segments 0–6 s and 6–12 s.
      seg1 := env.Get(t, sibling(m, "subs-1.vtt")).Status(http.StatusOK).ReadBody()
      assert.True(t, strings.Contains(seg1, "Later"), seg1)
      assert.False(t, strings.Contains(seg1, "Hello there"), seg1)
  }

  func TestHLSSubsBadSegment(t *testing.T) {
      env := cogtest.New(t, feed.New())
      env.CreateUser(t, "alice", "pw")
      id := captionedItem(t, env, "alice", false, testVTT)
      m := masterURL(t, env, "alice", id)
      for _, f := range []string{"subs-99.vtt", "subs--1.vtt", "subs-x.vtt", "subs-.vtt"} {
          env.Get(t, sibling(m, f)).Status(http.StatusNotFound)
      }
      env.Get(t, sibling(masterURL(t, env, "alice", packagedItem(t, env, "alice", false)), "subs.m3u8")).Status(http.StatusNotFound)
  }

  func TestHLSSubsNeedSignature(t *testing.T) {
      env := cogtest.New(t, feed.New())
      env.CreateUser(t, "alice", "pw")
      id := captionedItem(t, env, "alice", false, testVTT)
      bad := regexp.MustCompile(`sig=[^&]+`).ReplaceAllString(sibling(masterURL(t, env, "alice", id), "subs-0.vtt"), "sig=AAAA")
      env.Get(t, bad).Status(http.StatusForbidden)
  }

  func TestHLSCaptionsFileMissing(t *testing.T) {
      env := cogtest.New(t, feed.New())
      env.CreateUser(t, "alice", "pw")
      id := captionedItem(t, env, "alice", true, testVTT)
      space, err := feedPlugin(t, env).Space("alice")
      require.NoError(t, err)
      require.NoError(t, space.Own.Remove("captions/"+strconv.FormatInt(id, 10)+".vtt"))
      body := env.Get(t, masterURL(t, env, "alice", id)).Status(http.StatusOK).ReadBody()
      assert.False(t, strings.Contains(body, "TYPE=SUBTITLES"), body)
      assert.True(t, strings.Contains(body, "\nvideo.m3u8?sig="), body)
  }
  ```

  Run: `go test ./plugin/feed -run 'TestHLSSub|TestHLSCaptions'`
  Expected: FAIL. `TestHLSSubtitlesInMaster` finds no `TYPE=SUBTITLES`; `subs.m3u8` is 404 where 200 is wanted.

- [ ] **Step 2: Implement it in `ServeHLS`.**
  - Move `master.m3u8` to `hls.Master`.
  - Add the cases `subs.m3u8` and `strings.HasPrefix(file, "subs-") && strings.HasSuffix(file, ".vtt")`.
  - Add `readCaptions` and a `writeVTT` that sits beside `writePlaylist`, with the same HEAD and `Content-Length` handling.

- [ ] **Step 3: Run the tests**

  Run: `go test ./plugin/feed -run TestHLS`
  Expected: PASS, the phase-1 `TestHLS*` included.

- [ ] **Step 4: Commit**

  ```bash
  git add plugin/feed/hls_serve.go plugin/feed/hls_test.go
  git commit -m "feed: HLS presentations carry captions as a WebVTT rendition

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

---

### Task 6: Captions in the web player

**Files:**
- Modify: `plugin/feed/pages.templ` (video player bar: a CC button), `plugin/feed/pages_templ.go` (regenerated), `plugin/feed/static/player.js`
- Test: `plugin/feed/browser_test.go` (subtest `HLSCaptions`), and goldens under `plugin/feed/testdata/snapshots` (DOM only)

**Interfaces:**
- Consumes: the `subs.m3u8` rendition (Task 5), `captionedItem` (Task 5; the browser test calls it the way `packagedItem` is called today), `playHLS` and `activeHls` (phase 1).
- Produces:
  - `<button id="player-cc" class="btn btn-outline btn-sm" title="Captions" type="button" aria-pressed="false" hidden>CC</button>`, in the video player's bar only, before `#player-speed`.
  - `player.js`:
    - On `Hls.Events.SUBTITLE_TRACKS_UPDATED` with at least one track, it unhides `#player-cc`.
    - A click toggles `hls.subtitleDisplay = on` and `hls.subtitleTrack = on ? 0 : -1`, and sets `aria-pressed`.
    - On the native path (Safari), it uses `el.textTracks[0].mode = on ? "showing" : "disabled"` once `el.textTracks.length > 0`.
    - `new Hls({…, subtitleDisplay: false})`.
    - `root.dataset.captions` is `"on"` or `"off"` as the button state, for the test.

- [ ] **Step 1: Write the failing browser test** (next to `t.Run("HLS", …)`, with the user created in the setup block alongside `hls`)

  ```go
  env.CreateUser(t, "hlscc", "pass")
  ccItem := captionedItemFor(t, env, "hlscc", true, "WEBVTT\n\n00:00:03.000 --> 00:00:05.000\nHello there\n")

  // Captions arrive as an HLS subtitle rendition. The CC button turns them on,
  // and hls.js places the cue at the item time it names: the X-TIMESTAMP-MAP
  // ties caption time 0 to the audio track's first fragment.
  t.Run("HLSCaptions", func(t *testing.T) {
      b := eng.Start(t)
      b.Login(t, "hlscc", "pass")
      b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", ccItem))
      b.WaitVisible(t, `#feed-player[data-hls-state="loaded"]`)
      b.WaitVisible(t, `#player-cc:not([hidden])`)
      b.Click(t, "#player-cc")
      b.WaitVisible(t, `#feed-player[data-captions="on"]`)
      b.WaitEval(t, `(() => { const t = document.querySelector("#player-el").textTracks; for (const x of t) if (x.cues && x.cues.length) return true; return false })()`)
      start, _ := b.Eval(t, `(() => { for (const x of document.querySelector("#player-el").textTracks) if (x.cues && x.cues.length) return x.cues[0].startTime })()`).(float64)
      assert.InDelta(t, 3.0, start, 0.1)
  })
  ```

  - `captionedItemFor` is the browser test's copy of `captionedItem`, since `browser_test.go` and `hls_test.go` are both `package feed_test`. Reuse `captionedItem` itself if it compiles.
  - If `webtest` has no `WaitEval`, poll `b.Eval` the way the other subtests wait, and ledger it.

  Run: `go test ./plugin/feed -run 'TestBrowser/HLSCaptions'`
  Expected: FAIL: timeout waiting for `#player-cc:not([hidden])`. The page dump in the cache dir shows no `#player-cc`.

- [ ] **Step 2: Add the button to `pages.templ`, regenerate, and implement the toggle in `player.js`.**

  Run: `go tool templ fmt . && go tool templ generate -log-level warn`

- [ ] **Step 3: Run the browser test**

  Run: `go test ./plugin/feed -run 'TestBrowser/HLSCaptions'`
  Expected: PASS, start ≈ 3.0.

  If the cue lands at 3.0 + offset, the MPEGTS rule is wrong for hls.js's fMP4 path. Change `TimestampMap` (for example to `MPEGTS:0` when hls.js already applies the init offset), keep `TestVTTSegmentTimestampMap` in step with it, and ledger the ruling.

- [ ] **Step 4: Refresh goldens the button changed.** The hidden `#player-cc` is in the video player's DOM.

  Run: `WEBTEST_UPDATE=1 go test ./plugin/feed -run TestBrowser`, then `git diff --stat plugin/feed/testdata/snapshots`
  Expected: only video-player snapshots change, and each `.html` diff is the one `<button id="player-cc" … hidden>` line.

- [ ] **Step 5: Run the feed suite and commit**

  Run: `go test ./plugin/feed/...`
  Expected: ok.

  ```bash
  git add plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/static/player.js plugin/feed/browser_test.go plugin/feed/testdata/snapshots
  git commit -m "feed: web player shows HLS captions behind a CC button

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```

---

### Task 7: Design doc and TODO

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`, `TODO.md`

- [ ] **Step 1: Update the design.**
  - Status becomes "phases 1–2 done (<date>)".
  - In "Sources and storage → Captions", record this plan's "Decisions":
    - one VTT segment per audio segment, with unclipped cues;
    - the MPEGTS rule as Task 6 left it;
    - cleanup when read;
    - `NAME="Captions"` with no `LANGUAGE`;
    - the CC button.
  - Note under "Open questions" that a captions language is not recorded.
  - Keep "whether mpv shows WebVTT renditions" open, unless it was tried (`mpv '<master URL>'` with `--sid=1`); if so, record the result.

- [ ] **Step 2: Tick "Phase 2: the captions rendition" in `TODO.md`**, naming the tests: `TestParseVTT*`, `TestVTTSegment*`, `TestHLSSub*` and `TestBrowser/HLSCaptions`.

- [ ] **Step 3: Verify and commit**

  Run: `go tool verify -continue`
  Expected: generate, fmt, templ fmt, vet and tests all pass.
  - verify's `go fix` may rewrite files this branch never touched. Revert those, as in phase 1.
  - A `cmd/cog TestBrowserCore` timeout under load is known. Rerun it alone and report it.

  ```bash
  git add DESIGN/plugins/feed_hls.md TODO.md
  git commit -m "feed: HLS phase 2 in the design and TODO

  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
  ```
