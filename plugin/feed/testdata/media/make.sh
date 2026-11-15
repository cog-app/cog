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
$ff -f lavfi -i sine=frequency=440:duration=6 -f lavfi -i color=c=red:size=64x64:rate=1:duration=1 \
	-map 0:a -map 1:v -c:a libmp3lame -b:a 32k -c:v mjpeg -disposition:v attached_pic -id3v2_version 3 episode.mp3
# The same tracks starting 1.5 s in, as a YouTube pair with an edit list or
# priming would: captions must still land on the audio's first sample.
$ff -f lavfi -i testsrc=size=160x90:rate=24:duration=12 -output_ts_offset 1.5 -avoid_negative_ts disabled \
	-c:v libx264 -profile:v baseline -level 3.0 -g 48 -keyint_min 48 -sc_threshold 0 -pix_fmt yuv420p \
	-movflags +frag_keyframe+empty_moov+default_base_moof+global_sidx -f mp4 video-offset.mp4
$ff -f lavfi -i sine=frequency=440:duration=12 -output_ts_offset 1.5 -avoid_negative_ts disabled -c:a aac -b:a 32k \
	-movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4 audio-offset.m4a
