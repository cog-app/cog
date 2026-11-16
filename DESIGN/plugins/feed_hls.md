# Feed media over HLS

Status: phases 1 to 5 done (2026-11-16): downloaded items play over HLS from
signed links, in the web player through hls.js, with a captions rendition, a
clean (profanity-muted) audio rendition, and skip (silence cut out of the
playlists, with edge transcodes), and undownloaded YouTube plays from
googlevideo. Phase 6 is open; the phase 5 manual check on desktop Chrome and
the phone is pending.
Supersedes the merged-mp4 YouTube download (`feed: YouTube video plays from a
download`, MR !54); merged downloads made before are repackaged as tracks.

## Why

YouTube serves picture and sound as separate tracks, and Cog's players want
them separate too:

- **Background play** should stop fetching video and keep the audio. A player
  can do that by switching HLS variants. It can't with one merged mp4.
- **Edits for any player.** Cog already computes silence (skip) and
  profanity (mute) ranges, but only its own players apply them. A playlist the
  server writes can apply them for a player that knows nothing about Cog,
  mpv included.
- **Captions** belong in the same stream. Profanity filtering already reads
  them.
- **External players.** A signed link should start playback in mpv with no
  setup: a desktop launcher that plays a Cog playlist, silence skipped.

## Goals

- One HLS presentation per item: video variants, an audio-only variant, an
  audio group (original and clean) and a captions group.
- The server applies skip by writing the playlist, and mute by offering a
  stored clean rendition, so any HLS player gets both.
- Signed URLs, so an external player needs no credentials.
- No transcoding of whole videos. Stream copy wherever possible, and
  transcode only the small pieces an edit needs.

## Non-goals

- Live streams, DRM, and an adaptive bitrate ladder beyond the tracks the
  source already offers.
- Speed changes. A player does those itself.

## Sources and storage

Each source track is kept on its own as a **fragmented MP4** with a segment
index (start time, duration, byte range, keyframe), so a playlist can address
any run of segments by byte range out of one file.

- **The index is the track file's own `sidx`** (`plugin/feed/hls`,
  `ReadTrack`). Nothing about fragments goes in the database, and no playlist
  is baked to disk: a playlist request reads the track's `moov` and `sidx`, a
  few KB at the head of the file (under 25 KB for an hour). A stored copy
  could fall out of step with the file, and a baked playlist would change
  with every revoked key and every option. Phase 5 reads googlevideo's `sidx`
  with the same parser over one Range request. A cache comes in if remote
  reads turn out slow.
- **Packaging** (`pipeline.Packager`, after the downloader) is ffmpeg's mp4
  muxer with a global `sidx` (`-movflags
  +frag_keyframe+empty_moov+default_base_moof+global_sidx`; audio in 2 s
  fragments), not its HLS muxer. The `sidx` also makes the file seekable
  when it is played straight through, which Media3 needs for the media route.
  Video is always stream-copied; audio is copied when it is AAC and encoded
  to AAC at 128k otherwise. Cover art is never a video track.
- **A file that already is a single-track fMP4 with a `sidx` is used as it
  is**, not copied: YouTube's DASH tracks qualify when yt-dlp runs with
  `--fixup never`, which keeps it from remuxing m4a into a plain MP4. Audio
  qualifies when it is AAC.
- After packaging, `downloads.local_path` is the audio track and
  `downloads.video_path` the video track: the downloaded files when they were
  used as they are, else `media/<id>.hls-audio.m4a` and
  `media/<id>.hls-video.mp4`. Silence, transcription, profanity and the media
  route keep reading `local_path`; Collect removes a replaced original.
- A download that cannot be packaged keeps playing as it did (the media
  route), and is not tried again every tick. A video downloaded as two
  tracks has no single file to fall back to, so a player asking for it
  downloads it again, saying why, as a failed download is.
- Fragments are grouped into segments of about 6 s, each starting on a
  keyframe, when the playlist is written. Phase 4 cuts at fragment
  boundaries.

