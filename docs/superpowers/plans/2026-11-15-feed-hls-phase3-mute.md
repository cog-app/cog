# Feed HLS, Phase 3: Mute (the Clean Rendition) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Any HLS player, including mpv and the web player on video, gets profanity-cleaned audio by default. The clean audio is a second audio rendition of the item's presentation, the original stays selectable, and when clean audio exists the captions are masked.

**Architecture:**
- The profanity worker keeps making the cleaned audio as a whole re-encoded file, as it does today.
- A new packager pass, `Packager.RunCleanOnce`, runs after the profanity worker. It makes the cleaned file a fragmented MP4 audio track with a `sidx`, reusing phase 1's `remux` and copying the audio when it is AAC.
- The master lists two `EXT-X-MEDIA TYPE=AUDIO` renditions: Clean (`DEFAULT=YES`) and Original.
- `clean.m3u8` and `clean.m4a` serve the clean track the way `audio.m3u8` and `audio.m4a` serve the original.
- Captions go through `profanity.FilterText` when there is a clean track.
- A cleaned video now plays over HLS everywhere. It no longer drops to clean audio only.

**Tech Stack:** Go 1.26, SQLite (per-user plugin DB), ffmpeg/ffprobe through `internal/sandbox`, templ, hls.js 1.7.4, cogtest and webtest.

**Spec:** `DESIGN/plugins/feed_hls.md` § Mute. The roadmap `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md` is binding; see decisions 1 and 2. Read both, and `CLAUDE.md`, first.

## Global Constraints

- **Workspace:** work in the worktree `cog/tmp/feed-hls` on branch `feed-hls`. Phase 2 must already be committed there. Do not push; ask first.
- **Errors:** never swallow them; wrap and return, or log. Tests go through `cogtest`, using `internal/testing/require` and `assert`, never testify.
- **Programs and files:**
  - Programs run through `sandbox.CommandContext` with the narrowest `Spec`, and ffmpeg inputs go through `ffargs.Input`.
  - Files go through `feed.Space` (`Own`, `Cache`, `Keep`). Never build a path.
- **Schema:** changes go in `sql/migration006_clean_track.sql`. Regenerate `sql/schema.sql` with `go generate ./plugin/feed` (it may need hand-tidying, as in phase 1).
- **Tooling:**
  - Format with `go fmt ./...` and `go tool templ fmt .`.
  - Regenerate templ with `go tool templ generate -log-level warn`.
  - Check builds with `go install ./...`.
- **Roadmap decision 1:** clean audio is a **whole track**, `media/<id>.hls-audio.clean.m4a`, not spliced segments. Audio is copied when it is AAC, and otherwise encoded with `-c:a aac -b:a 128k`.
- **Roadmap decision 2:**
  - There is **no `mute` query option**. `ServeHLS` keeps rejecting every parameter except `sig`.
  - The presence of a clean track is the switch, and that track is `DEFAULT=YES`.
  - The original rendition is always listed as well.
