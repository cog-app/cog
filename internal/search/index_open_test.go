package search

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// TestConcurrentIndexOpenDoesNotWedge covers a deadlock that a burst of writes
// used to cause. Every write whose user index is not open yet spawns a
// goroutine to open it, and bleve takes a file lock it then keeps: whichever
// goroutine won held the lock forever, and every other one — including any
// later search for that user — blocked on it for good.
func TestConcurrentIndexOpenDoesNotWedge(t *testing.T) {
	m := NewManager(slog.Default(), t.TempDir())
	t.Cleanup(m.Stop)

	const openers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		got  []*userSearch
		errs []error
	)
	for range openers {
		wg.Go(func() {
			us, err := m.ensureUserIndex("alice")
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			got = append(got, us)
		})
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("concurrent opens of one user index deadlocked")
	}

	for _, err := range errs {
		t.Errorf("open failed: %v", err)
	}
	if len(got) != openers {
		t.Fatalf("got %d indices, want %d", len(got), openers)
	}
	for i, us := range got[1:] {
		if us != got[0] {
			t.Fatalf("opener %d got a different index; every caller must share one", i+1)
		}
	}
}
