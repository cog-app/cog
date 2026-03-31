package media

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ytdlpInfo holds metadata extracted by yt-dlp.
type ytdlpInfo struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Duration    float64 `json:"duration"`
	Thumbnail   string  `json:"thumbnail"`
	Uploader    string  `json:"uploader"`
	Channel     string  `json:"channel"`
	WebpageURL  string  `json:"webpage_url"`
}

// ytdlpPlaylistMeta holds metadata for a playlist/channel itself.
type ytdlpPlaylistMeta struct {
	Title       string `json:"title"`
	Uploader    string `json:"uploader"`
	Channel     string `json:"channel"`
	Description string `json:"description"`
	Thumbnail   string `json:"thumbnail"`
}

// ytdlpAvailable reports whether yt-dlp is in PATH.
func ytdlpAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// ytdlpListEntries runs yt-dlp --flat-playlist to list videos in a channel/playlist.
func ytdlpListEntries(ctx context.Context, url string) ([]ytdlpInfo, error) {
	if !ytdlpAvailable() {
		return nil, fmt.Errorf("yt-dlp not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--flat-playlist",
		"--dump-json",
		"--no-warnings",
		"--no-download",
		"--playlist-end", "50",
		url,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("yt-dlp start: %w", err)
	}

	var entries []ytdlpInfo
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		var info ytdlpInfo
		if err := json.Unmarshal(scanner.Bytes(), &info); err != nil {
			continue
		}
		entries = append(entries, info)
	}

	if err := cmd.Wait(); err != nil {
		return entries, fmt.Errorf("yt-dlp exit: %w", err)
	}
	return entries, scanner.Err()
}

// ytdlpStreamURL resolves a YouTube watch URL to a direct stream URL.
func ytdlpStreamURL(ctx context.Context, watchURL string) (string, error) {
	if !ytdlpAvailable() {
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

// ytdlpChannelMeta fetches just the playlist/channel metadata (title, uploader)
// without downloading individual video info.
func ytdlpChannelMeta(ctx context.Context, url string) (*ytdlpPlaylistMeta, error) {
	if !ytdlpAvailable() {
		return nil, fmt.Errorf("yt-dlp not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--dump-single-json",
		"--flat-playlist",
		"--playlist-items", "0",
		"--no-warnings",
		url,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp channel meta: %w", err)
	}
	var meta ytdlpPlaylistMeta
	if err := json.Unmarshal(out, &meta); err != nil {
		return nil, fmt.Errorf("yt-dlp parse: %w", err)
	}
	return &meta, nil
}

// ytdlpVideoInfo gets detailed info for a single URL.
func ytdlpVideoInfo(ctx context.Context, url string) (*ytdlpInfo, error) {
	if !ytdlpAvailable() {
		return nil, fmt.Errorf("yt-dlp not found in PATH")
	}
	cmd := exec.CommandContext(ctx, "yt-dlp",
		"--dump-json",
		"--no-download",
		"--no-warnings",
		"--no-playlist",
		url,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp info: %w", err)
	}
	var info ytdlpInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("yt-dlp parse: %w", err)
	}
	return &info, nil
}
