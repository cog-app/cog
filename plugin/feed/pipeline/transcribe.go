package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Transcribe produces caption files (VTT) for downloaded audio/video items.
//
// Strategy:
//   - For YouTube items, try yt-dlp auto-captions first (fast, free).
//   - For everything else (or YouTube fallback), run whisper-cli on the
//     downloaded media file.
//
// If transcription is disabled (whisper_model == "none") and the item is not
// a YouTube item with auto-captions, the worker logs a warning, clears the
// needs_transcribe flag, and continues. The profanity worker will skip items
// without a captions_path.
type Transcribe struct {
	store *feed.Store
	space feed.Space
}

// NewTranscribe creates a Transcribe worker backed by the given store,
// keeping captions in space.
func NewTranscribe(s *feed.Store, space feed.Space) *Transcribe {
	return &Transcribe{store: s, space: space}
}

// RunOnce processes all pending transcribe items whose download is ready.
func (w *Transcribe) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingTranscribeReady(100)
	if err != nil {
		return fmt.Errorf("transcribe list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processOne(ctx, id); err != nil {
			slog.Warn("feed transcribe: process failed", "item_id", id, "err", err)
		}
	}
	return nil
}

func (w *Transcribe) processOne(ctx context.Context, itemID int64) error {
	it, err := w.store.GetItem(itemID)
	if err != nil {
		return fmt.Errorf("get item: %w", err)
	}
	sub, err := w.store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}
	file := fmt.Sprintf("%d.vtt", itemID)
	tmp := "captions/" + file
	outPath, err := w.space.Cache.LocalPath(tmp)
	if err != nil {
		return fmt.Errorf("captions output: %w", err)
	}

	// Try YouTube auto-captions first.
	if sub.Kind == "youtube" && it.MediaURL != "" {
		if err := tryYouTubeCaptions(ctx, it.MediaURL, outPath); err == nil {
			return w.recordCaptions(itemID, tmp)
		} else {
			slog.Info("feed transcribe: yt-dlp captions unavailable, falling back", "item_id", itemID, "err", err)
		}
	}

	// Fall back to whisper-cli on the downloaded file.
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	if d.LocalPath == "" {
		// Nothing to transcribe; clear flag so we don't loop forever.
		if err := w.store.ClearNeedsTranscribe(itemID); err != nil {
			return fmt.Errorf("clear needs_transcribe: %w", err)
		}
		return fmt.Errorf("no local file to transcribe")
	}

	model := feed.SettingWhisperModel.MustGet(w.store.DB())
	if model == feed.WhisperNone {
		slog.Warn("feed transcribe: whisper disabled, skipping", "item_id", itemID)
		return w.store.ClearNeedsTranscribe(itemID)
	}

	cliPath := feed.SettingWhisperCLIPath.MustGet(w.store.DB())
	if cliPath == "" {
		cliPath = "whisper-cli"
	}
	modelsDir := feed.SettingWhisperModelsDir.MustGet(w.store.DB())
	if modelsDir == "" {
		if modelsDir, err = w.space.Cache.LocalPath("whisper-models"); err != nil {
			return fmt.Errorf("whisper models: %w", err)
		}
	}
	modelPath, err := feed.EnsureWhisperModel(modelsDir, model)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	media, err := w.space.Path(d.LocalPath)
	if err != nil {
		return fmt.Errorf("media path: %w", err)
	}
	prefix := strings.TrimSuffix(outPath, ".vtt")
	// whisper.cpp decodes the downloaded media itself.
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{modelPath, media}, Write: []string{filepath.Dir(outPath)}}, cliPath,
		"-m", modelPath,
		"-f", media,
		"--output-vtt",
		"-of", prefix,
		"-nt",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("whisper-cli: %w: %s", err, string(out))
	}
	if _, err := os.Stat(outPath); err != nil {
		return fmt.Errorf("whisper output missing at %s: %w", outPath, err)
	}
	return w.recordCaptions(itemID, tmp)
}

// recordCaptions keeps the captions written at the cache's name tmp as the
// item's.
func (w *Transcribe) recordCaptions(itemID int64, tmp string) error {
	name := "captions/" + path.Base(tmp)
	if _, err := w.space.Keep(name, tmp); err != nil {
		return errors.Join(fmt.Errorf("keep captions: %w", err), w.space.Cache.Remove(tmp))
	}
	if err := w.store.SetCaptionsPath(itemID, name); err != nil {
		return fmt.Errorf("set captions path: %w", err)
	}
	if err := w.store.ClearNeedsTranscribe(itemID); err != nil {
		return fmt.Errorf("clear needs_transcribe: %w", err)
	}
	return nil
}

// tryYouTubeCaptions invokes yt-dlp to fetch auto-generated English captions
// for a YouTube URL into outPath (VTT). yt-dlp natively writes to
// <prefix>.<lang>.vtt, so we ask it to write to a temp prefix and then copy
// the resulting file.
func tryYouTubeCaptions(ctx context.Context, videoURL, outPath string) error {
	if err := feed.CheckURL(videoURL); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "feed-yt-captions-*")
	if err != nil {
		return fmt.Errorf("mkdtmp: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			slog.Warn("feed transcribe: remove temp dir", "path", tmpDir, "err", err)
		}
	}()

	prefix := filepath.Join(tmpDir, "out")
	cmd, err := feed.YTDLP(ctx, videoURL, []string{tmpDir},
		"--write-auto-sub",
		"--sub-lang", "en",
		"--sub-format", "vtt",
		"--skip-download",
		"--no-warnings",
		"-o", prefix,
	)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("yt-dlp captions: %w: %s", err, string(out))
	}
	matches, err := filepath.Glob(prefix + "*.vtt")
	if err != nil {
		return fmt.Errorf("find captions: %w", err)
	}
	if len(matches) == 0 {
		return errors.New("yt-dlp produced no captions file")
	}
	// Copy first match to outPath.
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return fmt.Errorf("read captions: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write captions: %w", err)
	}
	return nil
}
