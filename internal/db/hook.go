package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var hookSeq atomic.Int64

// ConnectHook is a function called on every new SQLite connection.
type ConnectHook = func(*sqlite3.SQLiteConn) error

// OpenHooked opens a SQLite database with an update hook registered on every connection.
// The hook fires on INSERT, UPDATE, DELETE with the operation, database name, table name, and rowid.
// Each call registers a unique driver to avoid sql.Register panics.
func OpenHooked(path string, hook func(op int, db, table string, rowid int64)) (*sql.DB, error) {
	return OpenWithHooks(path, func(conn *sqlite3.SQLiteConn) error {
		conn.RegisterUpdateHook(hook)
		return nil
	})
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
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
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
