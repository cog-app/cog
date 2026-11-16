package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// readyItem is a store with one downloaded audio item.
func readyItem(t *testing.T) (*feed.Store, int64) {
	t.Helper()
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep.mp3")
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", 100))
	return store, id
}

func pending(t *testing.T, store *feed.Store) []int64 {
	t.Helper()
	ids, err := store.PendingPackage(10)
	require.NoError(t, err)
	return ids
}

func TestSavePackage(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "media/1.hls-video.mp4", 300))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "media/1.hls-audio.m4a", d.LocalPath)
	assert.Equal(t, "media/1.hls-video.mp4", d.VideoPath)
	assert.Equal(t, int64(300), d.Bytes)
	assert.Equal(t, "done", d.PackageState)
	assert.Equal(t, 0, len(pending(t, store)))
	ip, err := store.GetItemPlayback(id)
	require.NoError(t, err)
	assert.True(t, ip.Packaged)
	assert.True(t, ip.HasVideo)
}

func TestPendingPackage(t *testing.T) {
	store, ready := readyItem(t)
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://other.example.com/feed"})
	require.NoError(t, err)
	queued, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "q", Type: "audio", MediaURL: "https://cdn.example.com/q.mp3"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(queued))
	failed, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "e", Type: "audio", MediaURL: "https://cdn.example.com/e.mp3"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(failed))
	require.NoError(t, store.MarkDownloadError(failed, "404"))
	assert.Equal(t, []int64{ready}, pending(t, store))
}

func TestMarkPackageError(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.MarkPackageError(id, "ffmpeg: boom"))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "error", d.PackageState)
	assert.Equal(t, "ffmpeg: boom", d.PackageError)
	assert.Equal(t, "media/1.mp3", d.LocalPath)
	assert.Equal(t, 0, len(pending(t, store)))
}

func TestMarkDownloadReadyResetsPackage(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "media/1.hls-video.mp4", 300))
	require.NoError(t, store.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 50))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.PackageState)
	assert.Equal(t, "media/1.m4a", d.LocalPath)
	assert.Equal(t, "media/1.video.mp4", d.VideoPath)
	assert.Equal(t, []int64{id}, pending(t, store))

	require.NoError(t, store.MarkPackageError(id, "x"))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", 100))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.PackageState)
	assert.Equal(t, "", d.PackageError)
	assert.Equal(t, "", d.VideoPath)
	assert.Equal(t, []int64{id}, pending(t, store))
}

func TestHLSKeyStableAndRotates(t *testing.T) {
	store, _ := readyItem(t)
	k1, err := store.HLSKey()
	require.NoError(t, err)
	k2, err := store.HLSKey()
	require.NoError(t, err)
	assert.Equal(t, 32, len(k1))
	assert.Equal(t, k1, k2)
	require.NoError(t, store.RotateHLSKey())
	k3, err := store.HLSKey()
	require.NoError(t, err)
	assert.Equal(t, 32, len(k3))
	assert.NotEqual(t, k1, k3)
}

func TestCleanTrackStore(t *testing.T) {
	store, id := readyItem(t)
	pendingClean := func() []int64 {
		t.Helper()
		ids, err := store.PendingCleanPackage(10)
		require.NoError(t, err)
		return ids
	}
	cleanPending := func() bool {
		t.Helper()
		ip, err := store.GetItemPlayback(id)
		require.NoError(t, err)
		return ip.CleanPending
	}

	// Not packaged yet: never pending.
	require.NoError(t, store.SetCleanAudioPath(id, "media/1.clean.m4a"))
	assert.Equal(t, 0, len(pendingClean()))

	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	assert.Equal(t, []int64{id}, pendingClean())
	assert.True(t, cleanPending())

	require.NoError(t, store.SaveCleanTrack(id, "media/1.hls-audio.clean.m4a"))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "media/1.hls-audio.clean.m4a", d.CleanTrackPath)
	assert.Equal(t, "media/1.hls-audio.clean.m4a", d.CleanAudioPath)
	assert.Equal(t, 0, len(pendingClean()))
	assert.False(t, cleanPending())

	require.NoError(t, store.SetCleanAudioPath(id, "media/1.clean.m4a"))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanTrackPath)
	assert.Equal(t, []int64{id}, pendingClean())
	assert.True(t, cleanPending())

	require.NoError(t, store.MarkCleanTrackError(id, "bad"))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "bad", d.CleanTrackError)
	assert.Equal(t, 0, len(pendingClean()))
}

// A new download replaces the old audio, so the clean file made from it is
// stale: it is forgotten and the item is cleaned again.
func TestMarkDownloadReadyResetsClean(t *testing.T) {
	s, id := readyItem(t)
	require.NoError(t, s.SetCleanAudioPath(id, "media/1.clean.m4a"))
	require.NoError(t, s.SaveCleanTrack(id, "media/1.hls-audio.clean.m4a"))
	require.NoError(t, s.ClearNeedsProfanity(id))

	require.NoError(t, s.MarkDownloadReady(id, "media/1.mp3", 2))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanAudioPath)
	assert.Equal(t, "", d.CleanTrackPath)
	assert.Equal(t, "", d.CleanTrackError)
	assert.True(t, d.NeedsProfanity)
}

// An item that was never cleaned is not queued for cleaning by a download.
func TestMarkDownloadReadyLeavesUncleanedAlone(t *testing.T) {
	s, id := readyItem(t)
	require.NoError(t, s.MarkDownloadReady(id, "media/1.mp3", 2))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.False(t, d.NeedsProfanity)
}
