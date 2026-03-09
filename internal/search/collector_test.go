package search

import (
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// testSource is a mock SearchSource for testing.
type testSource struct {
	name   string
	tables []string
	db     *sql.DB
}

func (s *testSource) Name() string     { return s.name }
func (s *testSource) Tables() []string { return s.tables }

func (s *testSource) Enrich(db *sql.DB, rowIDs []int64) ([]Document, error) {
	var docs []Document
	for _, id := range rowIDs {
		var name string
		err := db.QueryRow("SELECT name FROM items WHERE rowid = ?", id).Scan(&name)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, Document{
			ID: fmt.Sprintf("%s:%d", s.name, id),
			Fields: map[string]any{
				"name":   name,
				"source": s.name,
			},
		})
	}
	return docs, nil
}

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.Exec("CREATE TABLE items (name TEXT NOT NULL)")
	return db
}

func TestCollectorBatchFlush(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	db := setupTestDB(t)
	db.Exec("INSERT INTO items (name) VALUES ('alpha')")
	db.Exec("INSERT INTO items (name) VALUES ('beta')")

	ctx := t.Context()

	c := NewCollector(slog.New(slog.NewTextHandler(t.Output(), nil)), idx, func() *sql.DB { return db })
	c.RegisterSource(&testSource{name: "test", tables: []string{"items"}})
	c.Start(ctx)

	c.Send(Change{Op: OpInsert, Table: "items", RowID: 1})
	c.Send(Change{Op: OpInsert, Table: "items", RowID: 2})

	// Wait for flush.
	time.Sleep(300 * time.Millisecond)

	result, err := idx.Search("alpha", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 hit for 'alpha', got %d", result.Total)
	}

	result, err = idx.Search("beta", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 hit for 'beta', got %d", result.Total)
	}

	// Test dedup: send update for same row, should still work.
	db.Exec("UPDATE items SET name = 'alpha-updated' WHERE rowid = 1")
	c.Send(Change{Op: OpUpdate, Table: "items", RowID: 1})
	time.Sleep(300 * time.Millisecond)

	result, err = idx.Search("alpha-updated", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 hit for 'alpha-updated', got %d", result.Total)
	}

	// Test delete.
	c.Send(Change{Op: OpDelete, Table: "items", RowID: 2})
	time.Sleep(300 * time.Millisecond)

	result, err = idx.Search("beta", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 {
		t.Fatalf("expected 0 hits after delete, got %d", result.Total)
	}

	c.Stop()
}

func TestCollectorFullSync(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	db := setupTestDB(t)
	db.Exec("INSERT INTO items (name) VALUES ('one')")
	db.Exec("INSERT INTO items (name) VALUES ('two')")
	db.Exec("INSERT INTO items (name) VALUES ('three')")

	c := NewCollector(slog.New(slog.NewTextHandler(t.Output(), nil)), idx, func() *sql.DB { return db })
	c.RegisterSource(&testSource{name: "test", tables: []string{"items"}})

	if err := c.FullSync(); err != nil {
		t.Fatal(err)
	}

	result, err := idx.Search("two", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("expected 1 hit for 'two', got %d", result.Total)
	}
}

func TestCollectorIgnoresUnknownTables(t *testing.T) {
	idx, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	db := setupTestDB(t)

	ctx := t.Context()

	c := NewCollector(slog.New(slog.NewTextHandler(t.Output(), nil)), idx, func() *sql.DB { return db })
	c.RegisterSource(&testSource{name: "test", tables: []string{"items"}})
	c.Start(ctx)

	// Unknown table should be silently dropped.
	c.Send(Change{Op: OpInsert, Table: "unknown_table", RowID: 1})
	time.Sleep(300 * time.Millisecond)

	c.Stop()
}
