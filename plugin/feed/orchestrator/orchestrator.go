// Package orchestrator drives the feed plugin's background workers.
//
// It lives in its own sub-package to break the import cycle that would arise
// if feed.go imported feed/fetcher (which already imports feed).
package orchestrator

import (
	"context"
	"log/slog"
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
//   - tick: how often to run a full pass over all users.
//   - log: structured logger.
func Run(ctx context.Context, plug *feed.Plugin, profPlugin *profanity.Plugin, tick time.Duration, log *slog.Logger) {
	initial := min(tick, 2*time.Second)

	timer := time.NewTimer(initial)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		tickOnce(ctx, plug, profPlugin, log)
	}

	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickOnce(ctx, plug, profPlugin, log)
		case poke := <-plug.Pokes():
			if poke.Username != "" {
				processUserPoke(ctx, plug, profPlugin, poke, log)
			}
		}
	}
}

func processUserPoke(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, poke feed.PokeRequest, log *slog.Logger) {
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
	processUser(ctx, plug, prof, poke.Username, log)
}

func tickOnce(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, log *slog.Logger) {
	feed.RecordTick()
	usernames, err := plug.Users()
	if err != nil {
		log.Error("feed orchestrator: list users", "err", err)
		return
	}
	for _, username := range usernames {
		if err := ctx.Err(); err != nil {
			return
		}
		processUser(ctx, plug, prof, username, log)
	}
}

func processUser(ctx context.Context, plug *feed.Plugin, prof *profanity.Plugin, username string, log *slog.Logger) {
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

	space, err := plug.Space(username)
	if err != nil {
		log.Error("feed orchestrator: storage", "user", username, "err", err)
		return
	}
	if err := pipeline.Collect(store, space); err != nil {
		log.Error("feed orchestrator: collect", "user", username, "err", err)
	}

	dl := pipeline.NewDownloader(store, space)
	if err := dl.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: download", "user", username, "err", err)
	}

	sil := pipeline.NewSilence(store, space)
	if err := sil.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: silence", "user", username, "err", err)
	}

	tx := pipeline.NewTranscribe(store, space)
	if err := tx.RunOnce(ctx); err != nil {
		log.Error("feed orchestrator: transcribe", "user", username, "err", err)
	}

	if prof != nil {
		proc := &pipeline.ProfanityPluginProcessor{Plugin: prof, Username: username}
		pf := pipeline.NewProfanity(store, space, proc)
		if err := pf.RunOnce(ctx); err != nil {
			log.Error("feed orchestrator: profanity", "user", username, "err", err)
		}
	}
}