- **YouTube, downloaded:** yt-dlp fetches the H.264 (≤1080p) video track and
  the m4a audio track separately, with no merge. Both are already fragmented
  (YouTube's DASH formats carry a `sidx`), so packaging may only need to read
  the index.
- **YouTube, not downloaded:** an item with no packaged download plays from
  googlevideo (phase 5, `feed.remoteYouTube`).
  - One yt-dlp resolve per item gives both tracks. The result is cached in
    `youtube_streams` until 10 minutes before the URLs' `expire`, with the
    format ids pinned, so a re-resolve picks the same formats and the byte
    ranges in an issued playlist stay valid. If the pinned ids are dead, the
    row is dropped and one fresh resolve runs in the same flight. Segment
    names carry the format id, so a player holding playlists for a format no
    longer chosen gets 410 for its segments, never another format's bytes,
    and restarts.
  - Cog reads each track's `sidx` and `moov` with one Range request (about
    256 KB, `RemoteTrack`) and writes the master and media playlists with the
    same presentation code as a local item. Captions are absent (none without
    a download).
  - Every segment is a request to `yt-audio-<format>.m4a` or
    `yt-video-<format>.mp4` on Cog, signed like any HLS link. It is served
    remote whatever the package state, so a player that started before the
    download was packaged finishes remote; its next master is local. In `direct` mode Cog answers 302 to the googlevideo URL (a
    fresh one if the cached one is near expiry). In `proxy` mode Cog streams
    the range itself through safehttp (redirects followed, addresses checked
    at dial) and, on a 403, refreshes the URL once and retries.
  - The mode is a server-wide admin setting, `hls_youtube_mode` (`direct`
    default, or `proxy`), in Feed System Settings, stored in feed's root
    database. The whisper settings live there too, so every user's transcribe
    worker sees the admin's choice. Whisper values saved per user before this
    change are ignored; the admin saves them once again.
  - Falls back to downloading when there is no remote presentation: the
    resolve or index read fails, the item is not YouTube, or the item is
    muted. `ResolveMedia` and `StreamURL` make that choice; a client that goes
    away mid-resolve returns its error and does not queue a download. A
    download that is done but not yet packaged also plays remote, and so do
    legacy done-but-unpackaged downloads (see Open questions).
  - **No mute without a download.** A muted item never goes remote
    (`ResolveMedia` and `StreamURL` fall back to download). Links are
    perpetual, so the remote serve path checks the same mute decision on
    every request and answers 503 (as `ItemMedia` does), with no resolve and
    no fetch.
  - **No skip without a download.** A remote master asked for `skip=silence`
    ignores it and carries `SESSION-DATA us.duckfam.cog.skip=unavailable`,
    which phase 4 clients read.
- **Podcasts:** usually MP3. Transcoding to AAC in fMP4 is cheap for audio
  (seconds per hour) and done once. **Open:** whether HLS packed-audio MP3
  segments (byte ranges of the origin file) play widely enough to skip the
  transcode.
