package hls

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// SegmentTarget is the length, in seconds, fragments are grouped up to.
const SegmentTarget = 6.0

// Segment is a run of whole fragments a playlist lists as one entry.
type Segment struct {
	Start, Duration float64 // Start: the first fragment's Start
	Offset, Length  int64
}

// Segments groups fragments into segments: a new one starts at a keyframe
// fragment once the current one has reached SegmentTarget.
func Segments(frags []Fragment) []Segment {
	var segs []Segment
	for i, f := range frags {
		if i == 0 || (f.Keyframe && segs[len(segs)-1].Duration >= SegmentTarget) {
			segs = append(segs, Segment{Start: f.Start, Offset: f.Offset})
		}
		s := &segs[len(segs)-1]
		s.Duration += f.Duration
		s.Length += f.Length
	}
	return segs
}

// TrackSegments groups a track's fragments. Every audio fragment starts cleanly,
// whatever its sidx says.
func TrackSegments(t Track) []Segment {
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

// Init is the initialisation section an entry's media needs; a playlist
// writes it as EXT-X-MAP.
type Init struct {
	URI            string
	Offset, Length int64
}

// Entry is one playlist line group: a resource, or a byte range of it.
type Entry struct {
	URI            string
	Offset, Length int64 // Length 0: whole resource, no EXT-X-BYTERANGE
	Duration       float64
	Init           Init // EXT-X-MAP when it differs from the previous entry's; none while URI == ""
	Discontinuity  bool // EXT-X-DISCONTINUITY before this entry
}

// Playlist is a VOD media playlist of entries.
func Playlist(entries []Entry) string {
	target := 1
	for _, e := range entries {
		// The target is the longest EXTINF as printed, rounded to the nearest second.
		printed, err := strconv.ParseFloat(fmt.Sprintf("%.3f", e.Duration), 64)
		if err != nil {
			printed = e.Duration // unreachable: %.3f of a float always parses
		}
		target = max(target, int(math.Round(printed)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n", target)
	var prev Init
	for _, e := range entries {
		if e.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if e.Init.URI != "" && e.Init != prev {
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q,BYTERANGE=\"%d@%d\"\n", e.Init.URI, e.Init.Length, e.Init.Offset)
			prev = e.Init
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", e.Duration)
		if e.Length > 0 {
			fmt.Fprintf(&b, "#EXT-X-BYTERANGE:%d@%d\n", e.Length, e.Offset)
		}
		fmt.Fprintf(&b, "%s\n", e.URI)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// TrackEntries lists t's segments as byte ranges of fileURI.
func TrackEntries(t Track, fileURI string) []Entry {
	init := Init{URI: fileURI, Offset: t.InitOffset, Length: t.InitLength}
	segs := TrackSegments(t)
	entries := make([]Entry, len(segs))
	for i, s := range segs {
		entries[i] = Entry{URI: fileURI, Offset: s.Offset, Length: s.Length, Duration: s.Duration, Init: init}
	}
	return entries
}

// MediaPlaylist is a VOD playlist of t's segments, each a byte range of
// fileURI.
func MediaPlaylist(t Track, fileURI string) string {
	return Playlist(TrackEntries(t, fileURI))
}

// bandwidth is a track's peak and average bit rate over its segments.
func bandwidth(t Track) (peak, average int64) {
	var bytes int64
	var secs float64
	for _, s := range TrackSegments(t) {
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

// Rendition is an alternative a master playlist offers: an audio track, or
// subtitles.
type Rendition struct {
	Name, Language, URI string
	Default             bool
	Track               *Track // nil for subtitles
}

// Presentation is what a master playlist describes.
type Presentation struct {
	Audio       []Rendition
	Video       *Track
	VideoURI    string
	Subtitles   []Rendition
	SessionData [][2]string
}

func yesNo(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

// Master writes p as an HLS master playlist. With video, the audio is a
// group of renditions the video variant names, and an audio-only variant of
// the default rendition follows.
func Master(p Presentation) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, kv := range p.SessionData {
		fmt.Fprintf(&b, "#EXT-X-SESSION-DATA:DATA-ID=%q,VALUE=%q\n", kv[0], kv[1])
	}
	if len(p.Audio) == 0 {
		return b.String()
	}
	def := p.Audio[0]
	for _, r := range p.Audio {
		if r.Default {
			def = r
			break
		}
	}
	var audio Track
	if def.Track != nil {
		audio = *def.Track
	}
	subs := ""
	if len(p.Subtitles) > 0 {
		subs = ",SUBTITLES=\"subs\""
	}
	aPeak, aAvg := bandwidth(audio)
	media := func(typ, group string, r Rendition) {
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=%s,GROUP-ID=%q,NAME=%q", typ, group, r.Name)
		if r.Language != "" {
			fmt.Fprintf(&b, ",LANGUAGE=%q", r.Language)
		}
		fmt.Fprintf(&b, ",DEFAULT=%s,AUTOSELECT=YES,URI=%q\n", yesNo(r.Default), r.URI)
	}
	if p.Video != nil {
		for _, r := range p.Audio {
			media("AUDIO", "audio", r)
		}
	}
	for _, r := range p.Subtitles {
		media("SUBTITLES", "subs", r)
	}
	if p.Video != nil {
		video := p.Video
		vPeak, vAvg := bandwidth(*video)
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d", vPeak+aPeak, vAvg+aAvg)
		if video.Codecs != "" && audio.Codecs != "" {
			fmt.Fprintf(&b, ",CODECS=\"%s,%s\"", video.Codecs, audio.Codecs)
		}
		if video.Width > 0 && video.Height > 0 {
			fmt.Fprintf(&b, ",RESOLUTION=%dx%d", video.Width, video.Height)
		}
		fmt.Fprintf(&b, ",AUDIO=\"audio\"%s\n%s\n", subs, p.VideoURI)
	}
	fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d", aPeak, aAvg)
	if audio.Codecs != "" {
		fmt.Fprintf(&b, ",CODECS=%q", audio.Codecs)
	}
	fmt.Fprintf(&b, "%s\n%s\n", subs, def.URI)
	return b.String()
}

// MasterPlaylist presents an audio track, and a video track when there is
// one, as an HLS master playlist.
func MasterPlaylist(audio Track, video *Track, audioURI, videoURI string) string {
	return Master(Presentation{
		Audio: []Rendition{{Name: "Original", URI: audioURI, Default: true, Track: &audio}},
		Video: video, VideoURI: videoURI,
	})
}
