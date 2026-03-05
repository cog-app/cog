package search

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	chanBufSize  = 1024
	batchMaxSize = 100
	batchTimeout = 200 * time.Millisecond
)

// Collector accumulates database changes and flushes them to a search index
// in batches. It deduplicates changes within a batch window.
type Collector struct {
	ch       chan Change
	index    *Index
	sources  map[string]SearchSource   // name -> source
	tableMap map[string]SearchSource   // table name -> source
	dbFunc   func() *sql.DB

	done chan struct{}
	once sync.Once
}

// NewCollector creates a new collector that writes to the given index.
// dbFunc returns the database to pass to SearchSource.Enrich.
func NewCollector(index *Index, dbFunc func() *sql.DB) *Collector {
	return &Collector{
		ch:       make(chan Change, chanBufSize),
		index:    index,
		sources:  make(map[string]SearchSource),
		tableMap: make(map[string]SearchSource),
		dbFunc:   dbFunc,
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

// Send pushes a change to the collector. Non-blocking; drops on full channel.
func (c *Collector) Send(ch Change) {
	select {
	case c.ch <- ch:
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
	c.once.Do(func() { close(c.ch) })
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
		case ch, ok := <-c.ch:
			if !ok {
				flush()
				return
			}
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
				log.Printf("search: enrich %s: %v", src.Name(), err)
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
			log.Printf("search: batch %s: %v", src.Name(), err)
		}
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
				log.Printf("search: full sync self-sync %s: %v", src.Name(), err)
				continue
			}
			if len(docs) > 0 {
				if err := c.index.Batch(docs, nil); err != nil {
					log.Printf("search: full sync batch %s: %v", src.Name(), err)
				}
			}
			continue
		}

		if db == nil {
			log.Printf("search: full sync skip %s: no database", src.Name())
			continue
		}

		// Query all rowIDs from each table.
		for _, table := range src.Tables() {
			rows, err := db.Query(fmt.Sprintf("SELECT rowid FROM %s", table))
			if err != nil {
				log.Printf("search: full sync query %s: %v", table, err)
				continue
			}

			var rowIDs []int64
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					continue
				}
				rowIDs = append(rowIDs, id)
			}
			rows.Close()

			if len(rowIDs) == 0 {
				continue
			}

			// Enrich in batches.
			for i := 0; i < len(rowIDs); i += batchMaxSize {
				end := min(i+batchMaxSize, len(rowIDs))
				docs, err := src.Enrich(db, rowIDs[i:end])
				if err != nil {
					log.Printf("search: full sync enrich %s: %v", src.Name(), err)
					continue
				}
				if err := c.index.Batch(docs, nil); err != nil {
					log.Printf("search: full sync batch %s: %v", src.Name(), err)
				}
			}
		}
	}
	return nil
}
