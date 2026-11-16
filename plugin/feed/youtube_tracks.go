package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// YouTube format selectors, shared by the downloader and remote HLS.
const (
	YouTubeVideoFormat = "bv[height<=1080][vcodec^=avc1]/bv[height<=1080]"
	YouTubeAudioFormat = "ba[ext=m4a]/ba"
)

// YouTubeFormat joins a video and an audio selector for yt-dlp. Each is a "/"
// chain, and "+" binds tighter than "/", so each is wrapped.
func YouTubeFormat(video, audio string) string {
	return "(" + video + ")+(" + audio + ")"
}

// YouTubeTrack is one of a YouTube video's two tracks, as googlevideo serves it.
type YouTubeTrack struct {
	Kind      hls.Kind
	FormatID  string
	URL       string
	ExpiresAt time.Time // from the URL's expire=, else now+1h (streamExpiry)
	// AvailableAt is yt-dlp's available_at: googlevideo refuses (403) the
	// URL until then. Zero when yt-dlp gave none.
	AvailableAt time.Time
}

// youtubeAvailableWait is the longest Cog waits for a URL's available_at.
// Further off, googlevideo is taken to have failed.
const youtubeAvailableWait = 30 * time.Second

// sleepFor waits d, or until ctx ends; tests replace it (SetAvailableSleep).
var sleepFor = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// youtubeAvailableSkew is added to every AvailableAt, because the time is
// Google's and the wait runs on Cog's clock: a clock a little behind would
// still ask too early and get a 403.
const youtubeAvailableSkew = time.Second

// AwaitAvailable returns once every track's URL is available (AvailableAt),
// taking now as the time: googlevideo answers 403 to a URL asked for before
// then. It waits for the latest, and not at all when every one has passed.
// A URL available more than youtubeAvailableWait ahead is an error at once,
// and so is ctx ending meanwhile, so a client that leaves is not waited for.
func AwaitAvailable(ctx context.Context, now time.Time, tracks ...YouTubeTrack) error {
	var wait time.Duration
	for _, t := range tracks {
		d := t.AvailableAt.Add(youtubeAvailableSkew).Sub(now)
		if t.AvailableAt.IsZero() || d <= 0 {
			continue
		}
		if d > youtubeAvailableWait {
			return fmt.Errorf("youtube format %s is available only in %s, more than %s away", t.FormatID, d, youtubeAvailableWait)
		}
		wait = max(wait, d)
	}
	if wait == 0 {
		return nil
	}
	if err := sleepFor(ctx, wait); err != nil {
		return fmt.Errorf("wait %s for youtube's available_at: %w", wait, err)
	}
	return nil
}

// ResolveYouTubeTracks resolves the video and audio formats of watchURL that
// format names (see YouTubeFormat), with one yt-dlp call.
func ResolveYouTubeTracks(ctx context.Context, watchURL, format string) (video, audio YouTubeTrack, err error) {
	if !ytdlpStreamAvailable() {
		return video, audio, fmt.Errorf("yt-dlp not found in PATH")
	}
	cmd, err := YTDLP(ctx, watchURL, nil,
		"--skip-download",
		"--no-warnings",
		"--no-playlist",
		"--format", format,
		"--print", "%(requested_formats)j",
	)
	if err != nil {
		return video, audio, err
	}
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return video, audio, fmt.Errorf("yt-dlp tracks: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return video, audio, fmt.Errorf("yt-dlp tracks: %w", err)
	}
	return parseRequestedFormats(out)
}

type requestedFormat struct {
	FormatID string `json:"format_id"`
	Protocol string `json:"protocol"`
	VCodec   string `json:"vcodec"`
	ACodec   string `json:"acodec"`
	URL      string `json:"url"`
	// AvailableAt is unix seconds; yt-dlp writes an integer, read as a
	// float in case it ever writes a fraction.
	AvailableAt float64 `json:"available_at"`
}

// parseRequestedFormats reads yt-dlp's --print "%(requested_formats)j".
func parseRequestedFormats(out []byte) (video, audio YouTubeTrack, err error) {
	var formats []requestedFormat
	if err := json.Unmarshal(bytes.TrimSpace(out), &formats); err != nil {
		return video, audio, fmt.Errorf("yt-dlp requested formats: %w (output %q)", err, truncate(string(out), 80))
	}
	if len(formats) != 2 {
		return video, audio, fmt.Errorf("yt-dlp requested formats: got %d, want a video and an audio track", len(formats))
	}
	for _, f := range formats {
		if f.Protocol != "https" {
			return video, audio, fmt.Errorf("youtube format %s: protocol %q is not https", f.FormatID, f.Protocol)
		}
		t := YouTubeTrack{FormatID: f.FormatID, URL: f.URL, ExpiresAt: streamExpiry(f.URL)}
		if f.AvailableAt > 0 {
			// Rounded up: a second early is a 403.
			t.AvailableAt = time.Unix(int64(math.Ceil(f.AvailableAt)), 0)
		}
		switch {
		case f.VCodec != "none" && f.ACodec == "none":
			t.Kind = hls.Video
			video = t
		case f.VCodec == "none" && f.ACodec != "none":
			t.Kind = hls.Audio
			audio = t
		default:
			return video, audio, fmt.Errorf("youtube format %s: vcodec %q and acodec %q fit neither a video nor an audio track", f.FormatID, f.VCodec, f.ACodec)
		}
	}
	if video.URL == "" || audio.URL == "" {
		return video, audio, errors.New("yt-dlp requested formats: need one video and one audio track")
	}
	return video, audio, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
