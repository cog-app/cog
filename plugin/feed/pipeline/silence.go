package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Silence computes the skip ranges of each ready download once, so a player
// asking for them is answered from the database instead of waiting on ffmpeg.
type Silence struct {
	store *feed.Store
	space feed.Space
}

// NewSilence creates a Silence worker over the downloads in space.
func NewSilence(s *feed.Store, space feed.Space) *Silence {
	return &Silence{store: s, space: space}
}

// RunOnce analyses every ready download not analysed yet.
func (w *Silence) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingSilence(100)
	if err != nil {
		return fmt.Errorf("silence list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processOne(ctx, id); err != nil {
			slog.Warn("feed silence: process failed", "item_id", id, "err", err)
		}
	}
	return nil
}

// processOne records an item's silence ranges. A file ffmpeg cannot read is
// recorded as having none, so it is not tried again every tick.
func (w *Silence) processOne(ctx context.Context, itemID int64) error {
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	media, err := w.space.Path(d.LocalPath)
	if err != nil {
		return fmt.Errorf("media path: %w", err)
	}
	ranges, derr := feed.DetectSilence(ctx, media)
	if derr != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err := w.store.SetSilenceRanges(itemID, ranges); err != nil {
		return err
	}
	if derr != nil {
		return fmt.Errorf("detect silence: %w", derr)
	}
	return nil
}
