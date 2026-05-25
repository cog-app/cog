package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Transcribe is a v1 stub worker that consumes the needs_transcribe flag
// without producing captions. A real backend will replace this in a future
// release.
type Transcribe struct {
	store *feed.Store
}

// NewTranscribe creates a Transcribe worker backed by the given store.
func NewTranscribe(s *feed.Store) *Transcribe {
	return &Transcribe{store: s}
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
		slog.Info("feed transcribe stub: clearing flag (no captions backend in v1)", "item_id", id)
		if err := w.store.ClearNeedsTranscribe(id); err != nil {
			slog.Warn("feed transcribe: clear flag failed", "item_id", id, "err", err)
		}
	}
	return nil
}
