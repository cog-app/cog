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
// using yt-dlp. Used by the StreamURL handler.
func ResolveYouTubeStreamURL(ctx context.Context, watchURL string) (string, error) {
	if !ytdlpStreamAvailable() {
		return "", fmt.Errorf("yt-dlp not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--get-url",
		"--format", "best[height<=1080]",
		"--no-warnings",
		watchURL,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp stream-url: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
