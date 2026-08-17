package db

import (
	"database/sql"
	"strings"
	"time"
)

// IdleConnTimeout is how long an unused connection is kept before it is
// closed. Exported so tests can shorten it.
//
// This is the bound that matters for a host with many users: a *sql.DB is a
// small struct, but the connections behind it are SQLite handles, file
// descriptors, and page caches. One database per user per plugin means those
// accumulate for every user who has ever been served, and nothing releases
// them because the handles live until shutdown.
var IdleConnTimeout = 2 * time.Minute

// MaxIdleConns caps the connections kept ready per database. Two is the
// database/sql default and is right for per-user databases, which see bursts
// of a few concurrent queries and then nothing.
const MaxIdleConns = 2

// TunePool applies the pool settings every Cog database uses.
//
// An in-memory database gets no idle timeout. A shared-cache in-memory
// database exists only as long as something is connected to it, so expiring
// the last connection does not free a resource — it deletes the database.
//
// Note there is deliberately no SetMaxOpenConns: capping a SQLite database at
// one connection deadlocks any code that reads while a write transaction is
// open on another connection.
func TunePool(sdb *sql.DB, dsn string) {
	sdb.SetMaxIdleConns(MaxIdleConns)
	if isMemoryDSN(dsn) {
		return
	}
	sdb.SetConnMaxIdleTime(IdleConnTimeout)
}

func isMemoryDSN(dsn string) bool {
	return strings.Contains(dsn, "mode=memory") || strings.Contains(dsn, ":memory:")
}
