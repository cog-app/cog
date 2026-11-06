package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	chanBufSize  = 1024
	batchMaxSize = 100
	batchTimeout = 200 * time.Millisecond
)

// Collector accumulates database changes and flushes them to a search index
// in batches. It deduplicates changes within a batch window.
//
// Shutdown uses a quit channel and a stopped atomic flag rather than closing
// the input channel — closing would race with Send() and panic on producers
// (the SQLite update hook fires from any goroutine).
type Collector struct {
	log      *slog.Logger
	ch       chan Change
	index    *Index
	sources  map[string]SearchSource // name -> source
	tableMap map[string]SearchSource // table name -> source
	dbFunc   func() *sql.DB

	// OnIndexed, when set, learns of every batch written to the index: the
	// documents indexed and the IDs removed. A user's index tells the
	// propagator, so copies of what they shared follow their edits.
	OnIndexed func(docs []Document, deleted []string)

	quit    chan struct{}
	done    chan struct{}
	once    sync.Once
	stopped atomic.Bool
}

// NewCollector creates a new collector that writes to the given index.
// dbFunc returns the database to pass to SearchSource.Enrich.
func NewCollector(log *slog.Logger, index *Index, dbFunc func() *sql.DB) *Collector {
	return &Collector{
		log:      log,
		ch:       make(chan Change, chanBufSize),
		index:    index,
		sources:  make(map[string]SearchSource),
		tableMap: make(map[string]SearchSource),
		dbFunc:   dbFunc,
		quit:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// RegisterSource adds a search source to the collector.
func (c *Collector) RegisterSource(src SearchSource) {
	c.sources[src.Name()] = src
	for _, t := range src.Tables() {
		c.tableMap[t] = src
	}
}

// Send pushes a change to the collector. Non-blocking; drops on full channel
// or after Stop. Safe to call from the SQLite update hook on any goroutine.
func (c *Collector) Send(ch Change) {
	if c.stopped.Load() {
		return
	}
	select {
	case c.ch <- ch:
	case <-c.quit:
	default:
		// Channel full — drop the change. FullSync will catch up later.
	}
}

// Start begins the background goroutine that accumulates and flushes changes.
func (c *Collector) Start(ctx context.Context) {
	go c.run(ctx)
}

// Stop signals the collector to drain remaining changes and shut down.
func (c *Collector) Stop() {
	c.once.Do(func() {
		c.stopped.Store(true)
		close(c.quit)
	})
	<-c.done
}

func (c *Collector) run(ctx context.Context) {
	defer close(c.done)

	// pending maps source -> rowID -> last op
	pending := make(map[SearchSource]map[int64]Op)
	count := 0
	timer := time.NewTimer(batchTimeout)
	timer.Stop()

	flush := func() {
		if count == 0 {
			return
		}
		c.flush(pending)
		for k := range pending {
			delete(pending, k)
		}
		count = 0
		timer.Stop()
	}

	for {
		select {
		case ch := <-c.ch:
			src, ok := c.tableMap[ch.Table]
			if !ok {
				continue
			}
			m, ok := pending[src]
			if !ok {
				m = make(map[int64]Op)
				pending[src] = m
			}
			m[ch.RowID] = ch.Op
			count++
			if count >= batchMaxSize {
				flush()
			} else if count == 1 {
				timer.Reset(batchTimeout)
			}
		case <-timer.C:
			flush()
		case <-c.quit:
			// Drain any buffered changes before exiting.
			for {
				select {
				case ch := <-c.ch:
					src, ok := c.tableMap[ch.Table]
					if !ok {
						continue
					}
					m, ok := pending[src]
					if !ok {
						m = make(map[int64]Op)
						pending[src] = m
					}
					m[ch.RowID] = ch.Op
					count++
				default:
					flush()
					return
				}
			}
		case <-ctx.Done():
			flush()
			return
		}
	}
}

func (c *Collector) flush(pending map[SearchSource]map[int64]Op) {
	db := c.dbFunc()

	for src, rows := range pending {
		var enrichIDs []int64
		var deleteIDs []string

		for rowID, op := range rows {
			if op == OpDelete {
				deleteIDs = append(deleteIDs, fmt.Sprintf("%s:%d", src.Name(), rowID))
			} else {
				enrichIDs = append(enrichIDs, rowID)
			}
		}

		// Enrich inserts/updates.
		var docs []Document
		if len(enrichIDs) > 0 {
			var err error
			docs, err = src.Enrich(db, enrichIDs)
			if err != nil {
				c.log.Error("enrich failed", "source", src.Name(), "err", err)
				continue
			}

			// Any enrichIDs not returned by Enrich are orphaned — delete them.
			found := make(map[string]bool, len(docs))
			for _, d := range docs {
				found[d.ID] = true
			}
			for _, rowID := range enrichIDs {
				id := fmt.Sprintf("%s:%d", src.Name(), rowID)
				if !found[id] {
					deleteIDs = append(deleteIDs, id)
				}
			}
		}

		if len(docs) == 0 && len(deleteIDs) == 0 {
			continue
		}
		if err := c.index.Batch(docs, deleteIDs); err != nil {
			c.log.Error("batch failed", "source", src.Name(), "err", err)
			continue
		}
		c.indexed(docs, deleteIDs)
	}
}

// FullSync enumerates all rows from all sources and indexes them.
// Used for initial bootstrap or re-indexing.
func (c *Collector) FullSync() error {
	db := c.dbFunc()

	for _, src := range c.sources {
		// Sources that manage their own DB implement SelfSyncer.
		if ss, ok := src.(SelfSyncer); ok {
			docs, err := ss.SelfSync()
			if err != nil {
				c.log.Error("full sync self-sync failed", "source", src.Name(), "err", err)
				continue
			}
			if len(docs) > 0 {
				if err := c.index.Batch(docs, nil); err != nil {
					c.log.Error("full sync batch failed", "source", src.Name(), "err", err)
				} else {
					c.indexed(docs, nil)
				}
			}
			continue
		}

		if db == nil {
			c.log.Warn("full sync skip: no database", "source", src.Name())
			continue
		}

		// Query all rowIDs from each table.
		for _, table := range src.Tables() {
			rowIDs, err := tableRowIDs(db, table)
			if err != nil {
				c.log.Error("full sync query failed", "table", table, "err", err)
				continue
			}

			if len(rowIDs) == 0 {
				continue
			}

			// Enrich in batches.
			for i := 0; i < len(rowIDs); i += batchMaxSize {
				end := min(i+batchMaxSize, len(rowIDs))
				docs, err := src.Enrich(db, rowIDs[i:end])
				if err != nil {
					c.log.Error("full sync enrich failed", "source", src.Name(), "err", err)
					continue
				}
				if err := c.index.Batch(docs, nil); err != nil {
					c.log.Error("full sync batch failed", "source", src.Name(), "err", err)
				} else {
					c.indexed(docs, nil)
				}
			}
		}
	}
	return nil
}

func (c *Collector) indexed(docs []Document, deleted []string) {
	if c.OnIndexed != nil && (len(docs) > 0 || len(deleted) > 0) {
		c.OnIndexed(docs, deleted)
	}
}

// tableRowIDs returns every rowid in table.
func tableRowIDs(db *sql.DB, table string) ([]int64, error) {
	rows, err := db.Query(fmt.Sprintf("SELECT rowid FROM %s", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rowIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan rowid of %s: %w", table, err)
		}
		rowIDs = append(rowIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list rowids of %s: %w", table, err)
	}
	return rowIDs, nil
}
