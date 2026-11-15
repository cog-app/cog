# Feed HLS, Phase 6b: Android on Media3 HLS — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Android app plays Cog's video items as HLS through Media3. It shows the picture while the player area is on screen. Whenever the app is in the background or no surface is attached, it stops fetching video and keeps the audio, and it skips silence with Media3's own `skipSilenceEnabled`.

**Architecture:**
- Core's player (`core/player`) asks `ResolveMedia` for any variant. If the answer is `Unavailable`, it asks again for audio.
- The MIME type travels down to the engine:
  - `player.Source`
  - `mobile.Engine.Load`
  - Kotlin `Playback.load`, which calls `MediaItem.setMimeType`
- Whether video is decoded is a core decision: video is on only when the app is in the foreground and a surface is attached. Core passes it to the engine as `SetVideo(bool)`, and Kotlin turns that into `setTrackTypeDisabled(C.TRACK_TYPE_VIDEO, …)`.
- The picture is a `#[kt.native]` `PlayerView` inside the player bar. It is shown only for video items.

**Tech Stack:**
- Go 1.26: `core/player`, `core/mobile`, `core/ui`.
- gomobile bind (`shell/android/cmd/bindgolib`).
- Kotlin 2.1, with AGP 8.7.3.
- Media3 1.5.1: exoplayer, session, plus **exoplayer-hls** and **ui** added here.
- sngl (`app/main.sngl`, `go generate ./shell`).

**Spec:**
- `cog/DESIGN/plugins/feed_hls.md` § Clients: "Android: Media3's HLS module. In the background, disable the video renderer and the track selector moves to the audio-only variant."
- The binding Android decisions are roadmap decision 10, in `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md`.
- The app's player design is `design/player.md` (outer repo), § "The surface" and § "Background and audio-only".

**Repo:** every path below is relative to the **outer** repo `/home/jonathan/src/git.duckfam.us/jj-software/cog-feed-sngl`, except `cog/…` paths, which are its Cog checkout (branch `cog-feed-sngl`, the same git repository as the `feed-hls` worktree).

## Global Constraints

- **Server branches:** don't push, merge or rebase anyone's branch without asking. Task 1's merge of `feed-hls` into `cog/`'s `cog-feed-sngl` is a stop that needs the user's go-ahead.
- **Never swallow errors** (cog `CLAUDE.md`, which holds here too): wrap and return them, or log them. A Kotlin `catch` either rethrows or reports through `mobile.Mobile.reportError`.
- **Go tests:**
  - Use the outer repo's existing style: plain `testing`, `t.Fatalf`, and the fakes in `core/player/queue_test.go` and `core/mobile/mobile_test.go`.
  - Run them with `go test ./core/...` from the outer repo root.
  - Check builds with `go install ./core/...`, not `go build`.
  - Format with `go fmt ./core/...`.
- **No Kotlin test infrastructure exists**, so every Kotlin change is checked on the phone:
  1. `cd shell/android && ./gradlew installDebug`. It runs bindgolib itself. If the phone is attached over wifi-adb rather than USB, use `adb install -r app/build/outputs/apk/debug/app-debug.apk`.
  2. Install every build on the phone without asking (memory: install-android-builds-on-phone). If the phone is disconnected, say so and carry on.
  3. Run the exact manual checks the task lists, and record what was seen in the ledger.
- **Server for device checks:**
  1. In `cog/` (after Task 1), run `go tool dev`.
  2. Run `adb reverse tcp:8580 tcp:8580` and pair the app with `http://localhost:8580`.
  3. The dev server logs every request, and that log is where the checks below read which files the phone fetches.
- **Generated UI:** after any `app/main.sngl` change, run `go generate ./shell`. Never edit `shell/android/app/src/main/java/us/duckfam/cogfeed/ui/*` by hand.
- **Native UI:** custom Android UI goes in an app component with a `[android.platform]` override that calls a `#[kt.native]` composable (memory: app-wrappers-vs-sngl-ui). Don't grow `sngl:ui`.
- **Decided, don't reopen:**
  - Separate tracks served as HLS.
  - A player that can skip silence or change speed does it itself, so Media3 skips silence natively and the app never asks for `skip=silence`.
  - Pre-signed links need no credentials.
