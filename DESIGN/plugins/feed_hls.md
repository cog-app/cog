# Feed media over HLS

Status: design, not started (2026-11-15). Supersedes the merged-mp4 YouTube
download (`feed: YouTube video plays from a download`, MR !54), which stays
until this lands.

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
- Signed, expiring URLs, so an external player needs no credentials.
- No transcoding of whole videos. Stream copy wherever possible, and
  transcode only the small pieces an edit needs.

## Non-goals

- Live streams, DRM, and an adaptive bitrate ladder beyond the tracks the
  source already offers.
- Speed changes. A player does those itself.

## Sources and storage

Each source track is kept on its own as a **fragmented MP4** with a segment
index (start time, duration, byte range, keyframe), so a playlist can address
any run of segments by byte range out of one file. Packaging is a stream copy
(`ffmpeg -c copy -f hls -hls_segment_type fmp4 -hls_flags single_file`, or
the equivalent muxing in Go), and its index is stored in the feed database.

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

`/apps/feed/hls/{item}/master.m3u8?exp=…&sig=…` is signed with a per-user key
over the user, the item, the options and the expiry. Every URL inside the
playlists carries a token derived from it, so segment and caption requests
need no cookie or header. Revoking the key (a user setting) kills every link.

A Cog playlist (or any view) is also an M3U of signed master URLs:
`/apps/feed/playlists/{id}.m3u?exp=…&sig=…&skip=silence`. A desktop launcher
is just `mpv <that URL>`. A long expiry suits a personal launcher, and a
re-signing redirect keeps it working past the expiry of the item URLs inside
it.

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
- Hardware encoding (VAAPI; the compose file needs `/dev/dri` and the render
  group, as Jellyfin's has) once video edges are re-encoded. The bubblewrap
  sandbox must pass `/dev/dri` through as well.
