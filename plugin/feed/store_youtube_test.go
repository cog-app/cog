package feed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// youtubeItem is a stored item the streams can belong to.
func youtubeItem(t *testing.T, s *Store) int64 {
	t.Helper()
	subID, err := s.CreateSubscription(Subscription{Kind: "youtube", URL: "https://example.com/yt", Title: "yt", PollIntervalSec: 1800})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(Item{SubscriptionID: subID, GUID: "g", Type: "video", Title: "v", URL: "https://www.youtube.com/watch?v=x"})
	require.NoError(t, err)
	return id
}

func tracks(expires time.Time) (YouTubeTrack, YouTubeTrack) {
	return YouTubeTrack{Kind: hls.Video, FormatID: "137", URL: "https://gv/v", ExpiresAt: expires},
		YouTubeTrack{Kind: hls.Audio, FormatID: "140", URL: "https://gv/a", ExpiresAt: expires}
}

// countingResolver stubs the resolver; it returns tracks expiring at expires
// and records each call's format.
func countingResolver(t *testing.T, expires time.Time) (calls *atomic.Int32, formats *[]string) {
	t.Helper()
	calls = new(atomic.Int32)
	formats = new([]string)
	var mu sync.Mutex
	restore := SetTrackResolver(func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error) {
		calls.Add(1)
		mu.Lock()
		*formats = append(*formats, format)
		mu.Unlock()
		v, a := tracks(expires)
		return v, a, nil
	})
	t.Cleanup(restore)
	return calls, formats
}

func TestYouTubeStreamsRoundTrip(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	exp := time.Unix(1_800_000_000, 0)
	v, a := tracks(exp)
	// A track with no available_at reads back with none.
	v.AvailableAt = time.Unix(1_799_999_000, 0)
	require.NoError(t, s.SaveYouTubeStreams(id, v, a))
	gv, ga, ok, err := s.YouTubeStreams(id)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, v, gv)
	assert.Equal(t, a, ga)

	require.NoError(t, s.ForgetYouTubeStreams(id))
	_, _, ok, err = s.YouTubeStreams(id)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestYouTubeStreamsCached(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	calls, _ := countingResolver(t, now.Add(6*time.Hour))
	p := New()
	for range 2 {
		_, _, err := p.youtubeTracks(t.Context(), s, "u", id, "https://yt/w", now, false)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestYouTubeStreamsRefreshEarly(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	calls, _ := countingResolver(t, now.Add(9*time.Minute))
	p := New()
	for range 2 {
		_, _, err := p.youtubeTracks(t.Context(), s, "u", id, "https://yt/w", now, false)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(2), calls.Load())

	// Exactly 10 minutes left still refreshes.
	id3 := youtubeItem3(t, s)
	calls3, _ := countingResolver(t, now.Add(10*time.Minute))
	for range 2 {
		_, _, err := p.youtubeTracks(t.Context(), s, "u", id3, "https://yt/w", now, false)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(2), calls3.Load())

	// 11 minutes left: no refresh.
	id2 := youtubeItem2(t, s)
	calls2, _ := countingResolver(t, now.Add(11*time.Minute))
	for range 2 {
		_, _, err := p.youtubeTracks(t.Context(), s, "u", id2, "https://yt/w", now, false)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls2.Load())
}

// youtubeItem3 is a third item.
func youtubeItem3(t *testing.T, s *Store) int64 {
	t.Helper()
	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	id, _, err := s.UpsertItem(Item{SubscriptionID: subs[0].ID, GUID: "g3", Type: "video", Title: "v3", URL: "https://www.youtube.com/watch?v=z"})
	require.NoError(t, err)
	return id
}

// youtubeItem2 is a second item.
func youtubeItem2(t *testing.T, s *Store) int64 {
	t.Helper()
	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	id, _, err := s.UpsertItem(Item{SubscriptionID: subs[0].ID, GUID: "g2", Type: "video", Title: "v2", URL: "https://www.youtube.com/watch?v=y"})
	require.NoError(t, err)
	return id
}

func TestYouTubeStreamsFreshFormat(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	_, formats := countingResolver(t, now.Add(6*time.Hour))
	_, _, err := New().youtubeTracks(t.Context(), s, "u", id, "https://yt/w", now, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"(" + YouTubeVideoFormat + ")+(" + YouTubeAudioFormat + ")"}, *formats)
}

func TestYouTubeStreamsRefreshPinsFormats(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	v, a := tracks(now.Add(6 * time.Hour))
	require.NoError(t, s.SaveYouTubeStreams(id, v, a))
	_, formats := countingResolver(t, now.Add(6*time.Hour))
	_, _, err := New().youtubeTracks(t.Context(), s, "u", id, "https://yt/w", now, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"(137)+(140)"}, *formats)
}

func TestYouTubeStreamsSingleflight(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	restore := SetTrackResolver(func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		v, a := tracks(now.Add(6 * time.Hour))
		return v, a, nil
	})
	defer restore()
	p := New()
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			_, _, err := p.youtubeTracks(context.Background(), s, "u", id, "https://yt/w", now, false)
			assert.NoError(t, err)
		})
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		wg.Wait()
		t.Fatal("the resolver was never called")
	}
	time.Sleep(50 * time.Millisecond) // let the others join the flight
	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

func TestYouTubeStreamsDeadPinnedFormatsResolveFresh(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	v, a := tracks(now.Add(5 * time.Minute)) // near expiry, so it resolves
	require.NoError(t, s.SaveYouTubeStreams(id, v, a))
	fresh := YouTubeFormat(YouTubeVideoFormat, YouTubeAudioFormat)
	var formats []string
	restore := SetTrackResolver(func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error) {
		formats = append(formats, format)
		if format != fresh {
			return YouTubeTrack{}, YouTubeTrack{}, errors.New("format not available")
		}
		nv, na := tracks(now.Add(6 * time.Hour))
		nv.FormatID, na.FormatID = "299", "251"
		return nv, na, nil
	})
	defer restore()
	gv, ga, err := New().youtubeTracks(t.Context(), s, "u", id, "https://yt/w", now, false)
	require.NoError(t, err)
	assert.Equal(t, []string{"(137)+(140)", fresh}, formats)
	assert.Equal(t, "299", gv.FormatID)
	assert.Equal(t, "251", ga.FormatID)
	sv, sa, ok, err := s.YouTubeStreams(id)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "299", sv.FormatID)
	assert.Equal(t, "251", sa.FormatID)
}

func TestYouTubeStreamsResolveFailure(t *testing.T) {
	s := newStoreInternal(t)
	id := youtubeItem(t, s)
	now := time.Unix(1_700_000_000, 0)
	boom := errors.New("boom")
	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 8)
	restore := SetTrackResolver(func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return YouTubeTrack{}, YouTubeTrack{}, boom
	})
	defer restore()
	p := New()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() {
			_, _, errs[i] = p.youtubeTracks(context.Background(), s, "u", id, "https://yt/w", now, false)
		})
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		wg.Wait()
		t.Fatal("the resolver was never called")
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, err := range errs {
		assert.True(t, errors.Is(err, boom), "got %v", err)
	}
	assert.Equal(t, int32(1), calls.Load())
	_, _, ok, err := s.YouTubeStreams(id)
	require.NoError(t, err)
	assert.False(t, ok)
}