- **Commits:** prefix the subject with the area (`core/player:`, `core/mobile:`, `app:`, `android:`, `TODO:`). Every commit message ends with:
  ```
  Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX
  ```

## Decisions this plan makes

- **No reload when the app goes to the background.** `design/player.md` step 4 reloads a resolver stream as the AUDIO variant. With HLS that isn't needed: disabling the video track type stops the video rendition's segments while the audio rendition keeps playing, so there is no gap. Every video `ResolveMedia` answers with HLS now, and a legacy merged mp4 still drops its video track, which is enough. *Cost if wrong:* a legacy merged download keeps fetching video bytes in the background until it is repackaged. Phase 1 repackages them all.
- **HLS sources carry no `Authorization`.** A signed link needs none. Phase 5's direct mode answers a segment with a 302 to googlevideo, and `DefaultHttpDataSource` may resend the request's headers across that redirect. So `Player.source` attaches the Bearer token only when the MIME type is not HLS. *Cost if wrong:* none. The signed route ignores the header.
- **Video defaults to off.** `Player` keeps two flags, `foreground` and `surface`. Video is on only when both are true. They are process state in `core/player`, next to `procEngine`, so they survive the player being rebuilt and apply to every item it loads.
- **`State.Video` is true when the item's media answered `Variant == VIDEO`.** The bar shows the surface only then. Audio items never mount a `PlayerView`.
- **Unavailable video falls back to audio once.** The player retries `ResolveMedia` with `VARIANT_AUDIO` and plays that. It shows the server's reason ("Preparing the video", "Downloading the video") as `State.Notice` until the next item loads. If the audio request fails too, the item fails with that second error.
- **Silence skipping comes from the web's pref.** It reads `PlayerPrefs.skip_silence` at each load, through a new `Engine.SetSkipSilence(bool)`. The app gets no toggle in this phase.

## Review Focus

1. **The Bearer token must never leave Cog's host.** An HLS master on Cog, then a 302 to googlevideo (phase 5 direct mode), must reach googlevideo with no `Authorization`. Task 2's `TestHLSSourceCarriesNoCredentials` pins core's part. The device check in Task 8 pins the rest.
2. **The next item, loaded while the app is in the background, starts with video off.** The video state has to be applied after every `Load`, not only when the flags change. Covered by Task 4's `TestVideoStateAppliesToEveryLoad`.
3. **Unavailable for both variants:** an undownloaded YouTube item whose audio can't be resolved either. The player must fail once, with the reason, and not loop. Covered by Task 3's `TestUnavailableTwiceFails`.
4. **Podcast items keep playing exactly as before:** the media route, progressive, with no MIME type and Bearer auth. Covered by Task 2's `TestMediaRouteKeepsCredentials`, and Task 3's `TestResolveAsksForAnyVariant` asserts nothing else changes.
5. **Flags set before any player exists apply once one starts.** `MainActivity.onStart` runs before the first play. Covered by Task 4's `TestForegroundBeforePlayerStarts`.

---

### Task 1: Bring the HLS server into the app's Cog checkout

**Files:**
- Modify: `cog/` (git: merge branch `feed-hls` into `cog-feed-sngl`)
- Test: `go test ./core/...`

**Interfaces:**
- Consumes the following from the server, with phases 1–3 merged:
  - `ResolveMedia` returns `MediaSource{Url: "/apps/feed/hls/<user>/<id>/master.m3u8?sig=…", Mime: "application/vnd.apple.mpegurl", Variant: VARIANT_VIDEO}` for a packaged video item (`cog/plugin/feed/rpc_media.go`).
  - A video item that is not ready yet gets `CodeUnavailable` with a reason.
  - Phase 3 makes a cleaned video item answer with HLS too (`rpc_media.go` no longer bypasses HLS when there is clean audio).
- Produces: nothing new. `feedpb` already has `MediaSource.Mime`, `Variant`, and `PlayerPrefs.SkipSilence`.

