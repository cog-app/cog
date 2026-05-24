package db_test

import (
	"path/filepath"
	"sync"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// SQLite update-hook opcodes (sqlite3.h).
const (
	opInsert = 18
	opUpdate = 23
	opDelete = 9
)

// hookCapture is a thread-safe sink for hook calls.
type hookCapture struct {
	mu    sync.Mutex
	calls []hookCall
}

type hookCall struct {
	op    int
	table string
	rowid int64
}

func (h *hookCapture) fn() func(op int, dbName, table string, rowid int64) {
	return func(op int, _ string, table string, rowid int64) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls = append(h.calls, hookCall{op: op, table: table, rowid: rowid})
	}
}

func (h *hookCapture) snapshot() []hookCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]hookCall, len(h.calls))
	copy(out, h.calls)
	return out
}

// --- OpenSystem ---

func TestOpenSystem_CreatesSchema(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "system.db")
	d, err := db.OpenSystem(tmp)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	// Schema should include the users table (per internal/db/sql/schema.sql).
	var n int
	require.NoError(t, d.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n))
	require.Equal(t, 0, n)
}

func TestOpenSystem_IsIdempotent(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "system.db")
	d, err := db.OpenSystem(tmp)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	// Re-open the same file.
	d2, err := db.OpenSystem(tmp)
	require.NoError(t, err)
	t.Cleanup(func() { d2.Close() })

	require.NoError(t, d2.QueryRow(`SELECT COUNT(*) FROM users`).Scan(new(int)))
}

func TestOpenSystem_BadPath(t *testing.T) {
	// Path under a non-existent directory.
	_, err := db.OpenSystem(filepath.Join(t.TempDir(), "no", "such", "dir", "system.db"))
	require.True(t, err != nil)
}

// --- OpenHooked ---

func TestOpenHooked_HookFiresOnInsertUpdateDelete(t *testing.T) {
	cap := &hookCapture{}
	d, err := db.OpenHooked(":memory:", cap.fn())
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	_, err = d.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, val TEXT)`)
	require.NoError(t, err)

	_, err = d.Exec(`INSERT INTO t (val) VALUES (?)`, "a")
	require.NoError(t, err)

	_, err = d.Exec(`UPDATE t SET val = ? WHERE id = 1`, "b")
	require.NoError(t, err)

	_, err = d.Exec(`DELETE FROM t WHERE id = 1`)
	require.NoError(t, err)

	calls := cap.snapshot()
	require.Equal(t, 3, len(calls))
	require.Equal(t, opInsert, calls[0].op)
	require.Equal(t, "t", calls[0].table)
	require.Equal(t, opUpdate, calls[1].op)
	require.Equal(t, opDelete, calls[2].op)
}

// --- ClockHook (functional check via SQL) ---

func TestClockHook_OverridesUnixepoch(t *testing.T) {
	called := false
	d, err := db.OpenWithHooks(":memory:", db.ClockHook(func() int64 {
		called = true
		return 42
	}))
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	var got int64
	require.NoError(t, d.QueryRow(`SELECT unixepoch()`).Scan(&got))
	require.Equal(t, int64(42), got)
	require.True(t, called)
}

// --- Migrate ---

func TestMigrate_RunsSystemSchema(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "fresh.db")
	d, err := db.OpenSystem(tmp)
	require.NoError(t, err)
	t.Cleanup(func() { d.Close() })

	// Re-Migrate is idempotent.
	require.NoError(t, db.Migrate(d))
}