- **Rendition names** are exactly `Clean` and `Original`. With one audio rendition, the name stays phase 1's `Original`.
- **No new options** means no change to `hlsSignature` or `HLSMasterURL` in this phase.
- **Commit trailers.** Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX
  ```

## Decisions this plan makes (record them in the design in Task 8)

- **The packager makes the clean track, not the profanity worker.** The packager already owns "file → track" (probe, transcode or copy, `remux`, index check, error state). The profanity worker stays a "file → cleaned file" step, and it keeps working for downloads that were never packaged.
  - `RunCleanOnce` is wired after the profanity worker, so a new clean file becomes a track in the same tick.
  - It also picks up clean files made before this phase, including ones cleaned from a merged MP4, which carry a video stream.
- **The cleaned file's name no longer depends on the track's.** The profanity worker keeps its output as `media/<id>.clean<ext of local_path>`, for example `media/7.clean.m4a`, and never as `media/<id>.hls-audio.clean.m4a`.
  - Re-cleaning therefore never overwrites a live clean track with a plain file while a player reads it.
  - Once the track is kept, `clean_audio_path` is set to the track as well. The plain file then belongs to no download, and `Collect` removes it.
  - Packaged tracks play progressively too (phase 1), so `items/{id}/media` and the audio path of `ResolveMedia` keep serving `clean_audio_path` unchanged.
- **`SetCleanAudioPath` clears `clean_track_path` and `clean_track_error`.** A new clean file is not a track until the packager has seen it.
- **A cleaned video whose clean track is not ready plays clean audio only, as today.** This covers a clean track that is still pending and one that failed to package. It does not play HLS with unclean audio. `videoPlayOf` answers `playFile` for it.
- **No ranges means no clean audio.**
  - `profanity.(*Plugin).CleanAudioFile` returns `profanity.ErrNoRanges` when the word list is empty or matches nothing.
  - The worker then clears the flag and clears `clean_audio_path`, so a re-clean that no longer finds anything drops the old clean audio.
  - This fixes the retry every tick that happens today, when `Keep` fails on a file that was never written.
- **For an audio-only item with a clean track,** the master has the audio group and one `EXT-X-STREAM-INF` with `AUDIO="audio"`, whose URI is the default (clean) rendition's playlist. A cleaned video also keeps its audio-only variant, which points at `clean.m3u8`.
- **Captions are masked whenever the download has a clean track**, whichever audio rendition is playing. There is a single subtitles rendition. Two of them (masked and plain) are not worth it until there is a mute option.
- **The web player reports the audio rendition it plays** in `data-hls-audio`, taking the name from hls.js's current audio track. The browser test reads it, and a later Original/Clean switch would use it.

## Review Focus

1. **Clean audio made before this phase**, from a merged MP4 that carries video (`-c:v copy`) or from an MP3 (`libmp3lame`). It must still become an audio-only AAC track, with the video stream ignored and MP3 transcoded. Covered by Task 4 `TestPackageCleanTrackFromMerged` and `TestPackageCleanTrackFromMP3`.
2. **Re-cleaning after the word list changes.** The new clean track must replace the old one. The old plain and track files must be gone after `Collect`, and nothing may serve a non-track file as `clean.m4a` in between. Covered by Task 4 `TestRecleanReplacesTrack`.
3. **A clean file the packager cannot read.** The item must keep playing clean audio progressively, and must not be retried every tick. Covered by Task 4 `TestPackageCleanTrackFailure` and Task 7 `TestResolveMediaCleanPendingPlaysCleanAudio`.
4. **hls.js picks the `DEFAULT=YES` Clean rendition**, not the first one listed or the one whose name sorts first. Covered by Task 7 `TestBrowser/HLSClean`.
5. **An empty word list or no matches** clears the flag once, does not call `Keep`, and removes stale clean audio. Covered by Task 2 `TestProfanityWorkerNoRanges*`.

---

### Task 1: Reconcile with phase 2 as built

**Files:** none changed; ledger only.

**Interfaces:**
- Consumes, from the roadmap and phase 2: `hls.Rendition{Name, Language, URI string; Default bool; Track *Track}`, `hls.Presentation{Audio []Rendition; Video *Track; VideoURI string; Subtitles []Rendition; SessionData [][2]string}`, `hls.Master(Presentation) string`, `hls.Cue{Start, End float64; Text string}`, `hls.VTTSegment(cues []Cue, start, end float64, mpegts int64) string`, and the `subs-<n>.vtt` branch of `ServeHLS`.

- [ ] **Step 1:** Run `go doc ./plugin/feed/hls` and read `plugin/feed/hls_serve.go`. For each name above, write one ledger row: what is expected, what exists, and the same or the mismatch. Name the function or block where `ServeHLS` gets cues for `subs-<n>.vtt`, because Task 6 masks text there. Record each mismatch as `Task 1: Ruling: …`, adapting later tasks to what exists.
- [ ] **Step 2:** Run `go test ./plugin/feed/...`.
  Expected: ok. Phase 2 has landed and is green.

### Task 2: No ranges means no clean audio; the cleaned file's own name

**Files:**
- Modify: `plugin/profanity/profanity.go`, the `CleanAudioFile` method (around lines 94–102).
- Modify: `plugin/feed/pipeline/profanity.go`: `processOne`; replace `cleanPath` with `cleanName`.
- Test: `plugin/feed/pipeline/profanity_test.go`, `plugin/feed/pipeline/package_internal_test.go` (internal, for `cleanName`).

**Interfaces:**
- Produces:
  ```go
  package profanity
  // ErrNoRanges: the word list is empty or matches none of the captions, so
  // there is nothing to clean and no file was written.
  var ErrNoRanges = errors.New("profanity: nothing to mute")

  package pipeline
  func cleanName(itemID int64, localPath string) string // "media/<id>.clean" + path.Ext(localPath)
  ```

- [ ] **Step 1: Write the failing tests.**
  - `TestProfanityWorkerNoRangesClearsFlag`:
    - Set up a processor `noRanges` whose `Process` returns `fmt.Errorf("wrapped: %w", profanity.ErrNoRanges)` and counts its calls.
    - Make an item with captions and a ready download, then `EnqueueProfanity`.
    - Call `RunOnce` twice.
    - Assert `err == nil`, `calls == 1`, `GetDownload(id).NeedsProfanity == false` and `CleanAudioPath == ""`.
  - `TestProfanityWorkerNoRangesDropsOldClean`:
    - Same setup, plus `SetCleanAudioPath(id, "media/<id>.clean.m4a")` with that file put in `Own`.
    - After `RunOnce` and `Collect`: `CleanAudioPath == ""`, and `space.Own.Open("media/<id>.clean.m4a")` fails with `fs.ErrNotExist`.
  - Extend `TestProfanityWorkerUsesCaptions` with `assert.Equal(t, "media/<id>.clean.m4a", d.CleanAudioPath)`, where `local_path` is `media/<id>.hls-audio.m4a`.
  - `TestCleanName`:
    - `cleanName(7, "media/7.hls-audio.m4a") == "media/7.clean.m4a"`
    - `cleanName(7, "media/7.mp3") == "media/7.clean.mp3"`
    - `cleanName(7, "media/7") == "media/7.clean"`
- [ ] **Step 2:** Run `go test ./plugin/feed/pipeline -run 'TestProfanityWorker|TestCleanName'`.
  Expected: FAIL. `profanity.ErrNoRanges` and `cleanName` are undefined (a compile failure is the RED here).
- [ ] **Step 3: Implement.**
  - `CleanAudioFile` returns `ErrNoRanges` in both of its early `return nil` cases.
  - `processOne`:
    - On `errors.Is(err, profanity.ErrNoRanges)`, call `SetCleanAudioPath(itemID, "")` then `ClearNeedsProfanity(itemID)`, and return their errors joined.
    - Remove the cache tmp only if it exists. Check what `Cache.Remove` does with a missing name. It must not turn this path into an error.
    - Otherwise keep the output under `cleanName(itemID, d.LocalPath)`.
- [ ] **Step 4:** Run `go test ./plugin/feed/pipeline ./plugin/profanity`.
  Expected: ok.
- [ ] **Step 5:** Commit: `feed: no profanity to mute leaves no clean audio and is not retried`.

### Task 3: Store the clean track

**Files:**
- Create: `plugin/feed/sql/migration006_clean_track.sql`.
- Regenerate: `plugin/feed/sql/schema.sql`.
- Modify: `plugin/feed/store.go` (`Download`, the `GetDownload`/`FindDownload` scans, `SetCleanAudioPath`, `DownloadFiles`, `GetItemPlayback`, `ItemPlayback`).
- Modify: `plugin/feed/store_media.go` (new methods).
- Test: `plugin/feed/store_media_test.go`, `plugin/feed/pipeline/package_test.go`.

**Interfaces:**
- Produces:
  ```go
  // migration006: ALTER TABLE downloads ADD COLUMN clean_track_path TEXT NOT NULL DEFAULT '';
  //               ALTER TABLE downloads ADD COLUMN clean_track_error TEXT NOT NULL DEFAULT '';
  type Download struct { ...; CleanTrackPath, CleanTrackError string }
  type ItemPlayback struct { ...; CleanPending bool } // clean_audio_path != '' AND clean_track_path = ''
  func (s *Store) SetCleanAudioPath(itemID int64, path string) error            // now also clears clean_track_path, clean_track_error
  func (s *Store) PendingCleanPackage(limit int) ([]int64, error)              // package_state='done' AND clean_audio_path!='' AND clean_track_path='' AND clean_track_error='' ORDER BY item_id
  func (s *Store) SaveCleanTrack(itemID int64, path string) error              // clean_track_path = clean_audio_path = path, clean_track_error = ''
  func (s *Store) MarkCleanTrackError(itemID int64, msg string) error
  ```
  `DownloadFiles` adds `UNION SELECT clean_track_path … WHERE clean_track_path != ''`.

- [ ] **Step 1: Write the failing tests.**
  - `TestCleanTrackStore`, over one packaged download:
    - `SetCleanAudioPath(id, "media/1.clean.m4a")` → `PendingCleanPackage(10) == [id]`, and `GetItemPlayback(id).CleanPending == true`.
    - `SaveCleanTrack(id, "media/1.hls-audio.clean.m4a")` → `CleanTrackPath` and `CleanAudioPath` both equal that path, the pending list is empty, and `CleanPending == false`.
    - `SetCleanAudioPath(id, "media/1.clean.m4a")` → `CleanTrackPath == ""`, and the item is pending again.
    - `MarkCleanTrackError(id, "bad")` → not pending, and `CleanTrackError == "bad"`.
    - A download whose `package_state` is not `done` is never pending.
  - `TestCollectKeepsCleanTrack`:
    - Put `media/<id>.hls-audio.clean.m4a` and set it with `SaveCleanTrack`.
    - After `Collect`, the file still opens.
- [ ] **Step 2:** Run `go test ./plugin/feed ./plugin/feed/pipeline -run 'TestCleanTrackStore|TestCollectKeepsCleanTrack'`.
  Expected: FAIL to compile.
- [ ] **Step 3: Implement.** Write the migration and the store methods above, then run `go generate ./plugin/feed`.
- [ ] **Step 4:** Run `go test ./plugin/feed/...`.
  Expected: ok. `TestSchema*` passes with the regenerated schema.
- [ ] **Step 5:** Commit: `feed: record a download's clean audio track`.