- [ ] **Step 1: Check the precondition.** Run `git -C cog/tmp/feed-hls log --oneline -1 feed-hls` and confirm that phases 1–3 are on `feed-hls` (TODO.md ticks in `cog/tmp/feed-hls/TODO.md`). If phase 3 has not landed, stop: this plan needs it so that cleaned videos play as HLS. Record the state in the ledger.
- [ ] **Step 2: Ask the user.** Ask: "Merge `feed-hls` into `cog/`'s `cog-feed-sngl` (local only, no push)?" Wait for a yes. This is a stop under Global Constraints.
- [ ] **Step 3: Merge.**
  - Run `git -C cog merge --no-ff feed-hls`.
  - Expected: a clean merge. The two branches share base `35b35776`, and `cog-feed-sngl` only adds `6c30bd49` (Subscription favicon).
  - If there is a conflict, resolve it keeping both sides, then run `go test ./plugin/feed/...` inside `cog/`.
- [ ] **Step 4: Verify.**
  - Run `go install ./core/... && go test ./core/...`.
  - Expected: PASS. Nothing in core changes yet; this proves the newer `feedpb` still builds.

### Task 2: The MIME type reaches Media3; HLS goes without credentials

**Files:**
- Modify: `core/player/queue.go` (`Source`, `Player.source`)
- Modify: `core/mobile/mobile.go` (`Engine.Load`, adapter `engine.Load`)
- Modify: `shell/android/app/build.gradle.kts` (add the HLS module)
- Modify: `shell/android/app/src/main/java/us/duckfam/cogfeed/Playback.kt` (`load`)
- Test: `core/player/queue_test.go`, `core/mobile/mobile_test.go`

**Interfaces:**
- Produces:
  ```go
  // core/player
  type Source struct {
      URL           string
      Authorization string
      Mime          string // "" lets the engine infer it; HLSMime for a master playlist
      Title, Artist string
  }
  const HLSMime = "application/vnd.apple.mpegurl"

  // core/mobile: Kotlin's engine
  type Engine interface {
      Load(url, authorization, mime, title, artist string, startSec float64) error
      // Play, Pause, Seek, SetRate, Stop unchanged
  }
  ```
- Kotlin: `override fun load(url: String, authorization: String, mime: String, title: String, artist: String, startSec: Double)`.

- [ ] **Step 1: Write the failing tests** in `core/player/queue_test.go`:
  ```go
  func TestHLSSourceCarriesNoCredentials(t *testing.T) {
      feed := newFakeFeed(item(1, "One", 0))
      feed.media[1] = &feedpb.MediaSource{Url: "/apps/feed/hls/jon/1/master.m3u8?sig=s", Mime: HLSMime, Variant: feedpb.Variant_VARIANT_VIDEO}
      eng := &fakeEngine{}
      p, _ := newTestPlayer(feed, eng)
      // PlayPlaylist(ctx, 7) succeeds; then:
      // eng.load.URL == <test server> + "/apps/feed/hls/jon/1/master.m3u8?sig=s"
      // eng.load.Authorization == ""
      // eng.load.Mime == HLSMime
  }
  func TestMediaRouteKeepsCredentials(t *testing.T) // default fake media (/apps/feed/items/1/media, no mime): Authorization == "Bearer "+token, Mime == ""
  ```
  In `core/mobile/mobile_test.go`, extend `kotlinEngine.Load` to record `mime`. Then extend `TestInstallEngineAdaptsLoad` to load `player.Source{…, Mime: player.HLSMime}` and assert `k.mime == player.HLSMime`.
- [ ] **Step 2: Run** `go test ./core/player ./core/mobile`. Expected: FAIL to compile, with `unknown field Mime in struct literal` and `undefined: HLSMime`.
- [ ] **Step 3: Implement.**
  - Add `Source.Mime`, set from `m.Mime`.
  - In `Player.source`, attach `Authorization` only when `m.Url` starts with `/` **and** `m.Mime != HLSMime`.
  - Change `mobile.Engine.Load` to the new signature, and pass `src.Mime` through in the adapter.
  - Update the comment on `Source` and the comment on `mobile.Engine`.
