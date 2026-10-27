package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/webpush"
)

// Messenger writes the message owed to a subscription of a protocol.
type Messenger func(s *Subscription) (webpush.Message, error)

// Deliverer sends what is owed to subscriptions.
type Deliverer struct {
	Store  *Store
	Sender *webpush.Sender
	// Messages writes each protocol's messages. A subscription of a
	// protocol without one is left owing.
	Messages map[Protocol]Messenger
	// Sources mark the subscriptions owed a push before each round, from
	// the changes their protocol collected since the last.
	Sources []func(context.Context) error
	// Cleanups remove subscriptions their protocol no longer serves, such
	// as those to a deleted resource; they run with Prune.
	Cleanups []func(context.Context) error
	Log      *slog.Logger

	once sync.Once
	wake chan struct{}
}

const (
	// lease is how long a claimed subscription is left to its deliverer
	// before another may try: longer than a push request can take.
	lease = 2 * time.Minute
	// claimBatch is how many subscriptions one round claims.
	claimBatch = 100
	// firstRetry is the wait after a first failure; it doubles with each
	// one after, up to maxRetry.
	firstRetry = 30 * time.Second
	maxRetry   = 6 * time.Hour
)

// Backoff is the wait before trying again after failures consecutive
// failures.
func Backoff(failures int) time.Duration {
	d := firstRetry
	for i := 1; i < failures && d < maxRetry; i++ {
		d *= 2
	}
	return min(d, maxRetry)
}

// Deliver sends every due subscription what it is owed, until none is due.
// A subscription the push service no longer knows (404, 410) is removed;
// another failure is logged and tried again later, backing off. It returns
// an error only when the store fails.
func (d *Deliverer) Deliver(ctx context.Context) error {
	for {
		subs, err := d.Store.claim(lease, claimBatch)
		if err != nil {
			return err
		}
		if len(subs) == 0 {
			return nil
		}
		for i := range subs {
			if err := d.deliver(ctx, &subs[i]); err != nil {
				return err
			}
		}
		if len(subs) < claimBatch {
			return nil
		}
	}
}

func (d *Deliverer) deliver(ctx context.Context, s *Subscription) error {
	write, ok := d.Messages[s.Protocol]
	if !ok {
		return d.Store.failed(s, "no messages for protocol "+string(s.Protocol), d.Store.clock().Add(maxRetry))
	}
	msg, err := write(s)
	if errors.Is(err, ErrSkip) {
		return d.Store.delivered(s)
	}
	if err == nil {
		err = d.Sender.Send(ctx, s.Endpoint, s.Keys, msg)
	}
	var se *webpush.StatusError
	switch {
	case err == nil:
		return d.Store.delivered(s)
	case errors.Is(err, webpush.ErrGone):
		d.Log.Info("push subscription gone; removed", "subscription", s.ID, "user", s.UserID, "protocol", s.Protocol)
		return d.Store.Remove(s.ID)
	case errors.As(err, &se):
		wait := max(Backoff(s.Failures+1), se.RetryAfter)
		d.Log.Warn("push refused; will retry", "subscription", s.ID, "user", s.UserID, "status", se.Code,
			"failures", s.Failures+1, "retry_in", wait, "err", err)
		return d.Store.failed(s, fmt.Sprintf("push service answered %d", se.Code), d.Store.clock().Add(wait))
	default:
		// The endpoint is the client's secret, so it is left out.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = fmt.Errorf("post to push service: %w", ue.Err)
		}
		wait := Backoff(s.Failures + 1)
		d.Log.Warn("push failed; will retry", "subscription", s.ID, "user", s.UserID,
			"failures", s.Failures+1, "retry_in", wait, "err", err)
		return d.Store.failed(s, err.Error(), d.Store.clock().Add(wait))
	}
}
