package hls

import (
	"fmt"
	"math"
	"strings"
)

// SegmentTarget is the length, in seconds, fragments are grouped up to.
const SegmentTarget = 6.0

// Segment is a run of whole fragments a playlist lists as one entry.
type Segment struct {
	Duration       float64
	Offset, Length int64
}

// Segments groups fragments into segments: a new one starts at a keyframe
// fragment once the current one has reached SegmentTarget.
func Segments(frags []Fragment) []Segment {
	var segs []Segment
	for i, f := range frags {
		if i == 0 || (f.Keyframe && segs[len(segs)-1].Duration >= SegmentTarget) {
			segs = append(segs, Segment{Offset: f.Offset})
		}
		s := &segs[len(segs)-1]
		s.Duration += f.Duration
		s.Length += f.Length
	}
	return segs
}

// segments groups a track's fragments. Every audio fragment starts cleanly,
// whatever its sidx says.
func segments(t Track) []Segment {
	if t.Kind != Audio {
		return Segments(t.Fragments)
	}
	frags := make([]Fragment, len(t.Fragments))
	for i, f := range t.Fragments {
		f.Keyframe = true
		frags[i] = f
	}
	return Segments(frags)
}

// MediaPlaylist is a VOD playlist of t's segments, each a byte range of
// fileURI.
func MediaPlaylist(t Track, fileURI string) string {
	segs := segments(t)
	target := 1
	for _, s := range segs {
		target = max(target, int(math.Round(s.Duration)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", target)
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q,BYTERANGE=\"%d@%d\"\n", fileURI, t.InitLength, t.InitOffset)
	for _, s := range segs {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n#EXT-X-BYTERANGE:%d@%d\n%s\n", s.Duration, s.Length, s.Offset, fileURI)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// bandwidth is a track's peak and average bit rate over its segments.
func bandwidth(t Track) (peak, average int64) {
	var bytes int64
	var secs float64
	for _, s := range segments(t) {
		if s.Duration > 0 {
			peak = max(peak, int64(math.Ceil(float64(s.Length)*8/s.Duration)))
		}
		bytes += s.Length
		secs += s.Duration
	}
	if secs > 0 {
		average = int64(math.Ceil(float64(bytes) * 8 / secs))
	}
	return peak, average
}

// MasterPlaylist presents an audio track, and a video track when there is
// one, as an HLS master playlist. With video, the audio is a rendition the
// video variant names, and an audio-only variant follows.
func MasterPlaylist(audio Track, video *Track, audioURI, videoURI string) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	aPeak, aAvg := bandwidth(audio)
	if video != nil {
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio\",NAME=\"Original\",DEFAULT=YES,AUTOSELECT=YES,URI=%q\n", audioURI)
		vPeak, vAvg := bandwidth(*video)
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d", vPeak+aPeak, vAvg+aAvg)
		if video.Codecs != "" && audio.Codecs != "" {
			fmt.Fprintf(&b, ",CODECS=\"%s,%s\"", video.Codecs, audio.Codecs)
		}
		if video.Width > 0 && video.Height > 0 {
			fmt.Fprintf(&b, ",RESOLUTION=%dx%d", video.Width, video.Height)
		}
		fmt.Fprintf(&b, ",AUDIO=\"audio\"\n%s\n", videoURI)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d", aPeak, aAvg)
	if audio.Codecs != "" {
		fmt.Fprintf(&b, ",CODECS=%q", audio.Codecs)
	}
	fmt.Fprintf(&b, "\n%s\n", audioURI)
	return b.String()
}