- [ ] **Step 4: Run** `go test ./core/...`. Expected: PASS.
- [ ] **Step 5: Kotlin.**
  - In `app/build.gradle.kts`, after `media3-session`, add `implementation("androidx.media3:media3-exoplayer-hls:1.5.1")` with the comment `// HLS, which Cog serves video items as (feed_hls.md).`
  - In `Playback.load`, take `mime`, and when it is not empty call `.setMimeType(mime)` on the `MediaItem.Builder`. Core's `HLSMime` equals `MimeTypes.APPLICATION_M3U8`; say so in a comment.
- [ ] **Step 6: Device check.**
  1. Run `./gradlew installDebug`. Expected: BUILD SUCCESSFUL, installed.
  2. In the app, play a playlist whose first item is a packaged YouTube video. Expected: it plays (audio only for now, since no surface exists yet). The dev server log shows `GET /apps/feed/hls/…/master.m3u8`, `audio.m3u8`, and `audio.m4a` with ranges.
  3. Play a podcast. Expected: `GET /apps/feed/items/<id>/media` as before.
- [ ] **Step 7: Commit.**
  ```bash
  git add core/player core/mobile shell/android/app/build.gradle.kts shell/android/app/src/main/java/us/duckfam/cogfeed/Playback.kt
  git commit -m "core/player: media carries its MIME type to Media3; HLS links go without credentials"
  ```

### Task 3: Ask for any variant; fall back to audio when video isn't ready

**Files:**
- Modify: `core/player/queue.go` (`load`, `State`)
- Modify: `core/ui/playing.go` (`Playing.Notice`, `fromPlayerState`)
- Modify: `app/main.sngl` (`playerBar` shows the notice)
- Test: `core/player/queue_test.go`, `core/ui/playing_test.go`

**Interfaces:**
- Produces:
  - `State.Notice string`: why this item plays as it does, e.g. "Preparing the video". It is cleared by the next load.
  - `State.Video bool`: the media answered `VARIANT_VIDEO`.
  - `Playing.Notice string` and `Playing.Video bool` (core/ui).
- Fake change: `fakeFeed` gains `variants []feedpb.Variant`, which records each `ResolveMedia` variant, and `unavailable map[int64]string`. A key present there answers `connect.NewError(connect.CodeUnavailable, errors.New(msg))` to a request whose variant is not AUDIO. `failAudio map[int64]string` does the same for the AUDIO request.

- [ ] **Step 1: Write the failing tests.**
  ```go
  func TestResolveAsksForAnyVariant(t *testing.T) // feed.variants == []Variant{VARIANT_UNSPECIFIED}; State().Notice == ""; State().Video == false
  func TestVideoMediaIsVideo(t *testing.T)        // media Variant VIDEO → State().Video == true
  func TestUnavailableVideoFallsBackToAudio(t *testing.T) {
      // unavailable[1] = "Preparing the video"
      // feed.variants == {UNSPECIFIED, AUDIO}; eng.load.URL ends "/apps/feed/items/1/media";
      // State().Status != StatusFailed; State().Notice == "Preparing the video"; State().Video == false
  }
  func TestUnavailableTwiceFails(t *testing.T) // unavailable[1] and failAudio[1] = "no audio": Status == StatusFailed, Error contains "no audio", len(feed.variants) == 2
  func TestNoticeClearedOnNextItem(t *testing.T) // item 1 falls back, Next() loads item 2 normally → Notice == ""
  ```
  In `core/ui/playing_test.go`, add `TestFromPlayerStateNotice`: a `State{…, Notice: "Preparing the video", Video: true}` gives `Playing.Notice == "Preparing the video"` and `Playing.Video == true`.
- [ ] **Step 2: Run** `go test ./core/player ./core/ui`. Expected: FAIL (compile: `Notice`, `Video`, `variants` undefined).
- [ ] **Step 3: Implement.**
  - In `load`:
    1. Request `feedpb.Variant_VARIANT_UNSPECIFIED`.
    2. If `connect.CodeOf(err) == connect.CodeUnavailable`, keep the message (`connect.Error.Message()`) as the notice and request `VARIANT_AUDIO`.
    3. Any other error, or an error from the second request, goes to `p.fail` as today.
  - Set `Notice` and `Video` under the lock where `p.st` is set before `engine.Load`.
  - In `fromPlayerState`, copy both fields.
