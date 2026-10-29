package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var hookSeq atomic.Int64

// TxLockImmediate makes every transaction on a writable handle begin with
// BEGIN IMMEDIATE, taking the write lock up front. A deferred transaction
// that reads and then writes cannot wait for the lock: when another
// connection commits between its read and its write, WAL cannot upgrade the
// stale snapshot and SQLite fails it at once with "database is locked",
// whatever _busy_timeout says. Taking the lock at BEGIN makes it wait there
// instead. Read-only handles leave it out; they never write.
const TxLockImmediate = "_txlock=immediate"

// ConnectHook is a function called on every new SQLite connection.
type ConnectHook = func(*sqlite3.SQLiteConn) error

// OpenHooked opens a SQLite database with an update hook registered on every connection.
// The hook fires on INSERT, UPDATE, DELETE with the operation, database name, table name, and rowid.
// Each call registers a unique driver to avoid sql.Register panics.
func OpenHooked(path string, hook func(op int, db, table string, rowid int64)) (*sql.DB, error) {
	return OpenWithHooks(path, UpdateHook(hook))
}

// UpdateHook adapts a row-change callback to a ConnectHook, so it can be
// combined with others on one database.
func UpdateHook(hook func(op int, db, table string, rowid int64)) ConnectHook {
	return func(conn *sqlite3.SQLiteConn) error {
		conn.RegisterUpdateHook(hook)
		return nil
	}
}

// OpenWithHooks opens a SQLite database with one or more connect hooks.
// The path may include query parameters; _journal_mode and _foreign_keys
// are appended automatically.
func OpenWithHooks(path string, hooks ...ConnectHook) (*sql.DB, error) {
	return OpenWithHooksMode(path, "", hooks...)
}

// OpenWithHooksMode is OpenWithHooks with an explicit SQLite access mode,
// e.g. "ro". An empty mode leaves it unset.
func OpenWithHooksMode(path, mode string, hooks ...ConnectHook) (*sql.DB, error) {
	driverName := fmt.Sprintf("sqlite3_cog_%d", hookSeq.Add(1))
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			for _, h := range hooks {
				if err := h(conn); err != nil {
					return err
				}
			}
			return nil
		},
	})
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	dsn := path + sep + "_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000"
	if mode != "" {
		dsn += "&mode=" + mode
	} else {
		dsn += "&" + TxLockImmediate
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	TunePool(db, dsn)
	return db, nil
}

// ClockHook returns a ConnectHook that overrides SQLite's built-in unixepoch()
// with a custom function backed by nowFunc. This allows tests to control time
// for all SQL DEFAULT (unixepoch()) and UPDATE ... SET col = unixepoch() calls.
func ClockHook(nowFunc func() int64) ConnectHook {
	return func(conn *sqlite3.SQLiteConn) error {
		return conn.RegisterFunc("unixepoch", nowFunc, false)
	}
}

// TxObserver follows one connection's writes and learns whether the
// transaction that made them committed. Its methods run inside SQLite's
// hooks, on the writing goroutine: they must not use the database.
type TxObserver interface {
	// Change is a row written by the transaction in progress.
	Change(op int, db, table string, rowid int64)
	// Commit is called as the transaction commits. Act on what it changed
	// asynchronously: the commit is not durable until the hook returns.
	Commit()
	// Rollback discards what the transaction changed.
	Rollback()
}

// ObserveHook returns a ConnectHook that gives each connection the update
// hook (nil for none) and an observer of its own from newObserver (nil for
// none). SQLite keeps one update hook per connection, so everything that
// watches rows has to share it.
func ObserveHook(update func(op int, db, table string, rowid int64), newObserver func() TxObserver) ConnectHook {
	return func(conn *sqlite3.SQLiteConn) error {
		var obs TxObserver
		if newObserver != nil {
			obs = newObserver()
		}
		switch {
		case obs == nil && update == nil:
			return nil
		case obs == nil:
			conn.RegisterUpdateHook(update)
			return nil
		}
		conn.RegisterUpdateHook(func(op int, db, table string, rowid int64) {
			if update != nil {
				update(op, db, table, rowid)
			}
			obs.Change(op, db, table, rowid)
		})
		conn.RegisterCommitHook(func() int {
			obs.Commit()
			return 0
		})
		conn.RegisterRollbackHook(obs.Rollback)
		return nil
	}
}
