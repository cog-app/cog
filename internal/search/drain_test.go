package search

import (
	"log/slog"
	"testing"
)

func TestNudgeDeduplicatesAndClears(t *testing.T) {
	d := newDrainer(NewManager(slog.Default(), t.TempDir()))

	d.nudge("alice", "feed")
	d.nudge("alice", "feed")
	d.nudge("alice", "notes")
	d.nudge("bob", "feed")

	keys := d.take()
	if len(keys) != 3 {
		t.Fatalf("expected 3 distinct databases, got %d: %+v", len(keys), keys)
	}

	// A write storm on one table should cost one drain, not one per row.
	if again := d.take(); len(again) != 0 {
		t.Fatalf("take did not clear the set: %+v", again)
	}
}

// TestNudgeIsNonBlocking matters because the nudge runs inside the SQLite
// write path: blocking there would stall the write that triggered it.
func TestNudgeIsNonBlocking(t *testing.T) {
	d := newDrainer(NewManager(slog.Default(), t.TempDir()))

	// The wake channel holds one token. Filling it must not wedge callers.
	for range 1000 {
		d.nudge("alice", "feed")
	}

	if len(d.take()) != 1 {
		t.Fatal("expected the storm to collapse to one database")
	}
}
