package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

func benchInserts(b *testing.B, capture bool) {
	b.Helper()
	sdb, err := OpenPluginDB(filepath.Join(b.TempDir(), "plugin.db"), nil, false)
	if err != nil {
		b.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		b.Fatal(err)
	}
	if capture {
		if err := EnsureChangeCapture(sdb, []string{"items"}); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if _, err := sdb.Exec(`INSERT INTO items (title) VALUES (?)`, fmt.Sprint(i)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInsertWithoutCapture(b *testing.B) { benchInserts(b, false) }
func BenchmarkInsertWithCapture(b *testing.B)    { benchInserts(b, true) }

// Bulk inserts in one transaction, which is how a scan or import writes.
func benchBulk(b *testing.B, capture bool) {
	b.Helper()
	sdb, err := OpenPluginDB(filepath.Join(b.TempDir(), "plugin.db"), nil, false)
	if err != nil {
		b.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		b.Fatal(err)
	}
	if capture {
		if err := EnsureChangeCapture(sdb, []string{"items"}); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for b.Loop() {
		tx, err := sdb.Begin()
		if err != nil {
			b.Fatal(err)
		}
		for i := range 1000 {
			if _, err := tx.Exec(`INSERT INTO items (title) VALUES (?)`, i); err != nil {
				b.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBulk1000WithoutCapture(b *testing.B) { benchBulk(b, false) }
func BenchmarkBulk1000WithCapture(b *testing.B)    { benchBulk(b, true) }
