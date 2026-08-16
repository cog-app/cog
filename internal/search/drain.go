package search

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/db"
)

const (
	// drainBatch is how many queued changes are claimed per statement.
	drainBatch = 500
	// drainDebounce is how long a nudge waits before draining. A SQLite
	// update hook fires inside the writing transaction, so an immediate drain
	// would look for a row that is not committed yet.
	drainDebounce = 100 * time.Millisecond
)

// DrainInterval is the unprompted sweep. Nudges are an optimisation, not the
// contract: a change sits in the queue until someone drains it, so a missed
// nudge costs latency rather than an unindexed row. It is also all there is
// when the writer is another process, or a backend with no hooks to nudge
// from. Exported so tests do not have to wait for it.
var DrainInterval = 15 * time.Second

// dirtyKey identifies a database with changes waiting. The zero value means
// the system database, which has no user or plugin.
type dirtyKey struct {
	username string
	pluginID string
}

func (k dirtyKey) isSystem() bool { return k.username == "" && k.pluginID == "" }

// drainer moves queued row changes into the collectors that index them.
//
// Capture and indexing are deliberately separate: writes record what changed
// inside their own transaction, and this decides when to act on it. That is
// what lets a change survive a crash, and what lets a process index writes it
// did not make.
type drainer struct {
	m *Manager

	mu    sync.Mutex
	dirty map[dirtyKey]struct{}
	wake  chan struct{}
	done  chan struct{}
}

func newDrainer(m *Manager) *drainer {
	return &drainer{
		m:     m,
		dirty: make(map[dirtyKey]struct{}),
		wake:  make(chan struct{}, 1),
		done:  make(chan struct{}),
	}
}

// nudge marks a database as worth draining soon. Safe to call from a SQLite
// update hook: it takes a mutex and a non-blocking send, and never touches the
// database it was called about.
func (d *drainer) nudge(username, pluginID string) {
	d.mu.Lock()
	d.dirty[dirtyKey{username, pluginID}] = struct{}{}
	d.mu.Unlock()

	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// nudgeSystem marks the system database as worth draining soon.
func (d *drainer) nudgeSystem() {
	d.nudge("", "")
}

func (d *drainer) run(ctx context.Context) {
	defer close(d.done)

	ticker := time.NewTicker(DrainInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.drainAll(ctx, d.known())
		case <-d.wake:
			select {
			case <-time.After(drainDebounce):
			case <-ctx.Done():
				return
			}
			d.drainAll(ctx, d.take())
		}
	}
}

// wait blocks until the drain loop has exited. Callers stop it by cancelling
// the context it was started with, then wait here before closing anything the
// drain queries — a drain in flight holds a database handle and a collector.
func (d *drainer) wait() {
	<-d.done
}

// take returns the databases nudged since the last drain and clears the set.
func (d *drainer) take() []dirtyKey {
	d.mu.Lock()
	defer d.mu.Unlock()

	keys := make([]dirtyKey, 0, len(d.dirty))
	for k := range d.dirty {
		keys = append(keys, k)
	}
	d.dirty = make(map[dirtyKey]struct{})

	return keys
}

// known returns every database that could hold changes: each open user index
// crossed with each plugin that registered a search source. The periodic sweep
// uses this so a write made by another process is still picked up.
func (d *drainer) known() []dirtyKey {
	d.m.mu.Lock()
	defer d.m.mu.Unlock()

	keys := []dirtyKey{{}} // the system database
	for username := range d.m.userIndices {
		for pluginID := range d.m.pluginSources {
			keys = append(keys, dirtyKey{username, pluginID})
		}
	}

	return keys
}

func (d *drainer) drainAll(ctx context.Context, keys []dirtyKey) {
	for _, k := range keys {
		if ctx.Err() != nil {
			return
		}
		if err := d.drainOne(ctx, k); err != nil {
			d.m.log.Error("drain changes", "user", k.username, "plugin", k.pluginID, "err", err)
		}
	}
}

func (d *drainer) drainOne(ctx context.Context, k dirtyKey) error {
	var (
		sdb       *sql.DB
		collector *Collector
	)

	if k.isSystem() {
		sdb, collector = d.m.sysDB, d.m.systemCollector
		if sdb == nil || collector == nil {
			return nil
		}
	} else {
		if d.m.userDBFunc == nil {
			return nil
		}

		d.m.mu.Lock()
		us, open := d.m.userIndices[k.username]
		d.m.mu.Unlock()
		if !open {
			// Nothing to feed. Changes stay queued until the index is opened,
			// whose full sync covers them anyway.
			return nil
		}
		collector = us.collector

		var err error
		sdb, err = d.m.userDBFunc(k.username, k.pluginID)
		if err != nil {
			return err
		}
	}

	for {
		changes, err := db.DrainChanges(ctx, sdb, drainBatch)
		if err != nil {
			return err
		}
		for _, c := range changes {
			collector.Send(Change{
				Op:    changeOpToOp(c.Op),
				Table: c.Table,
				RowID: c.RowID,
			})
		}
		if len(changes) < drainBatch {
			return nil
		}
	}
}

// changeOpToOp converts a queued operation to the collector's.
func changeOpToOp(op int) Op {
	switch op {
	case db.ChangeInsert:
		return OpInsert
	case db.ChangeUpdate:
		return OpUpdate
	default:
		return OpDelete
	}
}
