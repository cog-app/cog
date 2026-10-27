package push

import (
	"context"
	"time"
)

const (
	// Debounce is how long a wake waits before marking and delivering, so
	// that the writes of an import or a sync that commit one at a time
	// make one push rather than one each.
	Debounce = time.Second
	// sweep is how often due retries are looked for without a wake.
	sweep = time.Minute
	// pruneEvery is how often expired subscriptions are deleted.
	pruneEvery = time.Hour
)

// Wake asks Run to mark and deliver soon. It never blocks, so it is safe
// from a database hook.
func (d *Deliverer) Wake() {
	d.init()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Deliverer) init() {
	d.once.Do(func() { d.wake = make(chan struct{}, 1) })
}

// Round runs the sources, which mark what changed, then delivers what is
// due. Run calls it; tests call it to deliver at once.
func (d *Deliverer) Round(ctx context.Context) error {
	for _, src := range d.Sources {
		if err := src(ctx); err != nil {
			return err
		}
	}
	return d.Deliver(ctx)
}

// Prune deletes expired subscriptions and runs the cleanups.
func (d *Deliverer) Prune(ctx context.Context) error {
	n, err := d.Store.Prune()
	if err != nil {
		return err
	}
	if n > 0 {
		d.Log.Info("pruned expired push subscriptions", "count", n)
	}
	for _, c := range d.Cleanups {
		if err := c(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Run delivers until ctx ends: Debounce after a wake, and every minute for
// retries. Failures are logged; delivery carries on.
func (d *Deliverer) Run(ctx context.Context) {
	d.init()
	tick := time.NewTicker(sweep)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			select {
			case <-ctx.Done():
				return
			case <-time.After(Debounce):
			}
		case <-tick.C:
		}
		if err := d.Round(ctx); err != nil {
			d.Log.Error("push delivery", "err", err)
		}
		if time.Since(lastPrune) >= pruneEvery {
			lastPrune = time.Now()
			if err := d.Prune(ctx); err != nil {
				d.Log.Error("prune push subscriptions", "err", err)
			}
		}
	}
}
