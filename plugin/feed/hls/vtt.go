package hls

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Cue is one caption: its caption-time span and its text.
type Cue struct {
	Start, End float64 // seconds, caption time
	Text       string  // lines joined by "\n", tags stripped
}

// minCue is the shortest cue kept. YouTube follows each rolling cue with a
// 10 ms "hold" cue that repeats it.
const minCue = 0.050

var vttTag = regexp.MustCompile(`<[^>]*>`)

// ParseVTT reads a WebVTT file. Times are HH:MM:SS.mmm or MM:SS.mmm.
// An error only for input that is not WebVTT (no "WEBVTT" header) or a
// read failure; a malformed cue is skipped.
//
// The cues are cleaned for display: YouTube's rolling auto-captions repeat
// the previous line in each cue and add 10 ms hold cues, so those are folded
// away. plugin/profanity.ParseVTT deliberately reads whole, uncleaned cues
// from the stored file; it is not replaced by this.
func ParseVTT(r io.Reader) ([]Cue, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		cues     []Cue
		block    []string
		header   bool
		prevLast string
	)
	flush := func() {
		if len(block) == 0 {
			return
		}
		b := block
		block = nil
		if cue, ok := parseBlock(b); ok {
			if cue.End-cue.Start < minCue {
				return
			}
			lines := strings.Split(cue.Text, "\n")
			for len(lines) > 0 && lines[0] == prevLast {
				lines = lines[1:]
			}
			if len(lines) == 0 {
				return
			}
			cue.Text = strings.Join(lines, "\n")
			prevLast = lines[len(lines)-1]
			cues = append(cues, cue)
		}
	}

	first := true
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if first {
			first = false
			line = strings.TrimPrefix(line, "\xef\xbb\xbf")
			if !strings.HasPrefix(line, "WEBVTT") {
				return nil, fmt.Errorf("not WebVTT: missing WEBVTT header")
			}
			header = true
		}
		if line == "" {
			if header {
				header = false
				block = nil
			} else {
				flush()
			}
			continue
		}
		block = append(block, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read WebVTT: %w", err)
	}
	if first {
		return nil, fmt.Errorf("not WebVTT: empty input")
	}
	flush()
	return cues, nil
}

// parseBlock reads one cue block: an optional identifier, a timing line, and
// the text lines. NOTE, STYLE and REGION blocks and malformed cues are not ok.
func parseBlock(b []string) (Cue, bool) {
	for _, p := range []string{"NOTE", "STYLE", "REGION"} {
		if b[0] == p || strings.HasPrefix(b[0], p+" ") || strings.HasPrefix(b[0], p+"\t") {
			return Cue{}, false
		}
	}
	ti := -1
	for i, l := range b {
		if strings.Contains(l, "-->") {
			ti = i
			break
		}
	}
	if ti < 0 {
		return Cue{}, false
	}
	startS, rest, _ := strings.Cut(b[ti], "-->")
	endS := strings.TrimSpace(rest)
	if f := strings.Fields(endS); len(f) > 0 {
		endS = f[0] // drop cue settings after the end time
	}
	start, ok1 := vttTime(strings.TrimSpace(startS))
	end, ok2 := vttTime(endS)
	if !ok1 || !ok2 {
		return Cue{}, false
	}
	var lines []string
	for _, l := range b[ti+1:] {
		l = strings.TrimSpace(vttTag.ReplaceAllString(l, ""))
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return Cue{}, false
	}
	return Cue{Start: start, End: end, Text: strings.Join(lines, "\n")}, true
}

// vttTime parses HH:MM:SS.mmm or MM:SS.mmm.
func vttTime(s string) (float64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var secs float64
	for i, p := range parts {
		last := i == len(parts)-1
		if !last && strings.Contains(p, ".") {
			return 0, false
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		secs = secs*60 + v
	}
	return secs, true
}

// VTTSegment is one WebVTT segment: the cues overlapping [start, end),
// with their own times, under an X-TIMESTAMP-MAP that puts caption time 0
// at MPEG-TS time mpegts (90 kHz).
func VTTSegment(cues []Cue, start, end float64, mpegts int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:%d,LOCAL:00:00:00.000\n\n", mpegts)
	for _, c := range cues {
		if c.End <= start || c.Start >= end {
			continue
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", vttStamp(c.Start), vttStamp(c.End),
			strings.ReplaceAll(c.Text, "-->", "--&gt;"))
	}
	return b.String()
}

// vttStamp formats seconds as HH:MM:SS.mmm.
func vttStamp(s float64) string {
	ms := int64(math.Round(s * 1000))
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// TimestampMap is the MPEGTS value for a track: its first fragment's Start × 90000, rounded.
func TimestampMap(t Track) int64 {
	if len(t.Fragments) == 0 {
		return 0
	}
	return int64(math.Round(t.Fragments[0].Start * 90000))
}

// SubtitleEntries lists one VTT segment per segment of the audio track; uri(n) names segment n.
func SubtitleEntries(audio Track, uri func(n int) string) []Entry {
	segs := TrackSegments(audio)
	entries := make([]Entry, len(segs))
	for i, s := range segs {
		entries[i] = Entry{URI: uri(i), Duration: s.Duration}
	}
	return entries
}
