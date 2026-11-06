package budget

import (
	"errors"
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func TestShared(t *testing.T) {
	remaining := int64(10)
	b := New(func(string) (int64, error) { return remaining, nil })

	w1, err := b.Begin("alice")
	require.NoError(t, err)
	w2, err := b.Begin("alice")
	require.NoError(t, err)
	require.NoError(t, w1.Grow(6))
	err = w2.Grow(5)
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "second write over the shared budget: %v", err)
	require.Equal(t, int64(6), b.Pending("alice"))

	// Abandoned: its bytes come back.
	w1.Abandon()
	require.NoError(t, w2.Grow(5))
	// Done: its bytes are recorded, so they leave pending but not left.
	w2.Done()
	require.Equal(t, int64(0), b.Pending("alice"))

	// With no writes in progress the next starts from recorded usage again.
	remaining = 5
	w3, err := b.Begin("alice")
	require.NoError(t, err)
	require.Equal(t, int64(5), w3.Room())
	w3.Done()
	w3.Done() // ending twice is harmless
}

func TestReader(t *testing.T) {
	b := New(func(string) (int64, error) { return 4, nil })
	w, err := b.Begin("alice")
	require.NoError(t, err)

	// Replacing 3 bytes with 7 grows by 4: room enough.
	r := w.Reader(strings.NewReader("1234567"), 3)
	_, err = io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, int64(7), r.N())
	require.Equal(t, int64(0), w.Room())

	// Kept only 2 in the end: 5 come back.
	r.Settle(2)
	require.Equal(t, int64(5), w.Room())
	w.Done()

	w, err = b.Begin("bob")
	require.NoError(t, err)
	_, err = io.ReadAll(w.Reader(strings.NewReader("12345"), 0))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "read past the budget: %v", err)
	w.Abandon()
}

func TestNil(t *testing.T) {
	var b *Budgets
	w, err := b.Begin("alice")
	require.NoError(t, err)
	require.NoError(t, w.Grow(1<<40))
	require.True(t, w.Room() < 0, "a nil budget limits")
	w.Done()
}
