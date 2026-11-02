// Package orchestrator drives the feed plugin's background workers.
//
// It lives in its own sub-package to break the import cycle that would arise
// if feed.go imported feed/fetcher (which already imports feed).
package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

// Run drives the feed plugin's background workers until ctx is canceled.
//
//   - plug: the feed plugin (for DB access and PluginID).
//   - profPlugin: optional profanity plugin; nil disables the profanity worker.
//   - dataDir: the cog data-directory root.
//   - tick: how often to run a full pass over all users.
//   - log: structured logger.
func Run(ctx context.Context, plug *feed.Plugin, profPlugin *profanity.Plugin, dataDir string, tick time.Duration, log *slog.Logger) {
	initial := min(tick, 2*time.Second)

	timer := time.NewTimer(initial)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		tickOnce(ctx, plug, profPlugin, dataDir, log)
	}

	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickOnce(ctx, plug, profPlugin, dataDir, log)
		case poke := <-plug.Pokes():
			if poke.Username != "" {
				processUserPoke(ctx, plug, profPlugin, dataDir, poke, log)
			}
		}
	}
}

func processUserPoke(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, dataDir string, poke feed.PokeRequest, log *slog.Logger) {
	db, err := plug.DB(poke.Username)
	if err != nil {
		log.Error("feed orchestrator: poke open db", "user", poke.Username, "err", err)
		return
	}
	if poke.SubID != 0 {
		// Set next_check_at=1 so the poller picks it up immediately.
		store := feed.NewStore(db)
		if err := store.UpdateSubscriptionNextCheck(poke.SubID, 1); err != nil {
			log.Error("feed orchestrator: poke set next check", "user", poke.Username, "sub", poke.SubID, "err", err)
		}
	}
	processUser(ctx, plug, prof, dataDir, poke.Username, log)
}

func tickOnce(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, dataDir string, log *slog.Logger) {
	feed.RecordTick()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		log.Error("feed orchestrator: read data dir", "err", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()
		dbPath := filepath.Join(dataDir, username, "app_data", feed.PluginID, "plugin.db")
		if _, err := os.Stat(dbPath); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Error("feed orchestrator: stat db", "user", username, "err", err)
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		processUser(ctx, plug, prof, dataDir, username, log)
	}
}

func processUser(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, dataDir, username string, log *slog.Logger) {
	feed.RecordUserProcessed(username)
	db, err := plug.DB(username)
	if err != nil {
		log.Error("feed orchestrator: open db", "user", username, "err", err)
		return
	}
	store := feed.NewStore(db)

	poller := feed.NewPoller(store).WithFetcher(func(ctx context.Context, sub feed.Subscription) (feed.FetchResult, error) {
		return fetcher.Fetch(ctx, sub)
	})
	if err := poller.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: poll", "user", username, "err", err)
	}

	mediaRoot := filepath.Join(dataDir, username, "app_data", feed.PluginID, "media")
	captionsRoot := filepath.Join(dataDir, username, "app_data", feed.PluginID, "captions")

	dl := pipeline.NewDownloader(store, mediaRoot)
	if err := dl.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: download", "user", username, "err", err)
	}

	tx := pipeline.NewTranscribe(store, captionsRoot)
	if err := tx.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: transcribe", "user", username, "err", err)
	}

	if prof != nil {
		proc := &pipeline.ProfanityPluginProcessor{Plugin: prof, Username: username}
		pf := pipeline.NewProfanity(store, proc)
		if err := pf.RunOnce(ctx); err != nil {
			log.Error("feed orchestrator: profanity", "user", username, "err", err)
		}
	}
}
