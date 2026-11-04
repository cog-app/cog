package gallery

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/ffargs"
)

var (
	ffmpegPath  string
	ffprobePath string
)

func init() {
	ffmpegPath, _ = exec.LookPath("ffmpeg")
	ffprobePath, _ = exec.LookPath("ffprobe")
}

// FFmpegAvailable reports whether ffmpeg is installed.
func FFmpegAvailable() bool {
	return ffmpegPath != ""
}

// FFprobeAvailable reports whether ffprobe is installed.
func FFprobeAvailable() bool {
	return ffprobePath != ""
}

// videoInfo holds extracted video metadata.
type videoInfo struct {
	Width        int
	Height       int
	DurationMs   int64
	VideoCodec   string
	AudioCodec   string
	CreationTime *time.Time
}

// ffprobeInfo extracts metadata from a video file using ffprobe.
func ffprobeInfo(path string) (*videoInfo, error) {
	if !FFprobeAvailable() {
		return nil, fmt.Errorf("ffprobe not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	args := append([]string{"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams"}, ffargs.Probe(path)...)
	cmd := exec.CommandContext(ctx, ffprobePath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}

	var probe struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string            `json:"duration"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}

	info := &videoInfo{}
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if info.VideoCodec == "" {
				info.VideoCodec = s.CodecName
				info.Width = s.Width
				info.Height = s.Height
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}

	if probe.Format.Duration != "" {
		if d, err := strconv.ParseFloat(probe.Format.Duration, 64); err == nil {
			info.DurationMs = int64(d * 1000)
		}
	}

	// Try to parse creation_time from format tags.
	if ct, ok := probe.Format.Tags["creation_time"]; ok {
		if t, err := time.Parse(time.RFC3339, ct); err == nil {
			info.CreationTime = &t
		}
	}

	return info, nil
}

// ffmpegThumbnail generates a JPEG thumbnail from a video file.
func ffmpegThumbnail(srcPath, dstPath string, size int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	scaleFilter := fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", size, size)
	args := append(ffargs.Input(srcPath), "-ss", "00:00:01", "-vframes", "1", "-vf", scaleFilter, "-q:v", "5", "-y", dstPath)
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg thumbnail: %w: %s", err, out)
	}
	return nil
}
