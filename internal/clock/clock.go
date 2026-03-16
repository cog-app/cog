// Package clock provides a virtual clock abstraction for testability.
package clock

import (
	"sync"
	"time"
)

// Clock returns the current time. Production code uses System(); tests use TestClock.
type Clock func() time.Time

// System returns a Clock backed by time.Now.
func System() Clock { return time.Now }

// TestClock is a controllable clock for tests.
type TestClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewTest(now time.Time) *TestClock { return &TestClock{now: now} }
func (c *TestClock) Now() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *TestClock) Set(t time.Time)  { c.mu.Lock(); defer c.mu.Unlock(); c.now = t }
func (c *TestClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}
func (c *TestClock) Clock() Clock { return c.Now }
