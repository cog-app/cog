package feed

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// FetchResult is the result of a subscription poll. It lives in the feed package
// (rather than feed/fetcher) so the poller can reference it without creating an
// import cycle (feed/fetcher already imports feed for Subscription and Item).
type FetchResult struct {
	Items               []Item
	ETag                string
	LastModified        string
	SubscriptionUpdates map[string]string
	NotModified         bool
}

// FetchFunc is the function signature for fetching a subscription.
// Wire the real implementation (fetcher.Fetch adapted to return FetchResult) from
// outside the feed package to avoid the import cycle.
type FetchFunc func(ctx context.Context, sub Subscription) (FetchResult, error)

// Poller coordinates fetching due subscriptions and writing results back to the store.
type Poller struct {
	store   *Store
	fetchFn FetchFunc
	engine  *FilterEngine
}

// NewPoller creates a Poller. Call WithFetcher before RunOnce to supply the fetch
// implementation; without it, polling will fail with an error.
func NewPoller(s *Store) *Poller {
	return &Poller{store: s}
}

// WithFetcher sets the FetchFunc used by the poller. Returns the receiver for
// convenient chaining:
//
//	p := feed.NewPoller(store).WithFetcher(myFetch)
func (p *Poller) WithFetcher(fn FetchFunc) *Poller {
	p.fetchFn = fn
	return p
}

// WithFilterEngine attaches a FilterEngine that will be applied to each
// newly-inserted item. If not set (or nil), filter evaluation is skipped.
// Returns the receiver for convenient chaining:
//
//	p := feed.NewPoller(store).WithFetcher(myFetch).WithFilterEngine(eng)
func (p *Poller) WithFilterEngine(e *FilterEngine) *Poller {
	p.engine = e
	return p
}

// RunOnce polls every currently-due subscription exactly once and returns.
func (p *Poller) RunOnce(ctx context.Context) error {
	if p.fetchFn == nil {
		return fmt.Errorf("poller: no fetch function configured; call WithFetcher")
	}
	subs, err := p.store.ListDueSubscriptions(time.Now().Unix())
	if err != nil {
		return fmt.Errorf("poller list due: %w", err)
	}
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return err
		}
		p.pollOne(ctx, sub)
	}
	return nil
}

// Run loops, polling due subscriptions every tick. Returns when ctx is canceled.
func (p *Poller) Run(ctx context.Context, tick time.Duration) error {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if err := p.RunOnce(ctx); err != nil {
			slog.Warn("feed poller: run once", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (p *Poller) pollOne(ctx context.Context, sub Subscription) {
	res, err := p.fetchFn(ctx, sub)
	if err != nil {
		next := backoffNextCheck(sub, time.Now().Unix())
		if dbErr := p.store.RecordSubscriptionError(sub.ID, err.Error(), next); dbErr != nil {
			slog.Error("feed poller: record error failed", "subscription_id", sub.ID, "err", dbErr, "fetch_err", err)
			return
		}
		slog.Warn("feed poller: fetch failed", "subscription_id", sub.ID, "url", sub.URL, "err", err)
		return
	}

	now := time.Now().Unix()
	next := now + sub.PollIntervalSec
	if sub.PollIntervalSec == 0 {
		next = now + 1800
	}

	if res.NotModified {
		if err := p.store.UpdateSubscriptionPollState(sub.ID, res.ETag, res.LastModified, next, now, now); err != nil {
			slog.Error("feed poller: update poll state (not modified)", "subscription_id", sub.ID, "err", err)
		}
		return
	}

	for _, it := range res.Items {
		it.SubscriptionID = sub.ID
		id, inserted, err := p.store.UpsertItem(it)
		if err != nil {
			slog.Warn("feed poller: upsert item", "subscription_id", sub.ID, "guid", it.GUID, "err", err)
			continue
		}
		if inserted && p.engine != nil {
			if ferr := p.engine.Apply(id); ferr != nil {
				slog.Warn("feed poller: filter apply", "item_id", id, "subscription_id", sub.ID, "err", ferr)
			}
		}
	}

	if len(res.SubscriptionUpdates) > 0 {
		if err := p.store.UpdateSubscriptionMetadata(sub.ID, res.SubscriptionUpdates); err != nil {
			slog.Warn("feed poller: update metadata", "subscription_id", sub.ID, "err", err)
		}
	}

	if err := p.store.UpdateSubscriptionPollState(sub.ID, res.ETag, res.LastModified, next, now, now); err != nil {
		slog.Error("feed poller: update poll state", "subscription_id", sub.ID, "err", err)
	}
}

// backoffNextCheck returns now + min(poll_interval * 2^error_count, 86400)
// using the current error_count before it is incremented.
func backoffNextCheck(sub Subscription, now int64) int64 {
	interval := sub.PollIntervalSec
	if interval <= 0 {
		interval = 1800
	}
	shift := sub.ErrorCount // shift by current count (next error increments to count+1)
	if shift > 12 {
		shift = 12 // cap to avoid overflow
	}
	delta := interval << shift
	if delta > 86400 {
		delta = 86400
	}
	if delta < 1 {
		delta = 1
	}
	return now + delta
}
