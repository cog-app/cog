package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Processor applies a transformation that produces a profanity-muted audio
// file at `out` from the input at `in`.
type Processor interface {
	Process(ctx context.Context, in, out string) error
}

// Profanity is a worker that drains items where needs_profanity=1 and the
// download is ready, runs the Processor, and records the clean path.
type Profanity struct {
	store     *feed.Store
	processor Processor
}

// NewProfanity creates a Profanity worker backed by the given store and processor.
func NewProfanity(s *feed.Store, p Processor) *Profanity {
	return &Profanity{store: s, processor: p}
}

// RunOnce processes all pending profanity items whose download is ready.
func (w *Profanity) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingProfanityReady(100)
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

	out := cleanPath(d.LocalPath)
	if err := w.processor.Process(ctx, d.LocalPath, out); err != nil {
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
//	foo.tar.gz -> foo.tar.clean.gz  (filepath.Ext returns only the last segment)
//	foo (no ext) -> foo.clean
func cleanPath(p string) string {
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	if ext == "" {
		return base + ".clean"
	}
	return base + ".clean" + ext
}
