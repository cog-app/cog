package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

// Processor produces a profanity-muted audio file at `out` from `in`, using
// the captions to locate spans to mute.
type Processor interface {
	Process(ctx context.Context, in, out string, captions []cogplugin.CaptionSegment) error
}

// ProfanityPluginProcessor adapts plugin/profanity into a Processor.
type ProfanityPluginProcessor struct {
	Plugin   *profanity.Plugin
	Username string
}

func (p *ProfanityPluginProcessor) Process(ctx context.Context, in, out string, captions []cogplugin.CaptionSegment) error {
	return p.Plugin.CleanAudioFile(ctx, p.Username, in, out, captions)
}

// Profanity worker — drains items with needs_profanity + ready download +
// captions_path present, runs the Processor, records clean_audio_path.
type Profanity struct {
	store     *feed.Store
	processor Processor
}

func NewProfanity(s *feed.Store, p Processor) *Profanity {
	return &Profanity{store: s, processor: p}
}

func (w *Profanity) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingProfanityWithCaptions(100)
	if err != nil {
		return fmt.Errorf("profanity list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processOne(ctx, id); err != nil {
			slog.Warn("feed profanity: process failed", "item_id", id, "err", err)
		}
	}
	return nil
}

func (w *Profanity) processOne(ctx context.Context, itemID int64) error {
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	if d.LocalPath == "" {
		return fmt.Errorf("local_path empty for item %d", itemID)
	}
	if d.CaptionsPath == "" {
		return fmt.Errorf("captions_path empty for item %d", itemID)
	}
	captionsFile, err := os.Open(d.CaptionsPath)
	if err != nil {
		return fmt.Errorf("open captions: %w", err)
	}
	captions := profanity.ParseVTT(captionsFile)
	captionsFile.Close()

	out := cleanPath(d.LocalPath)
	if err := w.processor.Process(ctx, d.LocalPath, out, captions); err != nil {
		return fmt.Errorf("processor: %w", err)
	}
	if err := w.store.SetCleanAudioPath(itemID, out); err != nil {
		return fmt.Errorf("set clean path: %w", err)
	}
	if err := w.store.ClearNeedsProfanity(itemID); err != nil {
		return fmt.Errorf("clear needs_profanity: %w", err)
	}
	return nil
}

// cleanPath inserts ".clean" before the file extension.
//
//	foo.mp3 -> foo.clean.mp3
//	foo     -> foo.clean
func cleanPath(p string) string {
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	if ext == "" {
		return base + ".clean"
	}
	return base + ".clean" + ext
}
