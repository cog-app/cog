package plugin

import (
	"database/sql"
	"testing"
	"testing/fstest"

	_ "github.com/mattn/go-sqlite3"
)

func TestMigrate(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS items (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL
			);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN items.color NOT EXIST
			ALTER TABLE items ADD COLUMN color TEXT NOT NULL DEFAULT '';
		`)},
		"sql/migration002.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS tags (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				label TEXT NOT NULL
			);
		`)},
	}

	if err := Migrate(db, fsys); err != nil {
		t.Fatal(err)
	}

	// Verify schema has the new column and table.
	var color string
	db.Exec(`INSERT INTO items (name, color) VALUES ('test', 'red')`)
	if err := db.QueryRow(`SELECT color FROM items WHERE name = 'test'`).Scan(&color); err != nil {
		t.Fatal(err)
	}
	if color != "red" {
		t.Fatalf("got color %q, want red", color)
	}

	db.Exec(`INSERT INTO tags (label) VALUES ('foo')`)
	var label string
	if err := db.QueryRow(`SELECT label FROM tags WHERE id = 1`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "foo" {
		t.Fatalf("got label %q, want foo", label)
	}

	// Run again — should be idempotent.
	if err := Migrate(db, fsys); err != nil {
		t.Fatal("second migrate:", err)
	}
}

func TestMigrateSkipsWhenColumnExists(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Schema already has the column.
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS items (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				color TEXT NOT NULL DEFAULT ''
			);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN items.color NOT EXIST
			ALTER TABLE items ADD COLUMN color TEXT NOT NULL DEFAULT '';
		`)},
	}

	if err := Migrate(db, fsys); err != nil {
		t.Fatal(err)
	}
}
