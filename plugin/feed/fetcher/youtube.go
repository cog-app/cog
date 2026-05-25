package fetcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// ytdlpInfo holds metadata extracted by yt-dlp for a single video entry.
type ytdlpInfo struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Duration   float64 `json:"duration"`
	Thumbnail  string  `json:"thumbnail"`
	Uploader   string  `json:"uploader"`
	Channel    string  `json:"channel"`
	WebpageURL string  `json:"webpage_url"`
}

// ytdlpPlaylistMeta holds metadata for a playlist/channel.
type ytdlpPlaylistMeta struct {
	Title       string `json:"title"`
	Uploader    string `json:"uploader"`
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

// ytdlpChannelMeta fetches just the playlist/channel metadata without downloading
// individual video info.
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

// fetchYouTube fetches video metadata from a YouTube channel or playlist via yt-dlp.
func fetchYouTube(ctx context.Context, sub feed.Subscription) (feed.FetchResult, error) {
	entries, err := ytdlpListEntries(ctx, sub.URL)
	if err != nil {
		return feed.FetchResult{}, err
	}
	meta, _ := ytdlpChannelMeta(ctx, sub.URL) // best-effort; ignore failure

	items := make([]feed.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, feed.Item{
			Type:         "video",
			GUID:         e.ID,
			Title:        e.Title,
			Author:       firstNonEmpty(e.Uploader, e.Channel),
			URL:          e.WebpageURL,
			MediaURL:     e.WebpageURL,
			DurationSec:  int64(e.Duration),
			ThumbnailURL: e.Thumbnail,
			// PublishedAt: leave 0; flat-playlist response doesn't include it.
		})
	}

	r := feed.FetchResult{Items: items}
	if meta != nil {
		r.SubscriptionUpdates = map[string]string{
			"title":       meta.Title,
			"description": meta.Description,
			"image_url":   meta.Thumbnail,
		}
	}
	return r, nil
}

// firstNonEmpty returns a if non-empty, otherwise b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
