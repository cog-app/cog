package db

import (
	"database/sql"
	"fmt"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

var hookSeq atomic.Int64

// OpenHooked opens a SQLite database with an update hook registered on every connection.
// The hook fires on INSERT, UPDATE, DELETE with the operation, database name, table name, and rowid.
// Each call registers a unique driver to avoid sql.Register panics.
func OpenHooked(path string, hook func(op int, db, table string, rowid int64)) (*sql.DB, error) {
	driverName := fmt.Sprintf("sqlite3_cog_%d", hookSeq.Add(1))
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			conn.RegisterUpdateHook(hook)
			return nil
		},
	})
	db, err := sql.Open(driverName, path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	return db, nil
}