### Task 4: Package the clean audio as a track

**Files:**
- Modify: `plugin/feed/testdata/media/make.sh`, and rerun it. Add `plain-audio.m4a`: AAC 32k, 12 s, a 440 Hz sine, `-movflags +faststart` (not fragmented), standing for the profanity worker's output.
- Modify: `plugin/feed/pipeline/package.go`: add `RunCleanOnce` and `packClean`, reusing `track`, `asIs`, `probe` and `remux`.
- Modify: `plugin/feed/orchestrator/orchestrator.go`: call `RunCleanOnce` after the profanity block, whether or not `prof` is nil.
- Test: `plugin/feed/pipeline/package_test.go`.

**Interfaces:**
- Consumes: Task 3's store methods; phase 1's `packageArgs(in, out, hls.Audio, transcode)` and `(*Packager).remux(ctx, *track)`.
- Produces:
  ```go
  // RunCleanOnce makes every packaged download's cleaned audio an HLS audio
  // track, media/<id>.hls-audio.clean.m4a.
  func (p *Packager) RunCleanOnce(ctx context.Context) error
  ```
  The track's `cached` is `package/<id>.hls-audio.clean.m4a`, so `remux` keeps it as `media/<id>.hls-audio.clean.m4a`. Transcode when the probed audio codec is not `aac`. On failure, call `MarkCleanTrackError` and remove what was kept, as `pack` does.

