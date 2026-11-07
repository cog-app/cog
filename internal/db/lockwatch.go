package db

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"weak"
)

// SQLite keeps its locks as POSIX advisory locks, and on Linux closing any
// descriptor of a file drops every one of them the process holds on it. So
// a file opened and closed outside SQLite — a glob over the data directory,
// a backup that reads it, a checksum — silently leaves the process
// unlocked. Another process then takes itself for the last connection,
// checkpoints, and deletes the -wal and -shm files this one still has open,
// and its queries fail with "disk I/O error: no such file or directory".
// The cache sweep did exactly that to the system database.
//
// Nothing can stop a careless open, so WatchLocks checks that the process
// still holds a lock on every database it has connections to, and says
// loudly when it does not.

// LockCheckEvery is how often WatchLocks checks.
var LockCheckEvery = time.Minute

// errLockCheckUnsupported is returned where the held locks cannot be read.
var errLockCheckUnsupported = errors.New("lock check unsupported on this system")

type watched struct {
	path string
	db   weak.Pointer[sql.DB]
}

var registry struct {
	mu  sync.Mutex
	dbs []watched
}

// watch registers the database at dsn for WatchLocks. An in-memory one has
// no file to lock.
func watch(sdb *sql.DB, dsn string) {
	if isMemoryDSN(dsn) {
		return
	}
	path := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.dbs = append(registry.dbs, watched{path: path, db: weak.Make(sdb)})
}

// connected returns the paths of the registered databases that have open
// connections, which hold a lock while they are open, and forgets those
// collected since.
func connected() []string {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	var paths []string
	live := registry.dbs[:0]
	for _, w := range registry.dbs {
		sdb := w.db.Value()
		if sdb == nil {
			continue
		}
		live = append(live, w)
		if sdb.Stats().OpenConnections > 0 {
			paths = append(paths, w.path)
		}
	}
	clear(registry.dbs[len(live):])
	registry.dbs = live
	return paths
}

// unlocked returns the databases with open connections that the process
// holds no lock on. A database whose connections close between the two
// looks unlocked for a moment, so one is reported only if it still has
// connections, and still no lock, a second later.
func unlocked() ([]string, error) {
	suspect, err := missingLocks(connected())
	if err != nil || len(suspect) == 0 {
		return nil, err
	}
	time.Sleep(time.Second)
	still := map[string]bool{}
	for _, p := range connected() {
		still[p] = true
	}
	var again []string
	for _, p := range suspect {
		if still[p] {
			again = append(again, p)
		}
	}
	return missingLocks(again)
}

// WatchLocks checks every LockCheckEvery, until ctx ends, that the process
// holds a lock on each database it has connections to, and logs an error
// for each it does not. A system whose held locks cannot be read is said so
// once.
func WatchLocks(ctx context.Context, log *slog.Logger) {
	t := time.NewTicker(LockCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		paths, err := unlocked()
		if errors.Is(err, errLockCheckUnsupported) {
			log.Info("database lock check off", "reason", err)
			return
		}
		if err != nil {
			log.Error("database lock check", "err", err)
			continue
		}
		for _, p := range paths {
			log.Error("BUG: database lock lost: something in this process opened and closed the file outside SQLite. "+
				"Another process may delete its -wal and -shm files, and queries will then fail with disk I/O errors. "+
				"Restart to recover, and find the code that opens it.", "db", p)
		}
	}
}
