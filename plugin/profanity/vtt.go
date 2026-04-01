package profanity

import (
	"bufio"
	"io"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// ParseVTT parses a WebVTT caption file into timed segments.
func ParseVTT(r io.Reader) []plugin.CaptionSegment {
	var segments []plugin.CaptionSegment
	scanner := bufio.NewScanner(r)

	for scanner.Scan() {
		line := scanner.Text()
		// Look for timestamp lines: "00:00:01.000 --> 00:00:04.000"
		if !strings.Contains(line, "-->") {
			continue
		}
		parts := strings.SplitN(line, "-->", 2)
		if len(parts) != 2 {
			continue
		}
		start := parseVTTTime(strings.TrimSpace(parts[0]))
		end := parseVTTTime(strings.TrimSpace(parts[1]))
		if start < 0 || end < 0 {
			continue
		}

		// Collect text lines until blank line.
		var textParts []string
		for scanner.Scan() {
			tl := scanner.Text()
			if strings.TrimSpace(tl) == "" {
				break
			}
			// Strip VTT tags like <c> or <00:00:01.000>.
			tl = stripVTTTags(tl)
			if tl != "" {
				textParts = append(textParts, tl)
			}
		}
		if len(textParts) > 0 {
			segments = append(segments, plugin.CaptionSegment{
				Start: start,
				End:   end,
				Text:  strings.Join(textParts, " "),
			})
		}
	}
	return segments
}

// parseVTTTime parses "HH:MM:SS.mmm" or "MM:SS.mmm" to seconds.
func parseVTTTime(s string) float64 {
	// Remove position metadata after timestamp (e.g., "00:01:02.000 position:10%").
	if idx := strings.IndexByte(s, ' '); idx >= 0 {
		s = s[:idx]
	}

	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		h, _ := strconv.ParseFloat(parts[0], 64)
		m, _ := strconv.ParseFloat(parts[1], 64)
		sec := parseSeconds(parts[2])
		return h*3600 + m*60 + sec
	case 2:
		m, _ := strconv.ParseFloat(parts[0], 64)
		sec := parseSeconds(parts[1])
		return m*60 + sec
	default:
		return -1
	}
}

func parseSeconds(s string) float64 {
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func stripVTTTags(s string) string {
	var buf strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			buf.WriteRune(r)
		}
	}
	return strings.TrimSpace(buf.String())
}