- [ ] **Step 1: Write the failing tests.** In each, `needFFmpeg(t)`, then a packaged item (phase 1's helper) whose clean file is set with `SetCleanAudioPath`.
  - `TestPackageCleanTrack`:
    - The clean file is `plain-audio.m4a`.
    - After `RunCleanOnce`: `CleanTrackPath == CleanAudioPath == "media/<id>.hls-audio.clean.m4a"`.
    - `space.Track(CleanTrackPath)` gives `Kind == hls.Audio`, `Codecs == "mp4a.40.2"`, and a total duration within 0.1 of 12.
    - After `Collect`, the plain `media/<id>.clean.m4a` is gone.
  - `TestPackageCleanTrackFromMP3`: the clean file is `episode.mp3` (with cover). It gives an audio track `mp4a.40.2`, and `Kind == hls.Audio`.
  - `TestPackageCleanTrackFromMerged`: the clean file is `merged.mp4`, which carries video. It gives an audio track and no video.
  - `TestPackageCleanTrackFailure`:
    - The clean file is 1 KB of zeros.
    - After `RunCleanOnce`: `CleanTrackError != ""`, `CleanAudioPath` is unchanged, and `PendingCleanPackage` is empty.
    - No `media/<id>.hls-audio.clean.m4a` exists.
  - `TestRecleanReplacesTrack`:
    1. Run the profanity worker with a processor that copies `plain-audio.m4a`, then `RunCleanOnce`, then `Collect`.
    2. Record the track's size. Call `EnqueueProfanity` again, with a processor that copies `episode.mp3` (a different length, 6 s).
    3. Run the profanity worker. Assert `CleanTrackPath == ""`: the old track is not served as the new clean audio.
    4. Run `RunCleanOnce` and `Collect`.
    5. The track now lasts about 6 s (within 0.1). `Own.ReadDir("media")` lists no `*.clean.m4a` besides the track, and no `*.clean.mp3`.
- [ ] **Step 2:** Run `go test ./plugin/feed/pipeline -run 'TestPackageCleanTrack|TestRecleanReplacesTrack'`.
  Expected: FAIL, because `RunCleanOnce` is undefined.
- [ ] **Step 3: Implement** `RunCleanOnce` and `packClean`, and wire the orchestrator.
- [ ] **Step 4:** Run `go test ./plugin/feed/...`.
  Expected: ok, with no ffmpeg tests skipped. Check the `-v` output for `SKIP`.
- [ ] **Step 5:** Commit: `feed: package cleaned audio as an HLS audio track`.

### Task 5: Master with a clean rendition

**Files:**
- Modify: `plugin/feed/hls/playlist.go` (`Master`).
- Test: `plugin/feed/hls/playlist_test.go`.

**Interfaces:**
- Consumes: `hls.Presentation` and `hls.Rendition` (Task 1).
- Produces the same `Master` signature, with this behaviour:
  - With two or more `Audio` renditions, every one is written as `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME=%q,DEFAULT=YES|NO,AUTOSELECT=YES,URI=%q`. Only the rendition with `Default` gets `DEFAULT=YES`.
  - Every `EXT-X-STREAM-INF` names `AUDIO="audio"`, and the audio-only variant's URI is the default rendition's URI.
  - BANDWIDTH and CODECS use the largest of the renditions.
  - With one audio rendition and no video, the output is exactly what phase 2 writes (that test stays unchanged).

- [ ] **Step 1: Write the failing tests.**
  - `TestMasterCleanRendition`: video and audio fixtures; `Audio = [{Name:"Clean", URI:"clean.m3u8?s", Default:true, Track:&a}, {Name:"Original", URI:"audio.m3u8?s", Track:&a}]`. Assert each of these appears:
    - `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Clean",DEFAULT=YES,AUTOSELECT=YES,URI="clean.m3u8?s"`
    - `NAME="Original",DEFAULT=NO`
    - a video `EXT-X-STREAM-INF` containing `AUDIO="audio"`
    - an audio-only `EXT-X-STREAM-INF` containing `AUDIO="audio"` and followed by `clean.m3u8?s`
    - exactly one `DEFAULT=YES` (`strings.Count`)
  - `TestMasterAudioOnlyRenditions`: same, with no video. One `EXT-X-STREAM-INF` line, which has `AUDIO="audio"` and is followed by `clean.m3u8?s`, and two `EXT-X-MEDIA:TYPE=AUDIO` lines.
- [ ] **Step 2:** Run `go test ./plugin/feed/hls -run TestMaster`.
  Expected: FAIL; the assertions name the missing lines.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `go test ./plugin/feed/hls`.
  Expected: ok.
- [ ] **Step 5:** Commit: `feed: HLS master lists clean and original audio`.

### Task 6: Serve the clean rendition; mask the captions

**Files:**
- Modify: `plugin/feed/hls_serve.go`.
- Test: `plugin/feed/hls_test.go`.

**Interfaces:**
- Consumes: `Download.CleanTrackPath`, `hls.Master`, `(*Plugin).Profanity() *profanity.Plugin` (it may be nil), and `(*profanity.Plugin).FilterText(username, text string) string`.
- Produces these routes:
  - `clean.m3u8`: `hls.MediaPlaylist(cleanTrack, "clean.m4a"+query)`.
  - `clean.m4a`: `ServeContent` with `audio/mp4` and `private, max-age=86400`. Factor out the existing `audio.m4a`/`video.mp4` branch, so the three share one helper `serveTrackFile(w, r, space, stored, mime)`.
  - Both answer 404 when `CleanTrackPath == ""`.
  - In `master.m3u8`, with a clean track: Clean (default) first, then Original.
  - In `subs-<n>.vtt`, with a clean track and a non-nil `Profanity()`: each cue's `Text` goes through `FilterText(username, text)` before `VTTSegment`.

- [ ] **Step 1: Write the failing tests.** Add the helper `cleanItem(t, env, user, withVideo) int64`: phase 1's `packagedItem`, plus `audio.m4a` put as `media/<id>.hls-audio.clean.m4a` and `SaveCleanTrack`.
  - `TestHLSCleanRendition`: the master body contains `NAME="Clean",DEFAULT=YES` and `URI="clean.m3u8?sig=`, and `NAME="Original",DEFAULT=NO` with `URI="audio.m3u8?sig=`.
  - `TestHLSCleanMediaAndRange`:
    - `clean.m3u8` contains `clean.m4a?sig=` and `#EXT-X-MAP:URI="clean.m4a?sig=`.
    - A GET of `clean.m4a` with `Range: bytes=0-99` gives 206, `Content-Type: audio/mp4` and a 100-byte body.
  - `TestHLSNoCleanTrack`: on a phase-1 `packagedItem`, `clean.m3u8` and `clean.m4a` give 404, and the master has no `NAME="Clean"`.
  - `TestHLSCaptionsMaskedWhenClean`:
    - Use `cogtest.New(t, feed.New(), profanity.New())`.
    - Save the word list `darn` through the profanity plugin's settings action (`POST /apps/profanity/settings`; read `plugin/profanity/pages.go` for the form field).
    - Store captions whose cue at 1.000–2.000 reads `well darn it` and `SetCaptionsPath`.
    - `subs-0.vtt` of the clean item contains no `darn`, and contains the masked form `FilterText` gives (assert on `FilterText`'s own output for the same word list, not a literal).
    - The same captions on a `packagedItem` with no clean track keep `darn`.
- [ ] **Step 2:** Run `go test ./plugin/feed -run 'TestHLSClean|TestHLSNoCleanTrack|TestHLSCaptionsMasked'`.
  Expected: FAIL. There is no `NAME="Clean"`, `clean.m3u8` gives 404, and `darn` is present.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `go test ./plugin/feed/...`.
  Expected: ok, including every phase 1 and phase 2 `TestHLS*`.
- [ ] **Step 5:** Commit: `feed: serve the clean audio rendition and mask its captions`.

### Task 7: Cleaned video plays HLS

**Files:**
- Modify: `plugin/feed/rpc_media.go` (`ResolveMedia`, `videoPlayOf`).
- Modify: `plugin/feed/pages.go` (`StreamURL`, `setHLSURL`).
- Modify: `plugin/feed/static/player.js` (`playHLS`).
- Test: `plugin/feed/rpc_media_test.go`, `plugin/feed/pages_player_test.go`, `plugin/feed/browser_test.go`.

**Interfaces:**
- Produces:
  - `videoPlayOf(dl)` returns `playFile` when `dl.CleanAudioPath != "" && dl.CleanTrackPath == ""`. That is clean audio not yet a track, or one that failed; it plays audio only from the media route, as before.
  - `ResolveMedia` drops the `&& dl.CleanAudioPath == ""` guard on the video branch. The `playHLS` response sets `Cleaned: dl.CleanTrackPath != ""`.
  - `setHLSURL` skips an item with `ip.CleanPending`.
  - `StreamURL` follows `videoPlayOf`, as it does now.
  - `player.js`: on `Hls.Events.AUDIO_TRACK_SWITCHED`, and once on `MANIFEST_PARSED`, set `el.dataset.hlsAudio = hls.audioTracks[hls.audioTrack].name`.

- [ ] **Step 1: Write the failing tests.**
  - `TestResolveMediaCleanedVideoPlaysHLS`: on `cleanItem(…, true)`, an UNSPECIFIED request gives `Mime == hlsMIME`, `Variant == VIDEO`, `Cleaned == true`, and a URL prefixed `/apps/feed/hls/`.
  - `TestResolveMediaCleanPendingPlaysCleanAudio`:
    - A packaged video with `SetCleanAudioPath` set and no track: `Url` ends `/media`, `Variant == AUDIO`, `Cleaned == true`.
    - The same after `MarkCleanTrackError`.
  - `TestResolveMediaCleanedAudioItem`: an audio item with a clean track gives `Url` ending `/media`, `Variant == AUDIO`, `Cleaned == true`. Audio items stay progressive.
  - `TestStreamURLCleanedVideo`:
    - `api/stream-url/{id}` gives `{"hls": …}` for `cleanItem(…, true)`.
    - With the clean track pending, it gives `{"url": "/apps/feed/items/<id>/media"}`.
  - `TestBrowser/HLSClean`: next to `TestBrowser/HLS`, on a `cleanItem` with video. Wait for `data-hls-state="loaded"`, then assert `data-hls-audio == "Clean"`.
- [ ] **Step 2:** Run `go test ./plugin/feed -run 'TestResolveMediaClean|TestStreamURLCleaned|TestBrowser/HLSClean'`.
  Expected: FAIL. The cleaned video answers `/media` with `Variant == AUDIO`, and `data-hls-audio` is absent.
- [ ] **Step 3: Implement.**
- [ ] **Step 4:** Run `go test ./plugin/feed/...`.
  Expected: ok.
- [ ] **Step 5:** Commit: `feed: a cleaned video plays HLS with clean audio`.

### Task 8: Design and TODO

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`.
- Modify: `TODO.md`.

- [ ] **Step 1: Update the design.**
  - Status: `phases 1 and 3 done` (or 1–3 with phase 2).
  - Rewrite § Mute. The clean rendition is a whole re-encoded audio track, which is a **departure** from the spliced segments. Give the reasons (no AAC priming or `tfdt` seams, TTS replacements carried as they are, audio encoding at seconds per hour) and the cost (about 60 MB/h per cleaned item).
  - Note that there is no `mute` option: a clean track is the switch and is the default.
  - Record the rest of this plan's decisions: the packager owns the clean track, the clean file's own name, clean-pending plays audio only, `ErrNoRanges`, and captions masked whenever there is a clean track.
  - Remove `mute=profanity` from § Options and record why.
  - Under open questions, add "no trigger re-cleans when the word list changes".
- [ ] **Step 2:** In `TODO.md`, tick phase 3 and name its tests (`TestPackageCleanTrack*`, `TestHLSClean*`, `TestResolveMediaClean*`, `TestBrowser/HLSClean`). Add an open item: re-clean on word-list change.
- [ ] **Step 3:** Run `go tool verify`.
  Expected: everything passes. Revert any `go fix` rewrites of files this branch never touched, as phase 1 did. Name any flake by test name.
- [ ] **Step 4:** Commit: `feed: HLS phase 3 in the design and TODO`.