- [ ] **Step 4: Run** `go test ./core/...`. Expected: PASS.
- [ ] **Step 5: UI.**
  - In `app/main.sngl` `playerBar`, under the show line, add `if playing.notice != "" { ui.text(value=playing.notice, style={opacity=0.7, maxLines=2}) }`.
  - Run `go generate ./shell`. Expected: it regenerates `shell/android/app/src/main/java/us/duckfam/cogfeed/ui/MainScreen.kt` with no errors.
  - Run `go test ./core/... ./shell/...`. Expected: PASS.
- [ ] **Step 6: Device check.**
  1. Run `./gradlew installDebug`.
  2. Queue an undownloaded YouTube video. Expected: it plays its audio at once, the bar reads "Downloading the video", and the server log shows two `ResolveMedia` calls.
- [ ] **Step 7: Commit.**
  ```bash
  git add core/player core/ui app/main.sngl shell/android/app/src/main/java/us/duckfam/cogfeed/ui
  git commit -m "core/player: ask for video; play the audio while it isn't ready, saying why"
  ```

### Task 4: `SetVideo`: video only in the foreground with a surface

**Files:**
- Modify: `core/player/queue.go` (`Engine`, process state, `load`)
- Modify: `core/mobile/mobile.go` (`Engine.SetVideo`, `SetForeground`, `SetSurface`)
- Modify: `Playback.kt` (`setVideo`)
- Test: `core/player/queue_test.go`, `core/mobile/mobile_test.go`

**Interfaces:**
- Produces:
  ```go
  // core/player
  type Engine interface { /* … */ SetVideo(on bool) error }
  func SetForeground(on bool) // the app's UI is visible; process state, applied to Current()
  func SetSurface(on bool)    // a video surface is attached; process state, applied to Current()
  func (p *Player) videoOn() bool // foreground && surface && st.Video

  // core/mobile
  type Engine interface { /* … */ SetVideo(on bool) error }
  func SetForeground(on bool) // MainActivity onStart/onStop
  func SetSurface(on bool)    // VideoSurface.kt attach/detach
  ```
- Kotlin: `override fun setVideo(on: Boolean) = onMain { it.trackSelectionParameters = it.trackSelectionParameters.buildUpon().setTrackTypeDisabled(C.TRACK_TYPE_VIDEO, !on).build() }`.
- `fakeEngine` records `"video:true"` and `"video:false"` in `calls`, and `kotlinEngine` records `video`.

- [ ] **Step 1: Write the failing tests.**
  ```go
  func TestVideoOffByDefault(t *testing.T) // video item; calls after "load" include "video:false" before "play"
  func TestVideoFollowsForegroundAndSurface(t *testing.T) {
      // SetForeground(true): last video call still false; SetSurface(true): "video:true";
      // SetForeground(false): "video:false"; t.Cleanup resets both to false
  }
  func TestVideoStateAppliesToEveryLoad(t *testing.T) // foreground+surface on, then SetForeground(false), then Next(): item 2's load is followed by "video:false"
  func TestAudioItemNeverTurnsVideoOn(t *testing.T)   // both flags on, audio media → "video:false"
  func TestForegroundBeforePlayerStarts(t *testing.T) // SetForeground(true); SetSurface(true) with no player; then Start + play a video item → "video:true"
  ```
  In `core/mobile/mobile_test.go`, add `TestSetVideoReachesKotlin`: `engine{k}.SetVideo(true)` gives `k.video == true`.
- [ ] **Step 2: Run** `go test ./core/player ./core/mobile`. Expected: FAIL (compile: `SetVideo`, `SetForeground`, `SetSurface` undefined).
- [ ] **Step 3: Implement.**
  - Keep `procForeground` and `procSurface` beside `procEngine`, under the same mutex.
  - `SetForeground` and `SetSurface` set the flag, then call `Current().applyVideo()` when a player exists.
  - `load` calls `engine.SetVideo(p.videoOn())` right after `engine.Load` and before `SetRate`. A failure goes through `failLocked`.
  - In `mobile`, the two process functions forward to `player`. Add `SetVideo` to `mobile.Engine`. The adapter embeds the interface, so it needs no new method.
  - Update `design/player.md` § Engine to list `SetVideo` as built.
