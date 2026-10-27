package davsql_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	_ "github.com/mattn/go-sqlite3"
)

func openDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "t.db")+"?_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(schema)
	require.NoError(t, err)
	return db
}

func tx(t *testing.T, db *sql.DB, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := db.Begin()
	require.NoError(t, err)
	fn(tx)
	require.NoError(t, tx.Commit())
}

func TestVersionsAndTombstones(t *testing.T) {
	db := openDB(t, davsql.Schema)
	var v1, v2, v3 int64
	tx(t, db, func(tx *sql.Tx) {
		var err error
		v1, err = davsql.Bump(tx, 7, 100)
		require.NoError(t, err)
		v2, err = davsql.Bump(tx, 7, 100)
		require.NoError(t, err)
	})
	assert.Equal(t, int64(1), v1)
	assert.Equal(t, int64(2), v2)

	// Collections count separately.
	tx(t, db, func(tx *sql.Tx) {
		v, err := davsql.Bump(tx, 8, 100)
		require.NoError(t, err)
		assert.Equal(t, int64(1), v)
	})

	tx(t, db, func(tx *sql.Tx) {
		var err error
		v3, err = davsql.Delete(tx, 7, "a.ics", 100)
		require.NoError(t, err)
	})
	assert.Equal(t, int64(3), v3)

	deleted, err := davsql.Deleted(db, 7, 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.ics"}, deleted)
	deleted, err = davsql.Deleted(db, 7, 3)
	require.NoError(t, err)
	assert.Equal(t, 0, len(deleted))

	// A PUT that recreates the URI forgets the tombstone.
	tx(t, db, func(tx *sql.Tx) { require.NoError(t, davsql.Revive(tx, 7, "a.ics")) })
	deleted, err = davsql.Deleted(db, 7, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, len(deleted))

	states, err := davsql.States(db, []int64{7, 8, 9})
	require.NoError(t, err)
	assert.Equal(t, davsql.State{Token: 3, Modified: 100}, states[7])
	assert.Equal(t, davsql.State{Token: 1, Modified: 100}, states[8])
	_, ok := states[9]
	assert.False(t, ok)
}

func TestPruneRaisesFloor(t *testing.T) {
	db := openDB(t, davsql.Schema)
	tx(t, db, func(tx *sql.Tx) {
		_, err := davsql.Delete(tx, 1, "old.ics", 100)
		require.NoError(t, err)
		_, err = davsql.Delete(tx, 1, "new.ics", 500)
		require.NoError(t, err)
	})
	require.NoError(t, davsql.Prune(db, 1, 300))
	st, err := davsql.GetState(db, 1)
	require.NoError(t, err)
	assert.Equal(t, davsql.State{Token: 2, Floor: 1, Modified: 500}, st)
	deleted, err := davsql.Deleted(db, 1, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"new.ics"}, deleted)

	// Nothing to prune is a no-op.
	require.NoError(t, davsql.Prune(db, 1, 300))

	tx(t, db, func(tx *sql.Tx) { require.NoError(t, davsql.Purge(tx, 1)) })
	st, err = davsql.GetState(db, 1)
	require.NoError(t, err)
	assert.Equal(t, davsql.State{}, st)
}

func TestConditions(t *testing.T) {
	v := int64(4)
	ok := func(c davsql.Conditions, cur *int64) bool { return c.Check(cur) == nil }
	assert.True(t, ok(davsql.Conditions{}, nil))
	assert.True(t, ok(davsql.Conditions{}, &v))
	assert.True(t, ok(davsql.Conditions{IfNoneMatchAny: true}, nil))
	assert.False(t, ok(davsql.Conditions{IfNoneMatchAny: true}, &v))
	assert.True(t, ok(davsql.Conditions{IfMatchAny: true}, &v))
	assert.False(t, ok(davsql.Conditions{IfMatchAny: true}, nil))
	assert.True(t, ok(davsql.Conditions{IfMatch: []int64{3, 4}}, &v))
	assert.False(t, ok(davsql.Conditions{IfMatch: []int64{3}}, &v))
	assert.False(t, ok(davsql.Conditions{IfMatch: []int64{4}}, nil))
	assert.False(t, ok(davsql.Conditions{IfMatchNone: true}, &v))
}

// TestPluginSchemasMatch checks that every plugin database holding DAV
// resources declares the sync tables exactly as Schema does: the SQL here
// runs against them.
func TestPluginSchemasMatch(t *testing.T) {
	describe := func(db *sql.DB) string {
		var b strings.Builder
		for _, table := range []string{"dav_sync", "dav_tombstones"} {
			rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
			require.NoError(t, err)
			for rows.Next() {
				var cid, notnull, pk int
				var name, typ string
				var dflt sql.NullString
				require.NoError(t, rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk))
				fmt.Fprintf(&b, "%s.%s %s %d %v %d\n", table, name, typ, notnull, dflt, pk)
			}
			require.NoError(t, rows.Err())
			rows.Close()
		}
		var idx []string
		rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name LIKE 'dav_%' AND sql IS NOT NULL`)
		require.NoError(t, err)
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			idx = append(idx, s)
		}
		require.NoError(t, rows.Err())
		rows.Close()
		slices.Sort(idx)
		return b.String() + strings.Join(idx, "\n")
	}
	want := describe(openDB(t, davsql.Schema))
	for _, p := range []string{"calendar", "tasks", "contacts"} {
		schema, err := os.ReadFile(filepath.Join("..", "..", "..", "plugin", p, "sql", "schema.sql"))
		require.NoError(t, err)
		assert.Equal(t, want, describe(openDB(t, string(schema))), p)
	}
}
