package profanity

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// TTSEngine identifies the TTS backend to use.
type TTSEngine string

const (
	TTSNone   TTSEngine = "none"
	TTSPiper  TTSEngine = "piper"
	TTSSystem TTSEngine = "system"
)

// TTSConfig holds configuration for text-to-speech generation.
type TTSConfig struct {
	Engine  TTSEngine
	Model   string // piper model name or path
	Command string // system command template with {text} and {output} placeholders
}

// GenerateTTSClip generates a TTS audio clip for the given text.
// Returns the path to the generated audio file.
func GenerateTTSClip(ctx context.Context, cfg TTSConfig, text, outputPath string) error {
	switch cfg.Engine {
	case TTSPiper:
		return generatePiper(ctx, cfg.Model, text, outputPath)
	case TTSSystem:
		return generateSystem(ctx, cfg.Command, text, outputPath)
	default:
		return fmt.Errorf("TTS engine %q not configured", cfg.Engine)
	}
}

func generatePiper(ctx context.Context, model, text, outputPath string) error {
	if _, err := exec.LookPath("piper"); err != nil {
		return fmt.Errorf("piper not found in PATH")
	}
	args := []string{"--output_file", outputPath}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, "piper", args...)
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("piper: %w: %s", err, out)
	}
	return nil
}

func generateSystem(ctx context.Context, cmdTemplate, text, outputPath string) error {
	if cmdTemplate == "" {
		return fmt.Errorf("system TTS command not configured")
	}
	cmdStr := strings.ReplaceAll(cmdTemplate, "$TEXT", text)
	cmdStr = strings.ReplaceAll(cmdStr, "$OUTPUT", outputPath)
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("system TTS: %w: %s", err, out)
	}
	return nil
}

// CleanAudioFile produces a cleaned version of an audio/video file by replacing
// profane segments with silence or TTS audio. Uses ffmpeg for splicing.
// Returns the path to the cleaned file.
func CleanAudioFile(ctx context.Context, cfg TTSConfig, inputURL, outputPath string, ranges []plugin.AudioRange) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}

	if len(ranges) == 0 {
		return fmt.Errorf("no ranges to clean")
	}

	// Strategy:
	// 1. Download the source audio (if URL)
	// 2. For each range: generate silence or TTS clip
	// 3. Use ffmpeg filter_complex to replace segments

	tmpDir, err := os.MkdirTemp("", "profanity-clean-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Build ffmpeg filter graph.
	// Input 0: original audio
	// Inputs 1..N: replacement clips (silence or TTS)
	// Use amerge/amix to swap segments at the right timestamps.

	// Simpler approach: build an ffmpeg command that:
	// - Takes the input
	// - For each range, generates a volume=0 filter during that period (mute)
	// - Or if TTS is available, overlays the TTS clip

	if cfg.Engine == TTSNone || cfg.Engine == "" {
		// Mute-only mode: use volume filter.
		return muteSegments(ctx, inputURL, outputPath, ranges)
	}

	// TTS mode: generate clips, then splice.
	return spliceWithTTS(ctx, cfg, tmpDir, inputURL, outputPath, ranges)
}

// muteSegments uses ffmpeg to mute specific time ranges in an audio file.
func muteSegments(ctx context.Context, inputURL, outputPath string, ranges []plugin.AudioRange) error {
	// Build volume filter: volume=0 during each range, 1 elsewhere.
	// volume=enable='between(t,start,end)'
	var filters []string
	for _, r := range ranges {
		filters = append(filters, fmt.Sprintf("volume=enable='between(t,%.2f,%.2f)':volume=0", r.Start, r.End))
	}
	filterStr := strings.Join(filters, ",")

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-i", inputURL,
		"-af", filterStr,
		"-c:v", "copy",
		outputPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg mute: %w: %s", err, out)
	}
	return nil
}

// spliceWithTTS generates TTS clips for each range and overlays them on the original.
func spliceWithTTS(ctx context.Context, cfg TTSConfig, tmpDir, inputURL, outputPath string, ranges []plugin.AudioRange) error {
	// Generate TTS clips for each range.
	var clipPaths []string
	for i, r := range ranges {
		if r.ReplacementText == "" {
			clipPaths = append(clipPaths, "") // will mute instead
			continue
		}
		clipPath := filepath.Join(tmpDir, fmt.Sprintf("clip_%d.wav", i))
		if err := GenerateTTSClip(ctx, cfg, r.ReplacementText, clipPath); err != nil {
			clipPaths = append(clipPaths, "") // fallback to mute
			continue
		}
		clipPaths = append(clipPaths, clipPath)
	}

	// Build ffmpeg command with overlays.
	// First pass: mute all profane segments.
	mutedPath := filepath.Join(tmpDir, "muted.mp4")
	if err := muteSegments(ctx, inputURL, mutedPath, ranges); err != nil {
		return err
	}

	// Second pass: overlay TTS clips at the right positions.
	// Build inputs and amerge filter.
	args := []string{"-y", "-i", mutedPath}
	var overlayFilters []string
	inputIdx := 1

	for i, clipPath := range clipPaths {
		if clipPath == "" {
			continue
		}
		args = append(args, "-i", clipPath)
		// Delay the TTS clip to start at the right time.
		delayMs := int(ranges[i].Start * 1000)
		overlayFilters = append(overlayFilters,
			fmt.Sprintf("[%d]adelay=%d|%d[tts%d]", inputIdx, delayMs, delayMs, i))
		inputIdx++
	}

	if len(overlayFilters) == 0 {
		// No TTS clips generated, just use the muted version.
		return os.Rename(mutedPath, outputPath)
	}

	// Mix all TTS clips with the muted original.
	filterParts := overlayFilters
	mixInputs := "[0]"
	for i := range overlayFilters {
		mixInputs += fmt.Sprintf("[tts%d]", i)
	}
	// amix with duration=first to keep original length.
	nInputs := 1 + len(overlayFilters)
	filterParts = append(filterParts,
		fmt.Sprintf("%samix=inputs=%d:duration=first", mixInputs, nInputs))

	args = append(args, "-filter_complex", strings.Join(filterParts, ";"))
	args = append(args, "-c:v", "copy", outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Fallback: use muted version without TTS.
		os.Rename(mutedPath, outputPath)
		return fmt.Errorf("ffmpeg splice (falling back to muted): %w: %s", err, out)
	}
	return nil
}
