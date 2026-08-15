package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
)

func openWithTable(t *testing.T) *sql.DB {
	t.Helper()
	sdb, err := OpenPluginDB(filepath.Join(t.TempDir(), "plugin.db"), nil, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sdb.Close() })

	if _, err := sdb.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := EnsureChangeCapture(sdb, []string{"items"}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	return sdb
}

func TestChangeCaptureRecordsEachOperation(t *testing.T) {
	sdb := openWithTable(t)

	if _, err := sdb.Exec(`INSERT INTO items (title) VALUES ('one')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := sdb.Exec(`UPDATE items SET title = 'two' WHERE id = 1`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := sdb.Exec(`DELETE FROM items WHERE id = 1`); err != nil {
		t.Fatalf("delete: %v", err)
	}

	changes, err := DrainChanges(context.Background(), sdb, 100)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes, got %d: %+v", len(changes), changes)
	}

	want := []int{ChangeInsert, ChangeUpdate, ChangeDelete}
	for i, c := range changes {
		if c.Op != want[i] {
			t.Errorf("change %d: op = %d, want %d", i, c.Op, want[i])
		}
		if c.Table != "items" || c.RowID != 1 {
			t.Errorf("change %d: got %+v", i, c)
		}
	}
}

// TestChangeCaptureIsTransactional is the property update hooks do not have:
// a change that never committed must not be queued.
func TestChangeCaptureIsTransactional(t *testing.T) {
	sdb := openWithTable(t)

	tx, err := sdb.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO items (title) VALUES ('rolled back')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	changes, err := DrainChanges(context.Background(), sdb, 100)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("rolled-back write was queued: %+v", changes)
	}
}

func TestDrainIsExactlyOnce(t *testing.T) {
	sdb := openWithTable(t)

	for i := range 200 {
		if _, err := sdb.Exec(`INSERT INTO items (title) VALUES (?)`, i); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Several drainers at once stand in for several replicas draining the
	// same database: each change must go to exactly one of them.
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[int64]int{}
	)
	for range 4 {
		wg.Go(func() {
			for {
				changes, err := DrainChanges(context.Background(), sdb, 10)
				if err != nil {
					t.Errorf("drain: %v", err)
					return
				}
				if len(changes) == 0 {
					return
				}
				mu.Lock()
				for _, c := range changes {
					seen[c.RowID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if len(seen) != 200 {
		t.Errorf("saw %d distinct rows, want 200", len(seen))
	}
	for rowID, n := range seen {
		if n != 1 {
			t.Errorf("row %d drained %d times, want once", rowID, n)
		}
	}
}

func TestDrainWithoutCaptureIsNotAnError(t *testing.T) {
	sdb, err := OpenPluginDB(filepath.Join(t.TempDir(), "plugin.db"), nil, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer sdb.Close()

	changes, err := DrainChanges(context.Background(), sdb, 10)
	if err != nil {
		t.Fatalf("drain on a database with no capture: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestEnsureChangeCaptureIsIdempotent(t *testing.T) {
	sdb := openWithTable(t)

	if err := EnsureChangeCapture(sdb, []string{"items"}); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if _, err := sdb.Exec(`INSERT INTO items (title) VALUES ('one')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	changes, err := DrainChanges(context.Background(), sdb, 100)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("re-running ensure duplicated triggers: got %d changes", len(changes))
	}
}

func TestEnsureChangeCaptureRejectsOddTableNames(t *testing.T) {
	sdb := openWithTable(t)

	if err := EnsureChangeCapture(sdb, []string{"items; DROP TABLE items"}); err == nil {
		t.Fatal("expected an error for a table name that is not an identifier")
	}
}
