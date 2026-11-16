package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// YouTubeTrack is one of a YouTube video's two tracks, as googlevideo serves it.
type YouTubeTrack struct {
	Kind      hls.Kind
	FormatID  string
	URL       string
	ExpiresAt time.Time // from the URL's expire=, else now+1h (streamExpiry)
}

// ResolveYouTubeTracks resolves the video and audio formats of watchURL that
// format names ("<video>+<audio>"), with one yt-dlp call. Each selector is
// a "/" chain, so wrap them: "(<video>)+(<audio>)".
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
