package db

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestIdleConnectionsAreReleased is the point of the pool settings: handles for
// per-user databases live until shutdown, so if their connections never go
// away, every user ever served costs a SQLite handle and file descriptor for
// the life of the process.
func TestIdleConnectionsAreReleased(t *testing.T) {
	old := IdleConnTimeout
	IdleConnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { IdleConnTimeout = old })

	path := filepath.Join(t.TempDir(), "plugin.db")
	sdb, err := OpenPluginDB(path, nil, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	if _, err := sdb.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := sdb.Stats().OpenConnections; got == 0 {
		t.Fatal("expected a live connection after a query")
	}

	// MaxIdleTimeClosed counts connections the pool retired for sitting idle,
	// which is the behaviour under test. OpenConnections is not, because it
	// also counts a connection handed out to a concurrent caller, and this
	// package's tests share a process.
	//
	// database/sql sweeps expired connections about once a second.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sdb.Stats().MaxIdleTimeClosed > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("no connection retired after idling %v, stats: %+v", IdleConnTimeout, sdb.Stats())
}

// TestMemoryDatabaseSurvivesIdle guards the trap in the setting above: a
// shared-cache in-memory database is destroyed when its last connection
// closes, so expiring it would take the data with it.
func TestMemoryDatabaseSurvivesIdle(t *testing.T) {
	old := IdleConnTimeout
	IdleConnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { IdleConnTimeout = old })

	sdb, err := OpenWithHooks("file:pooltest?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	if _, err := sdb.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("exec: %v", err)
	}

	// database/sql sweeps expired connections about once a second, so a short
	// sleep proves nothing: wait long enough that an unguarded pool drops the
	// last connection, which would take the database with it.
	waitForIdle(t, sdb, 3*time.Second)

	if _, err := sdb.Exec(`INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("in-memory database did not survive idling: %v", err)
	}
}

// waitForIdle waits until the pool reports no open connections, or until the
// deadline passes. It does not fail: callers use it to reach the state an
// expiring pool would produce, whether or not this pool expires anything.
func waitForIdle(t *testing.T, sdb *sql.DB, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if sdb.Stats().OpenConnections == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestHandleStaysUsableAfterIdle checks the trade: connections are recycled,
// but the handle a caller is holding keeps working. Closing handles out from
// under their holders is the reason this stops at connection level.
func TestHandleStaysUsableAfterIdle(t *testing.T) {
	old := IdleConnTimeout
	IdleConnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { IdleConnTimeout = old })

	path := filepath.Join(t.TempDir(), "plugin.db")
	sdb, err := OpenPluginDB(path, nil, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	if _, err := sdb.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("exec: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if _, err := sdb.Exec(`INSERT INTO t (id) VALUES (1)`); err != nil {
		t.Fatalf("handle unusable after idle: %v", err)
	}
}
