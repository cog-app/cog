package feed

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ffargs"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
)

// silenceNoiseDB and silenceMinDur control silencedetect sensitivity.
// -30dB for ~0.5s trims gaps without cutting normal pauses.
const (
	silenceNoiseDB = "-30dB"
	silenceMinDur  = "0.5"
)

// DetectSilence runs ffmpeg silencedetect on a local media file and returns the
// detected silent spans as skip ranges. Returns (nil, nil) when no silence is
// found. ffmpeg writes silencedetect output to stderr.
func DetectSilence(ctx context.Context, mediaPath string) ([]Range, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", err)
	}
	args := append([]string{"-hide_banner", "-nostats"}, ffargs.Input(mediaPath)...)
	args = append(args, "-af", "silencedetect=noise="+silenceNoiseDB+":d="+silenceMinDur, "-f", "null", "-")
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{mediaPath}}, "ffmpeg", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg silencedetect %q: %w: %s", mediaPath, err, stderr.String())
	}
	return parseSilenceDetect(stderr.String()), nil
}

// parseSilenceDetect extracts silence spans from ffmpeg silencedetect stderr.
// Each "silence_start" must be paired with a following "silence_end"; an unpaired
// trailing start (silence to EOF) is dropped.
func parseSilenceDetect(stderr string) []Range {
	var out []Range
	var haveStart bool
	var start float64
	sc := bufio.NewScanner(strings.NewReader(stderr))
	for sc.Scan() {
		line := sc.Text()
		if _, after, ok := strings.Cut(line, "silence_start:"); ok {
			field := strings.TrimSpace(after)
			parts := strings.Fields(field)
			if len(parts) > 0 {
				if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
					start, haveStart = v, true
				}
			}
			continue
		}
		if _, after, ok := strings.Cut(line, "silence_end:"); ok && haveStart {
			field := strings.TrimSpace(after)
			parts := strings.Fields(field)
			if len(parts) > 0 {
				if v, err := strconv.ParseFloat(parts[0], 64); err == nil {
					out = append(out, Range{Start: start, End: v, Action: "skip"})
				}
			}
			haveStart = false
		}
	}
	return out
}
