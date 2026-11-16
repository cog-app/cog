package feed_test

import (
	"errors"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
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

// edgesPending reports whether the edges worker has the item to do.
func edgesPending(t *testing.T, store *feed.Store, id int64) bool {
	t.Helper()
	ids, err := store.PendingEdges(10)
	require.NoError(t, err)
	return len(ids) == 1 && ids[0] == id
}

// edgesMade is a packaged item, silence done, whose edges were wanted and made.
func edgesMade(t *testing.T) (*feed.Store, int64) {
	t.Helper()
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	require.NoError(t, store.SetSilenceRanges(id, nil))
	changed, err := store.WantEdges(id)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, edgesPending(t, store, id))
	require.NoError(t, store.SetEdges(id, "abcdefabcdef"))
	require.False(t, edgesPending(t, store, id))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	require.True(t, d.EdgesWanted)
	require.Equal(t, "abcdefabcdef", d.EdgesEdit)
	return store, id
}

func TestEdgesPending(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	assert.False(t, edgesPending(t, store, id), "silence not done")
	require.NoError(t, store.SetSilenceRanges(id, nil))
	assert.True(t, edgesPending(t, store, id))
	require.NoError(t, store.SetEdges(id, "abc"))
	assert.False(t, edgesPending(t, store, id))
	require.NoError(t, store.SetSilenceRanges(id, []feed.Range{{Start: 1, End: 3, Action: "skip"}}))
	assert.True(t, edgesPending(t, store, id), "new silence clears the edges")
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
}

func TestEdgesNotPendingUnwantedOrUnpackaged(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SetSilenceRanges(id, nil))
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	assert.False(t, edgesPending(t, store, id), "not packaged")
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	assert.True(t, edgesPending(t, store, id))
	// A clean file with no track yet is part of the plan: wait for it.
	require.NoError(t, store.SetCleanAudioPath(id, "media/1.clean.m4a"))
	assert.False(t, edgesPending(t, store, id), "clean track pending")
	require.NoError(t, store.SaveCleanTrack(id, "media/1.clean.hls.m4a"))
	assert.True(t, edgesPending(t, store, id))
}

func TestEdgesResetOnRepackage(t *testing.T) {
	store, id := edgesMade(t)
	require.NoError(t, store.SavePackage(id, "media/2.hls-audio.m4a", "", 300))
	assert.True(t, edgesPending(t, store, id))
}

func TestEdgesResetOnCleanTrack(t *testing.T) {
	for name, reset := range map[string]func(*feed.Store, int64) error{
		"SaveCleanTrack":    func(s *feed.Store, id int64) error { return s.SaveCleanTrack(id, "media/1.clean.hls.m4a") },
		"SetCleanAudioPath": func(s *feed.Store, id int64) error { return s.SetCleanAudioPath(id, "") },
		"ResetCleanAudio":   func(s *feed.Store, id int64) error { return s.ResetCleanAudio(id) },
		"MarkDownloadReady": func(s *feed.Store, id int64) error { return s.MarkDownloadReady(id, "media/1.mp3", 100) },
	} {
		t.Run(name, func(t *testing.T) {
			store, id := edgesMade(t)
			require.NoError(t, reset(store, id))
			d, err := store.GetDownload(id)
			require.NoError(t, err)
			assert.Equal(t, "", d.EdgesEdit)
			assert.Equal(t, "", d.EdgesError)
		})
	}
}

func TestEdgesResetOnPrefChange(t *testing.T) {
	store, id := edgesMade(t)
	prefs, err := store.LoadPlayerPrefs()
	require.NoError(t, err)
	require.NoError(t, store.SavePlayerPrefs(prefs))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "abcdefabcdef", d.EdgesEdit, "the same value keeps the edges")

	prefs.SkipSilenceMinSec = prefs.SkipSilenceMinSec + 1
	require.NoError(t, store.SavePlayerPrefs(prefs))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
	assert.True(t, edgesPending(t, store, id))
}

func TestEdgesErrorNotRetried(t *testing.T) {
	store, id := edgesMade(t)
	require.NoError(t, store.ResetEdges(id))
	require.NoError(t, store.MarkEdgesError(id, "abcdefabcdef", "ffmpeg: boom"))
	assert.False(t, edgesPending(t, store, id))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "abcdefabcdef", d.EdgesEdit, "records which edit failed")
	assert.Equal(t, "ffmpeg: boom", d.EdgesError)

	require.NoError(t, store.ResetEdges(id))
	assert.True(t, edgesPending(t, store, id))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
	assert.Equal(t, "", d.EdgesError)
}

func TestResetEdgesAll(t *testing.T) {
	store, id := edgesMade(t)
	require.NoError(t, store.ResetEdges())
	assert.True(t, edgesPending(t, store, id))
}

func TestWantEdgesReportsChange(t *testing.T) {
	store, id := readyItem(t)
	changed, err := store.WantEdges(id)
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = store.WantEdges(id)
	require.NoError(t, err)
	assert.False(t, changed)
	changed, err = store.WantEdges()
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestRecordEdit(t *testing.T) {
	store, id := readyItem(t)
	first := []hls.Cut{{Start: 1, End: 2}, {Start: 5, End: 6.5}}
	require.NoError(t, store.RecordEdit(id, "abcdefabcdef", first))
	require.NoError(t, store.RecordEdit(id, "abcdefabcdef", []hls.Cut{{Start: 9, End: 10}}))
	got, err := store.EditCuts(id, "abcdefabcdef")
	require.NoError(t, err)
	assert.Equal(t, first, got)

	_, err = store.EditCuts(id, "000000000000")
	assert.True(t, errors.Is(err, feed.ErrEditNotFound))
	_, err = store.EditCuts(id+1, "abcdefabcdef")
	assert.True(t, errors.Is(err, feed.ErrEditNotFound))
}

func TestPackagedItemIDs(t *testing.T) {
	store, id := readyItem(t)
	ids, err := store.PackagedItemIDs()
	require.NoError(t, err)
	assert.Equal(t, 0, len(ids))
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	ids, err = store.PackagedItemIDs()
	require.NoError(t, err)
	assert.Equal(t, []int64{id}, ids)
}
