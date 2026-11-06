// Package budget shares what is left of each user's quota among their
// writes in progress.
//
// Usage is counted from what has been recorded: the files plugin's rows and
// plugin storage's usage table. A write is recorded only when it is done, so
// writes made at once, each checked against recorded usage alone, could each
// be given the whole of what is left. Instead every writer (an upload, a file
// open over SFTP, a plugin's Put) draws its growth from one budget per user
// as the bytes arrive, and leaves it once its bytes are recorded.
package budget

import (
	"fmt"
	"io"
	"sync"

	"git.duckfam.us/jj-software/cog/plugin"
)

// Budgets holds the budget of each user with writes in progress. A nil
// *Budgets limits nothing.
type Budgets struct {
	remaining func(username string) (int64, error)

	mu sync.Mutex
	m  map[string]*entry
}

type entry struct {
	writes  int   // writes in progress
	left    int64 // bytes they may still add; negative for no limit
	pending int64 // bytes they have added that are not recorded yet
}

// New returns Budgets that start each user's budget from remaining, which
// reports what recorded usage leaves of their quota, or a negative number for
// no limit.
func New(remaining func(username string) (int64, error)) *Budgets {
	return &Budgets{remaining: remaining, m: map[string]*entry{}}
}

// Begin starts a write for username, joining the budget of their other
// writes in progress or starting it from their quota.
func (b *Budgets) Begin(username string) (*Write, error) {
	if b == nil {
		return &Write{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.m[username]
	if e == nil {
		left, err := b.remaining(username)
		if err != nil {
			return nil, err
		}
		e = &entry{left: left}
		b.m[username] = e
	}
	e.writes++
	return &Write{b: b, username: username, e: e}, nil
}

// Pending returns what username's writes in progress have added and not
// yet recorded, for a check against recorded usage to count too.
func (b *Budgets) Pending(username string) int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if e := b.m[username]; e != nil {
		return e.pending
	}
	return 0
}

// Write is one write in progress. Its methods are safe on the zero Write,
// which is not limited.
type Write struct {
	b        *Budgets
	username string
	e        *entry
	taken    int64 // what this write holds of the budget
	done     bool
}

// Grow takes delta more bytes for the write, or gives them back when delta
// is negative. It fails, taking nothing, when there is not room.
func (w *Write) Grow(delta int64) error {
	if w.b == nil {
		return nil
	}
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	if delta > 0 && w.e.left >= 0 && delta > w.e.left {
		return fmt.Errorf("%w: it would be larger than the space left", plugin.ErrQuotaExceeded)
	}
	w.take(delta)
	return nil
}

// Shrink gives back n bytes the write took.
func (w *Write) Shrink(n int64) {
	if w.b == nil {
		return
	}
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	w.take(-n)
}

// take moves delta bytes into the write; the caller holds the lock.
func (w *Write) take(delta int64) {
	if w.e.left >= 0 {
		w.e.left -= delta
	}
	w.e.pending += delta
	w.taken += delta
}

// Room returns what the write may still add, or a negative number for no
// limit.
func (w *Write) Room() int64 {
	if w.b == nil {
		return -1
	}
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.e.left
}

// Done ends the write once what it took is recorded: its bytes now count in
// usage, so they leave the budget.
func (w *Write) Done() {
	w.end(false)
}

// Abandon ends a write that recorded nothing, giving back what it took.
func (w *Write) Abandon() {
	w.end(true)
}

func (w *Write) end(giveBack bool) {
	if w.b == nil || w.done {
		return
	}
	w.done = true
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	if giveBack && w.e.left >= 0 {
		w.e.left += w.taken
	}
	w.e.pending -= w.taken
	if w.e.writes--; w.e.writes == 0 {
		delete(w.b.m, w.username)
	}
}

// Reader returns r, taking from the write's budget each byte read past base,
// the size of what the bytes replace; a read with no room fails with
// plugin.ErrQuotaExceeded.
func (w *Write) Reader(r io.Reader, base int64) *Reader {
	return &Reader{r: r, w: w, end: base}
}

// Reader is a reader drawing on a write's budget; see Write.Reader.
type Reader struct {
	r   io.Reader
	w   *Write
	n   int64 // bytes read
	end int64 // bytes charged for, counting what is replaced
}

func (c *Reader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	if c.n > c.end {
		if gerr := c.w.Grow(c.n - c.end); gerr != nil {
			return n, gerr
		}
		c.end = c.n
	}
	return n, err
}

// N returns the bytes read.
func (c *Reader) N() int64 { return c.n }

// Settle gives back what was charged beyond size, the bytes finally kept:
// a file smaller than what it replaced frees the difference.
func (c *Reader) Settle(size int64) {
	if c.end > size {
		c.w.Shrink(c.end - size)
		c.end = size
	}
}
