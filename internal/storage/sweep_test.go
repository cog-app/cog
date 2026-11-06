package storage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/storage"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

// TestSweepCaches: what a cache has not used for the age is removed, with
// the directories it leaves empty; what was read recently stays, and
// counted storage is never touched.
func TestSweepCaches(t *testing.T) {
	s, dir, _ := newStore(t, "alice")
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	cache, err := s.Dir(storage.Cache, "files", "alice")
	require.NoError(t, err)
	own, err := s.Dir(storage.Counted, "files", "alice")
	require.NoError(t, err)
	for _, n := range []string{"thumbnails/64/old.jpg", "thumbnails/64/used.jpg", "previews/new.pdf"} {
		_, err := cache.Put(n, strings.NewReader("x"))
		require.NoError(t, err)
	}
	_, err = own.Put("keep.txt", strings.NewReader("x"))
	require.NoError(t, err)

	// Made a month and a half ago, all but the new preview.
	cacheDir := filepath.Join(dir, "alice", "app_data", "files", "cache")
	long := now.Add(-45 * 24 * time.Hour)
	for _, n := range []string{"thumbnails/64/old.jpg", "thumbnails/64/used.jpg"} {
		require.NoError(t, os.Chtimes(filepath.Join(cacheDir, n), long, long))
	}
	require.NoError(t, os.Chtimes(filepath.Join(dir, "alice", "app_data", "files", "storage", "keep.txt"), long, long))
	// Read since: a hit marks it used.
	f, err := cache.Open("thumbnails/64/used.jpg")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	removed, err := s.SweepCaches(30 * 24 * time.Hour)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, err = cache.Stat("thumbnails/64/old.jpg")
	require.True(t, err != nil, "an unused entry survived")
	for _, n := range []string{"thumbnails/64/used.jpg", "previews/new.pdf"} {
		_, err := cache.Stat(n)
		require.NoError(t, err, n)
	}
	require.Equal(t, "x", read(t, own, "keep.txt"))

	// Once nothing is left, the directories go too.
	now = now.Add(60 * 24 * time.Hour)
	_, err = s.SweepCaches(30 * 24 * time.Hour)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(cacheDir, "thumbnails"))
	require.True(t, os.IsNotExist(err), "an emptied directory stayed: %v", err)
	_, err = os.Stat(cacheDir)
	require.NoError(t, err)
}

func TestThumbnailSize(t *testing.T) {
	for in, want := range map[int]int{0: 32, 16: 32, 32: 32, 33: 64, 100: 128, 512: 512, 600: 1024, 4096: 1024} {
		require.Equal(t, want, plugin.ThumbnailSize(in), "size %d", in)
	}
}
