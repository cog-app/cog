# Feed media over HLS

Status: phase 1 done (2026-11-15): downloaded items play over HLS from
signed links, in the web player through hls.js. Phases 2 to 6 are open.
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
- The server applies mute and skip by writing the playlist, so any HLS
  player gets them.
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
- **YouTube, not downloaded:** segments can point straight at googlevideo,
  using byte ranges read from the format's `sidx`. Then Cog serves only
  playlists and edited pieces. **Open:** googlevideo URLs carry the IP that
  resolved them (`ip=`) and expire after about 6 hours. A phone off the home
  network may be refused, so this needs a test off Wi-Fi, and a proxy
  fallback if the URLs are refused.
- **Podcasts:** usually MP3. Transcoding to AAC in fMP4 is cheap for audio
  (seconds per hour) and done once. **Open:** whether HLS packed-audio MP3
  segments (byte ranges of the origin file) play widely enough to skip the
  transcode.
- **Captions:** the VTT Cog already keeps (`downloads.captions_path`: YouTube
  auto-subs or whisper). They are served as segmented WebVTT with
  `X-TIMESTAMP-MAP`, cut to the same segments as the media.

## Playlists

Playlists are written per request from the segment index, the item's ranges
and the request's options. They are text, cheap to write, and never stored.
The media files stay static and cacheable.

- **Master:** `EXT-X-MEDIA TYPE=AUDIO` (original, and clean when there are
  mutes), `TYPE=SUBTITLES` (captions), the video variants referencing both
  groups, and an audio-only variant.
- **Media:** `EXT-X-MAP` for the init section, then byte-range segments.

### Mute (profanity)

The timeline is unchanged, so mute works in any player, video included.
The clean audio rendition is the original's segments, except that a segment
containing a mute range comes from a small file of re-encoded segments
(silenced through the range). A playlist mixes byte ranges from both files.
Captions in the clean presentation mask the muted words.

### Skip (silence)

- A skipped run of whole segments is left out of the playlist, and the jump
  is marked with `EXT-X-DISCONTINUITY`.
- **A cut inside a segment:** the partial segment at each edge is transcoded
  to a short segment that starts or ends exactly at the cut. Audio cuts are
  exact to a frame (about 21 ms). Video edges are re-encoded from the
  preceding keyframe. Edge segments are made once per range set and cached,
  keyed by the item and a hash of its ranges. The segments between edges
  still come from the original file, or straight from YouTube.
- Ranges shorter than a minimum (around 1 s, a pref) are dropped, and nearby
  ones merged, so a talk with many pauses isn't hundreds of discontinuities.
- Video and audio take the same cuts, so they stay in sync.

### Timeline

With skips, a player's clock is no longer the item's. Every skipped
presentation has an edit-list id (a hash of its ranges), and Cog maps
between presentation time and item time:

- Cog's own clients report item time (`ReportProgress`), mapping it
  themselves from ranges `GetRanges` returns.
- A progress report in presentation time names the edit-list id, and the
  server maps it back.
- External players report nothing today. **Open:** an mpv script, or
  gpodder episode actions, either of which would need the mapping.

## Options

Query parameters on the master playlist, carried into every URL it writes:

- `skip=silence`: apply skip ranges. Off by default.
- `mute=profanity`: make the clean audio and captions the default. The
  user's pref decides when it is absent.
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
  The player's mute and skip move into the playlist.
- **Android:** Media3's HLS module. In the background, disable the video
  renderer and the track selector moves to the audio-only variant.
- **Desktop (mpv):** plays the signed URLs as they are.

## Phases

1. Separate-track downloads and packaging, the segment index, master and
   media playlists for downloaded items, byte-range serving, and signed
   URLs. Web player on hls.js.
2. The captions rendition.
3. Mute: the clean rendition, with spliced re-encoded segments.
4. Skip: segments left out, discontinuities, edge transcodes, minimum length
   and merging, the edit-list id and time mapping.
5. Undownloaded YouTube segments straight from googlevideo, after the
   off-Wi-Fi test, with a proxy fallback.
6. M3U of a playlist or view for external players. Android on Media3 HLS
   with background audio-only.

## Open questions

- Whether googlevideo byte ranges work from a different IP than resolved them
  (phase 5).
- Whether HLS packed-audio MP3 plays widely enough to skip transcoding
  podcasts.
- Whether mpv (ffmpeg's HLS demuxer) shows WebVTT subtitle renditions; if not,
  also offer `--sub-file` from the M3U.
- How to get progress back from external players.
- YouTube's audio tracks come in fragments of about 10 s, so their segments
  run longer than 6 s. Fine for VOD; phase 4's cuts are coarser there unless
  the edge transcode splits a fragment.
- Packaging works through the backlog 100 downloads per tick, and an MP3
  podcast is encoded to AAC, tens of seconds per hour of audio. A large
  library's first packaging run holds up the other workers for a while.
- Hardware encoding (VAAPI; the compose file needs `/dev/dri` and the render
  group, as Jellyfin's has) once video edges are re-encoded. The bubblewrap
  sandbox must pass `/dev/dri` through as well.
