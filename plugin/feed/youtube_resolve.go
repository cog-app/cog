package feed

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ytdlpStreamAvailable reports whether yt-dlp is in PATH.
func ytdlpStreamAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// ResolveYouTubeStreamURL resolves a YouTube watch URL to a direct stream URL
// using yt-dlp: the best audio-only stream when audio is set, a combined one up
// to 1080p otherwise. Used by the StreamURL handler.
func ResolveYouTubeStreamURL(ctx context.Context, watchURL string, audio bool) (string, error) {
	if !ytdlpStreamAvailable() {
		return "", fmt.Errorf("yt-dlp not found in PATH")
	}
	format := "best[height<=1080]"
	if audio {
		format = "bestaudio/best"
	}
	cmd, err := YTDLP(ctx, watchURL, nil,
		"--get-url",
		"--format", format,
		"--no-warnings",
	)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp stream-url: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
