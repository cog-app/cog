package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"

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
	space     feed.Space
	processor Processor
}

// NewProfanity creates a Profanity worker keeping cleaned audio in space.
func NewProfanity(s *feed.Store, space feed.Space, p Processor) *Profanity {
	return &Profanity{store: s, space: space, processor: p}
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Recorded so edges stop waiting for a clean rendition; the
			// flag stays and the next tick tries again.
			if err := w.store.MarkProfanityError(id, err.Error()); err != nil {
				slog.Error("feed profanity: record error", "item_id", id, "err", err)
			}
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
	captionsFile, err := w.space.Own.Open(feed.Name(d.CaptionsPath))
	if err != nil {
		return fmt.Errorf("open captions: %w", err)
	}
	captions := profanity.ParseVTT(captionsFile)
	if err := captionsFile.Close(); err != nil {
		return fmt.Errorf("close captions: %w", err)
	}

	in, err := w.space.Path(d.LocalPath)
	if err != nil {
		return fmt.Errorf("media path: %w", err)
	}
	name := cleanName(itemID, d.LocalPath)
	tmp := "clean/" + path.Base(name)
	out, err := w.space.Cache.LocalPath(tmp)
	if err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := w.processor.Process(ctx, in, out, captions); err != nil {
		if errors.Is(err, profanity.ErrNoRanges) {
			// Nothing to mute: no clean audio, and an older one is stale.
			// The file it named is Collect's to remove.
			return errors.Join(
				w.space.Cache.Remove(tmp),
				w.store.SetCleanAudioPath(itemID, ""),
				w.store.ClearNeedsProfanity(itemID),
			)
		}
		return errors.Join(fmt.Errorf("processor: %w", err), w.space.Cache.Remove(tmp))
	}
	if _, err := w.space.Keep(name, tmp); err != nil {
		return errors.Join(fmt.Errorf("keep clean audio: %w", err), w.space.Cache.Remove(tmp))
	}
	if err := w.store.SetCleanAudioPath(itemID, name); err != nil {
		return fmt.Errorf("set clean path: %w", err)
	}
	if err := w.store.ClearNeedsProfanity(itemID); err != nil {
		return fmt.Errorf("clear needs_profanity: %w", err)
	}
	return nil
}

// cleanName is the storage name of an item's cleaned audio file,
// "media/<id>.clean" plus the extension of the download's file. It does not
// depend on the name of any track made from the download, so cleaning again
// never overwrites a track a player is reading.
func cleanName(itemID int64, localPath string) string {
	return fmt.Sprintf("media/%d.clean%s", itemID, path.Ext(localPath))
}