- [ ] **Step 4: Run** `go test ./core/...`. Expected: PASS.
- [ ] **Step 5: Kotlin.**
  - Implement `Playback.setVideo`.
  - In `MainActivity.onStart`, call `mobile.Mobile.setForeground(true)` before connecting the controller. In `onStop`, call `mobile.Mobile.setForeground(false)` before releasing it.
- [ ] **Step 6: Device check.** Run `./gradlew installDebug`, then play a video item. Expected: the server log shows `audio.m4a` ranges only, never `video.mp4`. There is no surface yet, so video stays off.
- [ ] **Step 7: Commit.**
  ```bash
  git add core/player core/mobile design/player.md shell/android/app/src/main/java/us/duckfam/cogfeed/Playback.kt shell/android/app/src/main/java/us/duckfam/cogfeed/MainActivity.kt
  git commit -m "core/player: video decodes only in the foreground with a surface"
  ```

### Task 5: The video surface in the player bar

**Files:**
- Modify: `app/main.sngl` (component `videoSurface`, its Android override, the `#[kt.native]` declaration, and its use in `playerBar`)
- Create: `shell/android/app/src/main/java/us/duckfam/cogfeed/VideoSurface.kt`
- Modify: `shell/android/app/build.gradle.kts` (`media3-ui:1.5.1`)
- Test: `go test ./core/... ./shell/...` (generation), device

**Interfaces:**
- Consumes: `Playing.Video` (Task 3) and `mobile.Mobile.setSurface` (Task 4).
- Produces in sngl:
  ```
  // A video item's picture, 16:9 across the bar. Nothing on targets without
  // one yet; on Android VideoSurface.kt's PlayerView.
  component videoSurface() ui.node { ui.vbox() {} }
  component videoSurface[android.platform]() { VideoSurface() }
  #[kt.native("VideoSurface", "us.duckfam.cogfeed.VideoSurface")]
  component VideoSurface() ui.node
  ```
  In `playerBar`, add `if playing.video { videoSurface() }` as the card's first child, above the title.
- Kotlin: `@Composable fun VideoSurface()`.
  - It draws an `AndroidView` of a `PlayerView` with `useController = false`, `player = Playback.player()`, `resizeMode = RESIZE_MODE_FIT`, and `Modifier.fillMaxWidth().aspectRatio(16f / 9f)`.
  - A `DisposableEffect(Unit)` calls `mobile.Mobile.setSurface(true)` and, in `onDispose`, `mobile.Mobile.setSurface(false)`, then sets `playerView.player = null`.

- [ ] **Step 1: Write the sngl and Kotlin** as above. Add `implementation("androidx.media3:media3-ui:1.5.1")` with the comment `// PlayerView, the video surface (VideoSurface.kt).`
- [ ] **Step 2: Run** `go generate ./shell && go test ./core/... ./shell/...`. Expected: PASS. `MainScreen.kt` calls `VideoSurface()` inside `if (playing.video)`.
- [ ] **Step 3: Device check.**
  1. Run `./gradlew installDebug`.
  2. Play a packaged video item with the app open. Expected: the picture shows above the title, and the server log now shows `video.m3u8` and `video.mp4` range requests.
  3. Press Home. Expected: audio continues with no gap, and within one segment (about 6 s) the log shows no more `video.mp4` requests while `audio.m4a` requests continue.
  4. Return to the app. Expected: within a few seconds `video.mp4` requests resume and the picture returns.
  5. Play a podcast. Expected: there is no surface and the bar looks as before.
- [ ] **Step 4: Commit.**
  ```bash
  git add app/main.sngl shell/android/app/build.gradle.kts shell/android/app/src/main/java/us/duckfam/cogfeed/VideoSurface.kt shell/android/app/src/main/java/us/duckfam/cogfeed/ui
  git commit -m "app: a video item's picture in the player bar; video stops in the background"
  ```

### Task 6: Skip silence with Media3

**Files:**
- Modify: `core/player/queue.go` (`Engine.SetSkipSilence`, `load`)
- Modify: `core/mobile/mobile.go` (`Engine.SetSkipSilence`)
- Modify: `Playback.kt`
- Test: `core/player/queue_test.go`, `core/mobile/mobile_test.go`

