package clock_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

func TestSetAndAdvance(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	tc := clock.NewTest(base)

	if got := tc.Now(); !got.Equal(base) {
		t.Fatalf("Now() = %v, want %v", got, base)
	}

	newTime := base.Add(24 * time.Hour)
	tc.Set(newTime)
	if got := tc.Now(); !got.Equal(newTime) {
		t.Fatalf("after Set, Now() = %v, want %v", got, newTime)
	}

	got := tc.Advance(2 * time.Hour)
	want := newTime.Add(2 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("Advance() = %v, want %v", got, want)
	}
	if now := tc.Now(); !now.Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", now, want)
	}
}

func TestClock(t *testing.T) {
	base := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	tc := clock.NewTest(base)
	c := tc.Clock()

	if got := c(); !got.Equal(base) {
		t.Fatalf("Clock()() = %v, want %v", got, base)
	}

	tc.Advance(time.Minute)
	want := base.Add(time.Minute)
	if got := c(); !got.Equal(want) {
		t.Fatalf("after Advance, Clock()() = %v, want %v", got, want)
	}
}
