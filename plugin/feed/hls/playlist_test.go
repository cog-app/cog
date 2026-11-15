package hls_test

import (
	"regexp"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// offsetsFrom lays fragments end to end from byte off and time 0.
func offsetsFrom(off int64, frags ...hls.Fragment) []hls.Fragment {
	var start float64
	for i := range frags {
		frags[i].Offset, frags[i].Start = off, start
		off += frags[i].Length
		start += frags[i].Duration
	}
	return frags
}

func durations(segs []hls.Segment) []float64 {
	out := make([]float64, len(segs))
	for i, s := range segs {
		out[i] = s.Duration
	}
	return out
}

func frag(d float64, key bool) hls.Fragment {
	return hls.Fragment{Duration: d, Length: 10, Keyframe: key}
}

// Fragments join into segments of at least SegmentTarget, each starting on a keyframe.
func TestSegments(t *testing.T) {
	frags := offsetsFrom(0, frag(2, true), frag(2, true), frag(2, true), frag(2, true), frag(3, false), frag(2, true), frag(1, true))
	got := hls.Segments(frags)
	// 2+2+2 reaches the target; the 3 s fragment is no keyframe, so it joins
	// 2 and then 2 more (5 s is short of the target); the last starts anew.
	assert.Equal(t, []float64{6, 7, 1}, durations(got))
	assert.Equal(t, int64(30), got[0].Length)
	assert.Equal(t, got[0].Offset+got[0].Length, got[1].Offset)
	assert.Equal(t, int64(30), got[1].Length) // 2, 3 and 2 s
}

func audioTrack() hls.Track {
	return hls.Track{
		Kind: hls.Audio, Codecs: "mp4a.40.2", InitOffset: 0, InitLength: 700,
		Fragments: offsetsFrom(800, frag(2, true), frag(2, true), frag(2, true), frag(2, true)),
	}
}

func TestMediaPlaylist(t *testing.T) {
	pl := hls.MediaPlaylist(audioTrack(), "audio.m4a?sig=x")
	for _, want := range []string{
		"#EXTM3U\n", "#EXT-X-VERSION:7\n", "#EXT-X-PLAYLIST-TYPE:VOD\n", "#EXT-X-TARGETDURATION:6\n",
		`#EXT-X-MAP:URI="audio.m4a?sig=x",BYTERANGE="700@0"` + "\n",
		"#EXTINF:6.000,\n#EXT-X-BYTERANGE:30@800\naudio.m4a?sig=x\n",
		"#EXTINF:2.000,\n#EXT-X-BYTERANGE:10@830\naudio.m4a?sig=x\n",
		"#EXT-X-ENDLIST\n",
	} {
		assert.True(t, strings.Contains(pl, want), "%q not in\n%s", want, pl)
	}
	assert.True(t, strings.HasPrefix(pl, "#EXTM3U\n"), pl)
}

// An audio track's fragments all start cleanly, whatever the sidx says.
func TestMediaPlaylistAudioIgnoresSAP(t *testing.T) {
	tr := audioTrack()
	for i := range tr.Fragments {
		tr.Fragments[i].Keyframe = false
	}
	pl := hls.MediaPlaylist(tr, "a")
	assert.Equal(t, 2, strings.Count(pl, "#EXTINF:"), pl)
}

// EXTINF rounded to the nearest integer must not exceed the target duration (RFC 8216 4.3.3.1).
func TestMediaPlaylistTargetDuration(t *testing.T) {
	tr := hls.Track{Kind: hls.Video, InitLength: 10, Fragments: offsetsFrom(10, frag(6.4, true), frag(2, true))}
	assert.True(t, strings.Contains(hls.MediaPlaylist(tr, "v"), "#EXT-X-TARGETDURATION:6\n"))
	tr.Fragments = offsetsFrom(10, frag(6.6, true), frag(2, true))
	assert.True(t, strings.Contains(hls.MediaPlaylist(tr, "v"), "#EXT-X-TARGETDURATION:7\n"))
}

func videoTrack() hls.Track {
	// 100 bytes over 2 s is 400 bit/s; 300 bytes over 6 s, 400 bit/s.
	f := func(n int64) hls.Fragment { return hls.Fragment{Duration: 2, Length: n, Keyframe: true} }
	return hls.Track{
		Kind: hls.Video, Codecs: "avc1.42C01E", Width: 160, Height: 90, InitLength: 500,
		Fragments: offsetsFrom(600, f(100), f(100), f(100), f(1000)),
	}
}

func TestMasterPlaylistVideo(t *testing.T) {
	video := videoTrack()
	pl := hls.MasterPlaylist(audioTrack(), &video, "audio.m3u8?q", "video.m3u8?q")
	for _, want := range []string{
		"#EXTM3U\n", "#EXT-X-INDEPENDENT-SEGMENTS\n",
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Original",DEFAULT=YES,AUTOSELECT=YES,URI="audio.m3u8?q"` + "\n",
		`CODECS="avc1.42C01E,mp4a.40.2",RESOLUTION=160x90,AUDIO="audio"` + "\nvideo.m3u8?q\n",
	} {
		assert.True(t, strings.Contains(pl, want), "%q not in\n%s", want, pl)
	}
	assert.Equal(t, 1, strings.Count(pl, "#EXT-X-MEDIA:"), pl)
	// The audio-only variant names no group.
	audioOnly := regexp.MustCompile(`#EXT-X-STREAM-INF:([^\n]*)\naudio\.m3u8\?q\n`).FindStringSubmatch(pl)
	if assert.True(t, audioOnly != nil, pl) {
		assert.True(t, strings.Contains(audioOnly[1], `CODECS="mp4a.40.2"`), audioOnly[1])
		assert.False(t, strings.Contains(audioOnly[1], "AUDIO="), audioOnly[1])
	}
	// Video peak: the 1000-byte fragment over 2 s (the segment of the last
	// fragment alone) is 4000 bit/s. Audio peak: 30 bytes over 6 s = 40 bit/s.
	assert.True(t, strings.Contains(pl, "#EXT-X-STREAM-INF:BANDWIDTH=4040,"), pl)
	assert.True(t, strings.Contains(pl, "#EXT-X-STREAM-INF:BANDWIDTH=40,"), pl)
}

func TestMasterPlaylistAudioOnly(t *testing.T) {
	pl := hls.MasterPlaylist(audioTrack(), nil, "audio.m3u8?q", "video.m3u8?q")
	assert.Equal(t, 1, strings.Count(pl, "#EXT-X-STREAM-INF:"), pl)
	assert.False(t, strings.Contains(pl, "#EXT-X-MEDIA:"), pl)
	assert.False(t, strings.Contains(pl, "video.m3u8"), pl)
	assert.True(t, strings.Contains(pl, `CODECS="mp4a.40.2"`+"\naudio.m3u8?q\n"), pl)
}

func TestMasterPlaylistUnknownCodec(t *testing.T) {
	video := videoTrack()
	video.Codecs = ""
	pl := hls.MasterPlaylist(audioTrack(), &video, "audio.m3u8?q", "video.m3u8?q")
	line := regexp.MustCompile(`#EXT-X-STREAM-INF:([^\n]*)\nvideo\.m3u8\?q\n`).FindStringSubmatch(pl)
	if assert.True(t, line != nil, pl) {
		assert.False(t, strings.Contains(line[1], "CODECS="), line[1])
		assert.True(t, strings.Contains(line[1], "RESOLUTION=160x90"), line[1])
	}
}
