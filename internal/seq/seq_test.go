package seq_test

import (
	"errors"
	"iter"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// fromSlice builds an iter.Seq2[T, error] from a slice plus an optional error
// that fires after all elements.
func fromSlice[T any](items []T, tail error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, v := range items {
			if !yield(v, nil) {
				return
			}
		}
		if tail != nil {
			var zero T
			yield(zero, tail)
		}
	}
}

// --- Collect ---

func TestCollect_All(t *testing.T) {
	got, err := seq.Collect(fromSlice([]int{1, 2, 3}, nil))
	require.NoError(t, err)
	require.Equal(t, 3, len(got))
	require.Equal(t, 1, got[0])
	require.Equal(t, 3, got[2])
}

func TestCollect_Empty(t *testing.T) {
	got, err := seq.Collect(fromSlice([]int{}, nil))
	require.NoError(t, err)
	require.Equal(t, 0, len(got))
}

func TestCollect_StopsOnError(t *testing.T) {
	boom := errors.New("boom")
	src := func(yield func(int, error) bool) {
		if !yield(1, nil) {
			return
		}
		if !yield(2, nil) {
			return
		}
		yield(0, boom)
	}
	got, err := seq.Collect(src)
	require.True(t, errors.Is(err, boom))
	require.Equal(t, 2, len(got))
}

// --- Peek ---

func TestPeek_FirstElementAndResumed(t *testing.T) {
	first, resumed, err := seq.Peek(fromSlice([]string{"a", "b", "c"}, nil))
	require.NoError(t, err)
	require.Equal(t, "a", first)

	all, err := seq.Collect(resumed)
	require.NoError(t, err)
	require.Equal(t, 3, len(all))
	require.Equal(t, "a", all[0])
	require.Equal(t, "c", all[2])
}

func TestPeek_EmptyIteratorReturnsErrEmpty(t *testing.T) {
	_, resumed, err := seq.Peek(fromSlice([]int{}, nil))
	require.True(t, errors.Is(err, seq.ErrEmpty))

	all, err := seq.Collect(resumed)
	require.NoError(t, err)
	require.Equal(t, 0, len(all))
}

func TestPeek_FirstYieldError(t *testing.T) {
	boom := errors.New("boom")
	src := iter.Seq2[int, error](func(yield func(int, error) bool) {
		_ = yield(0, boom)
	})
	_, _, err := seq.Peek(src)
	require.True(t, errors.Is(err, boom))
}

func TestPeek_ResumedStopsCleanlyOnYieldFalse(t *testing.T) {
	// Caller breaks out of resumed mid-iteration — Peek must call stop()
	// on the underlying Pull2 iterator so its goroutine exits. We can't
	// observe the goroutine directly here; just ensure no panic and that
	// breaking out doesn't deadlock the caller.
	_, resumed, err := seq.Peek(fromSlice([]int{1, 2, 3, 4, 5}, nil))
	require.NoError(t, err)
	var seen int
	for v := range resumed {
		seen++
		if v == 2 {
			break
		}
	}
	require.Equal(t, 2, seen)
}

// --- Filter ---

func TestFilter_Pred(t *testing.T) {
	src := fromSlice([]int{1, 2, 3, 4, 5}, nil)
	got, err := seq.Collect(seq.Filter(src, func(v int) bool { return v%2 == 0 }))
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
	require.Equal(t, 2, got[0])
	require.Equal(t, 4, got[1])
}

func TestFilter_StopsOnError(t *testing.T) {
	boom := errors.New("boom")
	src := fromSlice([]int{1, 2, 3}, boom)
	_, err := seq.Collect(seq.Filter(src, func(v int) bool { return true }))
	require.True(t, errors.Is(err, boom))
}

func TestFilter_EarlyTermination(t *testing.T) {
	src := fromSlice([]int{1, 2, 3, 4, 5}, nil)
	var seen int
	for range seq.Filter(src, func(v int) bool { return true }) {
		seen++
		if seen == 2 {
			break
		}
	}
	require.Equal(t, 2, seen)
}

// --- ToMap ---

func TestToMap(t *testing.T) {
	type pair struct {
		k string
		v int
	}
	src := fromSlice([]pair{{"a", 1}, {"b", 2}}, nil)
	m, err := seq.ToMap(src,
		func(p pair) string { return p.k },
		func(p pair) int { return p.v },
	)
	require.NoError(t, err)
	require.Equal(t, 1, m["a"])
	require.Equal(t, 2, m["b"])
}

func TestToMap_StopsOnError(t *testing.T) {
	boom := errors.New("boom")
	src := fromSlice([]int{1, 2}, boom)
	_, err := seq.ToMap(src,
		func(v int) int { return v },
		func(v int) int { return v * 10 },
	)
	require.True(t, errors.Is(err, boom))
}
