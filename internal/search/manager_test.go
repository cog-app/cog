package search

import (
	"database/sql"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestManagerSystemSearch(t *testing.T) {
	// Create an in-memory system DB with users.
	sysDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sysDB.Close()

	sysDB.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, display_name TEXT)`)
	sysDB.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT)`)
	sysDB.Exec(`CREATE TABLE volumes (id TEXT PRIMARY KEY, name TEXT, description TEXT)`)

	sysDB.Exec(`INSERT INTO users (id, username, display_name) VALUES ('u1', 'admin', 'Administrator')`)
	sysDB.Exec(`INSERT INTO users (id, username, display_name) VALUES ('u2', 'alice', 'Alice Smith')`)
	sysDB.Exec(`INSERT INTO groups (id, name) VALUES ('g1', 'developers')`)

	dataDir := t.TempDir()
	mgr := NewManager(slog.New(slog.NewTextHandler(t.Output(), nil)), dataDir)
	mgr.SetSystemDB(sysDB)

	ctx := t.Context()

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer mgr.Stop()

	// Search for admin.
	result, err := mgr.SearchSystem("admin", 10, 0)
	if err != nil {
		t.Fatalf("search system: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected at least one hit for 'admin'")
	}

	// Search for developers group.
	result, err = mgr.SearchSystem("developers", 10, 0)
	if err != nil {
		t.Fatalf("search system: %v", err)
	}
	if result.Total == 0 {
		t.Fatal("expected at least one hit for 'developers'")
	}
}

func TestManagerSystemSearchLiveUpdates(t *testing.T) {
	sysDB, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer sysDB.Close()

	sysDB.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT, display_name TEXT)`)
	sysDB.Exec(`CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT)`)
	sysDB.Exec(`CREATE TABLE volumes (id TEXT PRIMARY KEY, name TEXT, description TEXT)`)

	dataDir := t.TempDir()
	mgr := NewManager(slog.New(slog.NewTextHandler(t.Output(), nil)), dataDir)
	mgr.SetSystemDB(sysDB)

	ctx := t.Context()

	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Simulate an update hook firing after inserting a user.
	sysDB.Exec(`INSERT INTO users (id, username, display_name) VALUES ('u1', 'newuser', 'New User')`)
	// Manually feed the change (since we're using a plain DB, no hook).
	mgr.systemCollector.Send(Change{Op: OpInsert, Table: "users", RowID: 1})

	time.Sleep(300 * time.Millisecond)

	result, err := mgr.SearchSystem("newuser", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total == 0 {
		t.Fatal("expected hit for 'newuser' after live update")
	}
}