**Interfaces:**
- Produces: `SetSkipSilence(on bool) error` on both Engine interfaces. Kotlin implements it as `override fun setSkipSilence(on: Boolean) = onMain { it.skipSilenceEnabled = on }`.

- [ ] **Step 1: Write the failing tests.**
  ```go
  func TestSkipSilenceFromPrefs(t *testing.T) // feed.prefs.SkipSilence = true → fakeEngine.skipSilence == true after load, set before "play"
  func TestSkipSilenceOffByDefault(t *testing.T) // prefs without it → SetSkipSilence(false) called (a previous item's true must not linger)
  ```
  `fakeEngine` records `skipSilence`. In `mobile_test`, `kotlinEngine` gets a no-op `SetSkipSilence` and the test asserts the adapter forwards it.
- [ ] **Step 2: Run** `go test ./core/player ./core/mobile`. Expected: FAIL (compile).
- [ ] **Step 3: Implement.** In `load`, call `engine.SetSkipSilence(p.prefs.GetSkipSilence())` after `SetVideo`. Progress needs no mapping: ExoPlayer's `currentPosition` stays in media time when it skips silence, so reports stay in item time (feed_hls.md § Timeline).
- [ ] **Step 4: Run** `go test ./core/...`. Expected: PASS.
- [ ] **Step 5: Device check.**
  1. Turn on "Skip silence" in Cog's web player.
  2. Run `./gradlew installDebug` and play a talk with pauses. Expected: the pauses are shortened.
  3. The position reported to Cog stays in step with the web player's resume point. Check this by opening the item on the web after pausing.
- [ ] **Step 6: Commit.**
  ```bash
  git add core/player core/mobile shell/android/app/src/main/java/us/duckfam/cogfeed/Playback.kt
  git commit -m "core/player: Media3 skips silence when the user's prefs say to"
  ```

### Task 7: Docs and TODO

**Files:**
- Modify: `TODO.md` (M2 "Next: Cog's media over HLS", M5 "Background audio-only switch; optional PiP")
- Modify: `design/player.md` (§ Background and audio-only: no reload with HLS; § Android specifics: `VideoSurface.kt` reaches `Playback.player()` in-process, not through the `MediaController`)

- [ ] **Step 1: Edit.**
  - In TODO.md, tick the Android half of the HLS item. Tick "Background audio-only switch" and leave PiP open.
  - Add an M5 line: "Speed and skip-silence toggles in the app (skip silence follows the web's pref)".
  - In `design/player.md`, record the Decisions section of this plan.
- [ ] **Step 2: Commit.**
  ```bash
  git add TODO.md design/player.md
  git commit -m "TODO: Android plays Cog's HLS, audio only in the background"
  ```

### Task 8: Whole-flow device check

- [ ] **Step 1:** Run `./gradlew installDebug`. Then, with the dev server log open, do the following and record each result in the ledger:
  1. Play a playlist of video, podcast, and video. Expected: the surface appears, disappears, then appears again, and resume positions are correct after a restart of the app.
  2. Background the app mid-video, let it reach the next video item, then come back. Expected: the next item started with no `video.mp4` requests, and they began only on return.
  3. Revoke media links on Cog's Sync settings page, then press Next onto a video item. Expected: Media3 fails with a 403, and the bar shows "Couldn't play: …" without crashing.
  4. Check that no request after a 302 to a non-Cog host carries `Authorization`. This applies only once phase 5's direct mode exists; otherwise note "n/a until phase 5".
- [ ] **Step 2:** Run `go test ./core/...`. Expected: PASS. Record it with `task-done`.

## Open questions (not blocking)

- **The audio-only variant.** Media3's HLS period drops audio-only variants when video variants exist, so in the background it never switches to the audio-only variant the spec describes. It stops loading the deselected video rendition and keeps the audio rendition, which has the same effect. Task 5's device check is what confirms it.
- **Audio attributes.** `AUDIO_CONTENT_TYPE_SPEECH` stays. A video item may want `AUDIO_CONTENT_TYPE_MOVIE`, but that is a judgment call left for later.
- **Picture-in-picture and a full-screen player sheet** remain M5 items.