- **Captions:** the VTT Cog already keeps (`downloads.captions_path`: YouTube
  auto-subs or whisper). They are served as segmented WebVTT with
  `X-TIMESTAMP-MAP`, cut to the same segments as the media.
  - One VTT segment per audio segment, with the audio segment's duration. It
    holds every cue that overlaps it with the cue's own times, not clipped; a
    cue that crosses a boundary appears in both, and hls.js and Safari drop a
    cue they already hold. The cue bounds of a segment are caption time: the
    segment's start minus the audio track's first fragment start
    (`hls.SegmentCaptionBounds`).
  - `X-TIMESTAMP-MAP=MPEGTS:<n>,LOCAL:00:00:00.000`, where n is
    `round(first fragment Start of the Original track x 90000)`, in both the
    unskipped and the skip paths. **Phase 4 reversed phase 2's `MPEGTS:0`.**
    Phase 2's offset fixture had a `tfdt` of 0 while its `sidx` said 1.48, so
    the fixture was inconsistent and `MPEGTS:0` looked right; with consistent
    fixtures (`tfdt` equal to the `sidx` start) `MPEGTS:0` put the cues 1.5 s
    early in hls.js 1.7.4. Real tracks start at 0, where every value agrees.
  - YouTube auto-captions are cleaned when read, not when stored (profanity
    reads the stored file): cues under 50 ms (YouTube's 10 ms "hold" cues),
    inline `<...>` tags, blank lines, and leading lines that repeat the end of
    the previous kept cue (rolling captions) are dropped, and a cue left
    with no text is dropped.
  - The rendition is `NAME="Captions"`, `DEFAULT=NO`, `AUTOSELECT=YES`, with
    no `LANGUAGE`. The SUBTITLES group is listed only when
    `captions_path != ""`, and then every variant (video and audio-only)
    names it. A missing file is logged and the master is served without it.
  - The web player's CC button appears only on the video player, once hls.js
    (or Safari's native text tracks) reports a subtitle track. It is off by
    default and not saved as a pref.
  - `TARGETDURATION` is the longest `EXTINF` as printed, rounded to the
    nearest integer (RFC 8216), which fixes phase 1's 6.4996-printed-as-6.500
    against a target of 6.

## Playlists

Playlists are written per request from the segment index, the item's ranges
and the request's options. They are text, cheap to write, and never stored.
The media files stay static and cacheable.

- **Master:** `EXT-X-MEDIA TYPE=AUDIO` (original, and clean when a clean track exists), `TYPE=SUBTITLES` (captions), the video variants
  referencing both groups, and an audio-only variant.
- **Media:** `EXT-X-MAP` for the init section, then byte-range segments.

### Mute (profanity)

Mute is a stored clean rendition, not a playlist edit. The timeline is
unchanged, so it works in any player, video included.

**Departure from the first design.** This spec first said the clean rendition
was the original's segments, except that a segment containing a mute range
came from a small file of re-encoded segments. As built, the clean rendition
is a whole re-encoded audio track. Reasons:

- The profanity worker already re-encodes the whole file, including the TTS
  replacements for the muted words, so the clean file exists anyway.
- Splicing AAC segments from two encodes brings encoder priming and `tfdt`
  seams at every join.
- Audio encoding costs seconds per hour of audio.

The cost is disk: about 60 MB per hour for each cleaned item.

There is no `mute` option. A clean track is the switch, and it is the
default: where one exists, the master lists Original first and Clean second,
and Clean carries `DEFAULT=YES`. `TestBrowser/HLSClean` proves hls.js honours
`DEFAULT`, not the first-listed rendition.

Decisions:

- **The packager makes the clean track**, not the profanity worker. The
  packager owns "file to track" (probe, transcode or copy, `remux`, index
  check, error state). The profanity worker stays "file to cleaned file", and
  keeps working for downloads that were never packaged. `RunCleanOnce` runs
  after the profanity worker, so a new clean file becomes a track in the same
  tick. It also picks up clean files made before this phase, including ones
  cleaned from a merged MP4 (video stream ignored) or an MP3 (transcoded).
- **The cleaned file's own name** is `media/<id>.clean<ext of local_path>`,
  never the track's name, so a re-clean never overwrites a live track with a
  plain file. Once the track is kept, `clean_audio_path` points at it and the
  plain file belongs to no download, so `Collect` removes it.
  `SetCleanAudioPath` clears `clean_track_path` and `clean_track_error`: a new
  clean file is not a track until the packager has seen it.
- **A re-clean keeps the track name** `media/<id>.hls-audio.clean.m4a`,
  replaced by an atomic rename (`TestRecleanReplacesTrack`).
- **No ranges means no clean audio.** `CleanAudioFile` returns
  `profanity.ErrNoRanges` when the word list is empty or matches nothing; the
  worker clears the flag and `clean_audio_path`. This also ends the
  retry-every-tick when `Keep` failed on a file never written.
- **A cleaned video whose clean track is not ready** (pending or failed to
  package) plays clean audio only, as before; it never plays HLS with
  unclean audio.
- **Audio-only item with a clean track:** the master has the audio group and
  one `EXT-X-STREAM-INF` with `AUDIO="audio"`, whose URI is the default
  (clean) rendition's playlist. A cleaned video keeps its audio-only variant,
  which points at `clean.m3u8`.
- **Captions are masked whenever the download has a clean track**, whichever
  audio rendition plays, with a single subtitles rendition. If the profanity
  plugin is unavailable, a cleaned item withholds its captions (no
  `SUBTITLES` group, subtitle playlists 404, a warning is logged) rather than
  serve unmasked captions over clean audio.
- **The web player reports the audio rendition it plays** in
  `data-hls-audio`, from hls.js's current audio track.

### Skip (silence)

`skip=silence` on the master leaves the item's silence out of every playlist.
Whole segments inside a cut are dropped; the segments a cut lands inside are
replaced by short **edges** that start or end exactly at the cut. Everything
between edges still comes from the original files.

**Effective cuts.** The silence ranges with `Action=skip` go through
`hls.EffectiveCuts`, which both the playlists and the web player's
`api/ranges` use, so the web player skips exactly what a skip master skips:

- Ranges shorter than the **minimum length** are dropped. The pref
  `skip_silence_min_sec` defaults to 1.0 and is bounded to 0.5 to 10 (0 over
  gRPC means unchanged, so clients that send the whole `PlayerPrefs` without
  the field keep working). Ranges closer than that are merged.
- Each kept cut shrinks by **0.1 s** at both ends, so a skip never clips a
  word's start or tail. An end within 0.1 s of 0 or of the media duration is
  snapped to it and not shrunk. The duration is the Original track's end;
  `Ranges` falls back to unclamped cuts, with a warning (logged once per item
  per process, since the player polls), when the track can't be read.

**Per-rendition plans.** Renditions have different segment grids (Original
about 10 s, Clean about 6 s, Video its own), so each is planned on its own
(`PlanCuts(track, cuts)`), keyed `a` (Original), `c` (Clean) and `v` (Video).
The cuts are the same, so every rendition ends up the same length.

- A kept segment keeps its bytes and its `tfdt`; an edge is a new file, made
  per rendition.
- A piece shorter than 0.1 s is joined to its neighbour in the same kept span,
  so an edge never has a frame-less 10 ms video piece. A short whole segment
  is kept as it is; an overhang at the end of the media is dropped.
- **Discontinuities only at cuts.** `EXT-X-DISCONTINUITY` goes before the first
  entry of each kept span that follows a cut, never before the first entry,
  and never because the file changed (a file change only writes a new
  `EXT-X-MAP`). hls.js numbers continuities per playlist and lines up
  alternate audio and subtitles with the main playlist by that number; file
  changes fall in different places in each rendition, so a file-change rule
  gives each playlist a different count and audio or subtitles stall. Cut
  positions are identical in every rendition.
- **Timestamps stay in item time**: kept segments keep their `tfdt`, and edges
  are written with `+frag_discont` and an output offset of the piece start,
  so `tfdt` carries item time. Presentation time is what the playlist adds
  up to.

**Edges are made on demand, never in a request.**

- The skip master (and, from phase 6a, an M3U that asks for skip) calls
  `WantEdges`, which sets `downloads.edges_wanted` and `edges_wanted_at`, and
  wakes the fetcher (`TriggerFetch`) whenever it serves an item pending,
  including one wanted before whose edges a change reset; saving a new
  shortest silence wakes it too. `pipeline.Edges` runs after `RunCleanOnce`
  in the orchestrator tick, two items or 60 s per run, **most recently asked
  first** (`edges_wanted_at`, refreshed when a pending item is asked for
  again, at most once a minute). An item whose clean audio is still to come
  waits: a clean file without its track (until it is packaged or
  `clean_track_error`), or profanity muting that can still run. Muting can
  run when the profanity plugin is there (the orchestrator tells the worker),
  `downloads.profanity_error` is empty, and the captions it needs are there
  or being made with `downloads.transcribe_error` empty. The transcribe and
  profanity workers record those errors (whisper-cli missing or failing, a
  muting failure other than nothing to mute) and keep retrying every tick as
  before; a success clears them, as do new captions (profanity) and a new
  download (both). The Clean track is part of the plan, and edges made
  before it landed would be reset under a player; once muting cannot finish,
  the edges are made from what there is, and made again if a retry lands a
  clean track. The result is `downloads.edges_edit` (the plan's id) and
  `downloads.edges_index` (each edge's byte layout), so playlists open no edge
  files. Edges live in the cache under `edges/<item>/<edit>/<a|c|v>-<n>.mp4`
  and are served as `edge-<edit>-<a|c|v>-<n>.mp4`.
- **A run that stops keeps its edges**: out of budget or cancelled, the next
  run checks each edge it finds and makes only the rest. `Collect` keeps
  every edit directory of a pending item (wanted, neither made nor failed);
  the worker removes the item's other edits when it records one. So a pref
  change that leaves an item's cuts as they were (same edit) reuses its
  edges.
- **Audio edges** are AAC-LC with the priming packet dropped, at the source's
  sample rate and channel count. They are **whole AAC frames**: an edge's
  last frame decodes whole, and a player that keeps time by the audio it has
  played (mpv) drifts by each edge's difference from its piece. Each edge
  takes the frame count that keeps the audio played so far within half a
  frame of the plan (`audioSpans`), is cut to the sample with `atrim`, and
  keeps the end that joins the media beside it: its start after a cut, its
  end before media that follows without one, so only the time of the cut
  moves, by under a frame. `TestHLSSkipManyCutsInSync` plays a minute with
  22 cuts in ffmpeg and holds the audio to within 25 ms (about one AAC
  frame) of the presentation and of the video; it measures 2 ms (it was
  330 ms long when each edge ended past its piece).
- **Video edges** are libx264 with `-bf 0` and `sps-id=1`, with the source's
  timescale and in-band parameter sets. Decode times are shifted by the
  source's B-frame delay (so the edge's `tfdt` is the piece start), and the
  source's SPS/PPS are added to each edge's `avcC`. This is for ffmpeg and
  mpv, whose HLS demuxer keeps the first `moov` it sees and skips later ones:
  an edge must decode under the source's init and the source under an edge's.
  Hence the two parameter sets, and why hls.js (which reinitialises per
  `EXT-X-MAP`) was never the risk.
- Every edge is checked by its index, its first `tfdt` and its parameter sets
  before it counts; a failed run records `edges_error` against the edit (or
  against no edit, when the tracks could not be loaded) and
  `edges_failed_at`. The worker takes a failed item again six hours on,
  clearing its error.
- **The edge format** (`feed.EdgeFormat`, now 1) is hashed into the full
  plan's edit id: bump it when the ffmpeg arguments or `AddParamSets` change,
  so edges made the old way are not served as the new.
- **Unsupported:** a rendition that is not AAC-LC, or video that is not H.264
  baseline, main or high (HE-AAC, High 10, other codecs), is served
  unskipped, with `SESSION-DATA` `us.duckfam.cog.skip` = `unsupported`, and
  makes no edges.

**Modes of a skip master**, chosen per request and by the unsigned `edit`
selector on the children:

- **No cuts** (no silence found, or silence not done): served exactly like an
  unskipped master, with no `edit`, no `SESSION-DATA`, no `WantEdges`.
- **Full:** edges are ready, or the plan has no edge pieces at all (every cut
  is on a segment boundary, which is full at once). Every rendition is cut
  exactly.
- **Pending:** edges are being made. **Video waits**: a video master is served
  unskipped, with `SESSION-DATA` `us.duckfam.cog.skip` = `pending`, until the
  edges exist (a coarse video cut would desync from audio's exact cut).
  **Audio-only gets a coarse cut**: whole segments only, from the default
  rendition (Clean when it exists, else Original), with its subtitles;
  Original is not listed until the worker is done. (A coarse master carries
  no `us.duckfam.cog.skip`; it is skipped, if roughly.)
- **Failed:** the edges of this plan could not be made (or the item could not
  be planned). As pending, but `us.duckfam.cog.skip` = `failed`: video plays
  unskipped, audio-only keeps its coarse cut. The worker tries again six
  hours on; an input change resets the edges at once.
- **Unsupported:** above.

**The `edit` id.** A 12-hex-digit hash of the applied cuts (rounded to
milliseconds) and every listed rendition's layout (`a`, `c`, `v`: the byte
lengths change on a re-clean or re-download, so the id does too). It is an
unsigned child selector (`edit=<12 hex>`, allowed only with `skip=silence`):
the signature covers the user, the item and `skip`, and the cuts themselves are
re-derived server side, so `edit` cannot choose anything; it pins which plan a
child request reads. A player holding a coarse master keeps working after the
worker finishes (the coarse id is still accepted); an unknown id is 404, and
an edge file is served only when its id is the stored full plan's. The master
carries `SESSION-DATA` `us.duckfam.cog.edits` = the edit id, which Cog's
clients use to name the plan they play.

**Invalidation.** `SetSilenceRanges`, `markReady`, `SavePackage`,
`SaveCleanTrack`, `SetCleanAudioPath` and a change of the min-length pref reset
`edges_edit` and `edges_error`, so the item is wanted again. The master
self-heals: if the stored edit differs from the computed one, or its edge
files have been evicted, it forgets them (`ForgetEdges`, only while the edit
it read is still the one recorded, so it never wipes edges the worker
recorded meanwhile) and asks again. Stale edge directories are removed per
item when a new run succeeds, and `Collect` removes those of deleted items
and, for items not pending, of every edit but the recorded one.

**Captions under skip.** One VTT per piece of the default audio rendition's
plan, at the piece's duration, with the same discontinuities as the audio.
`X-TIMESTAMP-MAP` is `MPEGTS:<round(Original first fragment Start x 90000)>`
as in the unskipped path; cues are **clipped to the piece** (an unclipped cue
across a cut would show twice, once per continuity) and **not shifted**: they
stay in caption time, which hls.js and spec-following players both map to
item time. `TestBrowser/HLSSkipCaptions` proves it in hls.js: a cue after the
cut lands at its item time minus the cut. Withholding and masking follow the
unskipped rule (computed once, before the skip branch).

**Clean tracks keep the Original's start.** `packClean` offsets the clean
track to the Original's first fragment start, so Clean and Video stay in sync
and share one caption map (phase 3's "Clean always starts at 0" is resolved).
It also drops the encoder's AAC priming frame, so the clean track's first
sample is the Original's: under a kept init its edit list is lost, as with
edges.

**Margins and why.**

- *Edges on demand*: encodes cost CPU, and a request must not run ffmpeg.
- *Video waits for its edges*: a half-exact video cut with an exact audio
  cut desyncs; audio-only has no picture to desync from.
- *The 0.1 s shrink*: silence detection is not frame-exact; cutting inside a
  word is worse than playing 0.2 s of silence per cut.
- *Applied cuts as the edit id's input*: a changed pref or detection changes
  the id, so a stale edit is unknown, never silently wrong.
- *Web ranges use the same effective cuts*: one definition of "skipped".
- *Unsigned `edit`*: it only selects among plans the server computes.

### Timeline

With skips, a player's clock is no longer the item's. Every skipped
presentation has an edit id (see Skip), and Cog maps between presentation
time and item time:

- Cog's own clients report item time (`ReportProgress`), mapping it
  themselves from the ranges `GetRanges` returns.
- A progress report in presentation time sets `ReportProgressRequest.edit_list`
  to the edit id; the server maps it back to item time with that plan's cuts
  and the **media duration stored in `hls_edits`** (not the feed's duration,
  which is often wrong). The mapped time clamps to that duration, and a report
  within a hair of the presentation's end maps to the item's end. An edit
  recorded with no duration (0) maps unclamped. An unknown
  id is `InvalidArgument` ("unknown edit list").
- External players report nothing today. **Open:** an mpv script, or
  gpodder episode actions, either of which would need the mapping.

## Options

Query parameters on the master playlist, carried into every URL it writes:

- `skip=silence`: apply skip ranges. Off by default. Signed with the rest.
- `edit=<12 hex>`: on child URLs only, pins the plan (see Skip). Not signed.
- There is no `mute` option: the clean track is the default whenever it
  exists (see Mute). A switch would need a second, unmasked captions
  rendition, which is not worth it until a client wants Original by default.
- A player that can do something itself should: Cog's own clients ask
  without `skip` and skip silence natively (Media3's `skipSilenceEnabled`, the
  web player's ranges), and every player does its own speed. `skip=silence` is
  for players that can't, such as mpv.

## Signed URLs

`/apps/feed/hls/{user}/{item}/master.m3u8?sig=…` is an HMAC-SHA256 with a
per-user key over the user and the item (and, from phases 3 and 4, the
options in a canonical order). The user is in the path because the route has
no session. Every URL inside the playlists is relative and carries the same
`sig`, so one grant covers one item's presentation and segment and caption
requests need no cookie or header. Any other query parameter is refused until
a phase defines it.

**Links do not expire.** A link that leaks plays one item, which costs little.
The key (32 random bytes in `feed_settings`, made on first use) is what ends
them: "Revoke media links" on the Sync settings page replaces it, and every
link handed out stops working.

A Cog playlist (or any view) is also an M3U of signed master URLs:
`/apps/feed/playlists/{id}.m3u?sig=…&skip=silence`. A desktop launcher is just
`mpv <that URL>`.

## Clients

- **Web:** hls.js (desktop Chrome and Firefox; Safari plays HLS natively).
  Mute becomes the clean rendition and skip moves into the playlist.
- **Android:** Media3's HLS module. In the background, disable the video
  renderer and the track selector moves to the audio-only variant.
- **Desktop (mpv):** plays the signed URLs as they are.

## Phases

1. Separate-track downloads and packaging, the segment index, master and
   media playlists for downloaded items, byte-range serving, and signed
   URLs. Web player on hls.js.
2. The captions rendition. Done.
3. Mute: the clean rendition, as a whole re-encoded audio track. Done.
4. Skip: segments left out, discontinuities, edge transcodes, minimum length
   and merging, the edit id and time mapping. Done.
5. Undownloaded YouTube segments straight from googlevideo, with a proxy
   fallback. Done; manual check pending.
6. M3U of a playlist or view for external players. Android on Media3 HLS
   with background audio-only.

## Decided

- Undownloaded YouTube defaults to `direct` (`hls_youtube_mode`), with
  `proxy`, chosen by an admin in Feed System Settings, as the fallback. On
  2026-11-16, URLs resolved at home were range-requested from a phone on
  cellular. The audio answered 302, a googlevideo host redirect (players and
  safehttp follow it), not a refusal; an earlier, possibly-Wi-Fi run had the
  video at 206. So the URLs are not refused off the resolving network. The
  redirect's target from another network is confirmed by the phase 5 manual
  phone check.

## Open questions

- A packaging backfill for legacy YouTube downloads that are done but were
  never packaged: they now play remote instead of locally.
- An index cache: each remote start reads four 256 KB index ranges (two per
  track); cache them per item, keyed by the pinned format ids.
- Whether the googlevideo redirect target (the 302 the audio answered) accepts
  other IPs; the phase 5 phone check confirms it, and `proxy` is the fallback.
- Chunked requests: whether googlevideo throttles long segment ranges, in
  which case `direct` and `proxy` would split a range into chunks.
- Whether HLS packed-audio MP3 plays widely enough to skip transcoding
  podcasts.
- Whether mpv (ffmpeg's HLS demuxer) shows WebVTT subtitle renditions; if not,
  also offer `--sub-file` from the M3U.
- Whether iOS Safari's native HLS and Media3 (spec-following, media
  timeline) show captions at the right time on a track that does not start at
  0, given `MPEGTS` = the track's start. Checked on the phone in phase 6b.
- hls.js 1.7.4 misplaces a skip caption when a VTT piece after a cut is
  parsed before that discontinuity's media has given it an initPTS, and the
  map is `MPEGTS:0` (a track starting at 0). Its WebVTT parser takes a
  missing initPTS as 0, so `MPEGTS − initPTS` is 0, which it reads as "map
  lines up with the discontinuity" and places the cue at the discontinuity's
  start plus its caption time (cue B at 9 s, cut {6,8}: 15 s, not 7 s). A
  non-zero `MPEGTS` instead raises "Missing initPTS", and the piece is
  deferred and placed correctly. Our playlists and map are spec-correct
  (kept media keeps its media time); the order is a race, usually won by the
  media. `TestBrowser/HLSSkipCaptions` holds subtitle fragments until their
  initPTS. Before the web player plays skip, either do the same in its
  loader, or write the equivalent non-zero map (`MPEGTS:<m+k>,LOCAL:<k/90000>`,
  say k = 90000), which by its source hls.js would defer (untested). Media3
  is expected to wait for a discontinuity's timestamp adjuster before
  reading its WebVTT; confirm on the phone in phase 6b.
- A captions language is not recorded: neither source (YouTube, whisper)
  records one reliably, so the rendition has no `LANGUAGE`.
- How to get progress back from external players.
- No trigger re-cleans an item when the word list changes.
- `clean.m4a` keeps its name and is served with `Cache-Control: max-age=86400`,
  so after a re-clean a browser may use cached old bytes against new ranges
  for up to a day. Version the media URI when the word-list re-clean trigger
  is built; `audio.m4a` has the same issue after a re-download.
- A re-clean replaces the clean track under the same name. A client holding
  an old playlist gets the new file's bytes under its old byte ranges until
  it reloads.
- Whether mpv plays an audio-only cleaned master whose variant URI is also the
  default group rendition. Checked by hand in phase 6a.
- YouTube's audio tracks come in fragments of about 10 s, so their segments
  run longer than 6 s, and an Original audio edge can be up to that long.
  Encoding one on demand should be cheap, but measure.
- Packaging works through the backlog 100 downloads per tick, and an MP3
  podcast is encoded to AAC, tens of seconds per hour of audio. A large
  library's first packaging run holds up the other workers for a while.
- Whether Media3 decodes video edges whose `avcC` carries two SPS (the
  source's id 0 and the edge's id 1). Checked on the phone in phase 6b.
- The video edges' B-frame delay is measured from the source's first fragment
  only (`sidx` earliest presentation time minus the first `tfdt`), which is
  wrong for sources shifted by an `elst`; read the `trun` composition offset
  instead.
- Edge offsets are rounded to 1 ms (video seeks to the µs).
- `edges_wanted` never expires: an item asked for once stays wanted, and its
  edges are remade after every reset (a pref change remakes every wanted
  item's whose cuts changed). Expire it (say, a month after
  `edges_wanted_at`, or once played) if the worker's queue shows it.
- The worker reads an item's plan, encodes, then reads it again before it
  records (the stale check); an input change between that read and the
  record leaves stale edges for the master to self-heal. A generation
  counter would close it; it costs a re-encode only.
- **Before phases 5 and 6a build on skip**, a by-hand mpv check of a long
  video with many cuts: no audible gaps or repeats at the cuts, the
  wall-clock length matches the presentation, and audio and video are in
  sync at the end. ffmpeg's own decode is checked (`TestHLSSkipManyCutsInSync`);
  mpv's clock across discontinuities is not.
- `hls_edits` rows are never pruned (they cascade only when the item is
  deleted).
- Media playlists, `subs.m3u8` and each `subs-<n>.vtt` replan on every
  request (about 600 replans per captioned hour). Cache the plan if it shows.
- `AddParamSets` assumes the default base is the `moof`; it does not check
  `tfhd` base-data-offset or `saio`.
- Whether mpv shows presentation time or item time across discontinuities
  (decides whether an mpv script needs the mapping). Checked by hand in phase
  6a, along with whether its clock jumps cleanly over the gaps.
- Hardware encoding (VAAPI; the compose file needs `/dev/dri` and the render
  group, as Jellyfin's has) once video edges are re-encoded. The bubblewrap
  sandbox must pass `/dev/dri` through as well.
