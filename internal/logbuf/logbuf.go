// Package logbuf provides an in-memory ring buffer for slog entries.
package logbuf

import (
	"log/slog"
	"sync"
	"time"
)

// DefaultCapacity is the default number of entries the ring buffer holds.
const DefaultCapacity = 1000

// Entry is a single captured log record.
type Entry struct {
	Time    time.Time
	Level   slog.Level
	Message string // formatted line from slog.TextHandler
}

// Buffer is a fixed-size ring buffer of log entries.
type Buffer struct {
	mu      sync.Mutex
	entries []Entry
	head    int
	count   int
}

// New creates a ring buffer with the given capacity.
func New(capacity int) *Buffer {
	return &Buffer{entries: make([]Entry, capacity)}
}

// Add appends an entry, overwriting the oldest if full.
func (b *Buffer) Add(e Entry) {
	b.mu.Lock()
	b.entries[b.head] = e
	b.head = (b.head + 1) % len(b.entries)
	if b.count < len(b.entries) {
		b.count++
	}
	b.mu.Unlock()
}

// Entries returns a chronological copy of all buffered entries (oldest first).
func (b *Buffer) Entries() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]Entry, b.count)
	if b.count < len(b.entries) {
		copy(out, b.entries[:b.count])
	} else {
		n := copy(out, b.entries[b.head:])
		copy(out[n:], b.entries[:b.head])
	}
	return out
}

// Len returns the number of entries currently buffered.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}
