package logbuf

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestAddAndEntries(t *testing.T) {
	b := New(3)
	now := time.Now()
	b.Add(Entry{Time: now, Level: slog.LevelInfo, Message: "one"})
	b.Add(Entry{Time: now, Level: slog.LevelInfo, Message: "two"})

	entries := b.Entries()
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Message != "one" || entries[1].Message != "two" {
		t.Fatalf("unexpected order: %v", entries)
	}
}

func TestWrapAround(t *testing.T) {
	b := New(3)
	now := time.Now()
	for i := range 5 {
		b.Add(Entry{Time: now, Level: slog.LevelInfo, Message: string(rune('a' + i))})
	}

	if b.Len() != 3 {
		t.Fatalf("got len %d, want 3", b.Len())
	}
	entries := b.Entries()
	// Should have c, d, e (oldest two overwritten)
	if entries[0].Message != "c" || entries[1].Message != "d" || entries[2].Message != "e" {
		t.Fatalf("unexpected entries after wrap: %v", entries)
	}
}

func TestConcurrentWrites(t *testing.T) {
	b := New(100)
	var wg sync.WaitGroup
	now := time.Now()
	for range 10 {
		wg.Go(func() {
			for range 50 {
				b.Add(Entry{Time: now, Level: slog.LevelInfo, Message: "x"})
			}
		})
	}
	wg.Wait()

	if b.Len() != 100 {
		t.Fatalf("got len %d, want 100", b.Len())
	}
}
