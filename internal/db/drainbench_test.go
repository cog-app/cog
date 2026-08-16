package db

import (
	"context"
	"path/filepath"
	"testing"
)

// BenchmarkDrainEmpty measures the common case: the drainer wakes, finds
// nothing, and goes back to sleep. Most per-user databases are idle most of
// the time, so this runs far more often than a drain that finds work.
func BenchmarkDrainEmpty(b *testing.B) {
	sdb, err := OpenPluginDB(filepath.Join(b.TempDir(), "plugin.db"), nil, false)
	if err != nil {
		b.Fatal(err)
	}
	defer sdb.Close()
	if _, err := sdb.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, title TEXT)`); err != nil {
		b.Fatal(err)
	}
	if err := EnsureChangeCapture(sdb, []string{"items"}); err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		if _, err := DrainChanges(ctx, sdb, 500); err != nil {
			b.Fatal(err)
		}
	}
}
