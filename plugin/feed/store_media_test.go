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
	ids, err := store.PendingEdges(10, true)
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
	require.NoError(t, store.SetEdges(id, "abcdefabcdef", testEdgesIndex))
	require.False(t, edgesPending(t, store, id))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	require.True(t, d.EdgesWanted)
	require.Equal(t, "abcdefabcdef", d.EdgesEdit)
	require.NotEqual(t, "", d.EdgesIndex)
	return store, id
}

var testEdgesIndex = feed.EdgesIndex{feed.EdgeOriginal: {{InitOffset: 0, InitLength: 700, Offset: 780, Length: 3000}}}

// Every change that forgets the edges forgets where their media is too.
func TestEdgesIndexClearedOnReset(t *testing.T) {
	for name, reset := range map[string]func(*feed.Store, int64) error{
		"ResetEdges":        func(s *feed.Store, id int64) error { return s.ResetEdges(id) },
		"ResetEdgesAll":     func(s *feed.Store, id int64) error { return s.ResetEdges() },
		"MarkEdgesError":    func(s *feed.Store, id int64) error { return s.MarkEdgesError(id, "abcdefabcdef", "boom") },
		"SetSilenceRanges":  func(s *feed.Store, id int64) error { return s.SetSilenceRanges(id, nil) },
		"SavePackage":       func(s *feed.Store, id int64) error { return s.SavePackage(id, "media/2.hls-audio.m4a", "", 300) },
		"SaveCleanTrack":    func(s *feed.Store, id int64) error { return s.SaveCleanTrack(id, "media/1.clean.hls.m4a") },
		"SetCleanAudioPath": func(s *feed.Store, id int64) error { return s.SetCleanAudioPath(id, "") },
		"ResetCleanAudio":   func(s *feed.Store, id int64) error { return s.ResetCleanAudio(id) },
		"MarkDownloadReady": func(s *feed.Store, id int64) error { return s.MarkDownloadReady(id, "media/1.mp3", 100) },
		"PrefChange": func(s *feed.Store, id int64) error {
			prefs, err := s.LoadPlayerPrefs()
			if err != nil {
				return err
			}
			prefs.SkipSilenceMinSec++
			return s.SavePlayerPrefs(prefs)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, id := edgesMade(t)
			require.NoError(t, reset(store, id))
			d, err := store.GetDownload(id)
			require.NoError(t, err)
			assert.Equal(t, "", d.EdgesIndex)
		})
	}
}

func TestEdgesPending(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	assert.False(t, edgesPending(t, store, id), "silence not done")
	require.NoError(t, store.SetSilenceRanges(id, nil))
	assert.True(t, edgesPending(t, store, id))
	require.NoError(t, store.SetEdges(id, "abc", nil))
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

// A clean file still to come is part of the plan too: edges made before it
// lands would be reset by its track mid-playback and made twice.
func TestEdgesWaitForProfanity(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	require.NoError(t, store.SetSilenceRanges(id, nil))
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	require.True(t, edgesPending(t, store, id))

	// Profanity waits for the captions, which are being made.
	require.NoError(t, store.EnqueueProfanity(id))
	require.NoError(t, store.EnqueueTranscribe(id))
	assert.False(t, edgesPending(t, store, id), "captions to come")
	require.NoError(t, store.SetCaptionsPath(id, "captions/1.vtt"))
	require.NoError(t, store.ClearNeedsTranscribe(id))
	assert.False(t, edgesPending(t, store, id), "profanity to run")
	// Nothing to mute: no clean file.
	require.NoError(t, store.ClearNeedsProfanity(id))
	assert.True(t, edgesPending(t, store, id))

	// Profanity with no captions and none coming never runs: no wait.
	require.NoError(t, store.SetCaptionsPath(id, ""))
	require.NoError(t, store.EnqueueProfanity(id))
	assert.True(t, edgesPending(t, store, id), "profanity cannot run")
}

// Failed edges are tried again after edgesRetryAfter, in case what failed
// was passing (a full disk, a killed ffmpeg).
func TestEdgesRetriedAfterFailure(t *testing.T) {
	store, id := edgesMade(t)
	require.NoError(t, store.MarkEdgesError(id, "abcdefabcdef", "boom"))
	assert.False(t, edgesPending(t, store, id), "just failed")
	_, err := store.DB().Exec(`UPDATE downloads SET edges_failed_at = unixepoch() - 5*3600 WHERE item_id = ?`, id)
	require.NoError(t, err)
	assert.False(t, edgesPending(t, store, id), "failed 5 h ago")
	_, err = store.DB().Exec(`UPDATE downloads SET edges_failed_at = unixepoch() - 7*3600 WHERE item_id = ?`, id)
	require.NoError(t, err)
	assert.True(t, edgesPending(t, store, id), "failed 7 h ago")
}

// The master forgets the edges it saw only if they are still the ones
// recorded: the worker may have recorded others since it looked.
func TestForgetEdgesIfUnchanged(t *testing.T) {
	store, id := edgesMade(t)
	require.NoError(t, store.ForgetEdges(id, "000000000000"))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "abcdefabcdef", d.EdgesEdit, "recorded since")
	assert.NotEqual(t, "", d.EdgesIndex)

	require.NoError(t, store.ForgetEdges(id, "abcdefabcdef"))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
	assert.Equal(t, "", d.EdgesIndex)
	assert.True(t, edgesPending(t, store, id))
}

// Profanity muting that cannot finish does not hold the edges back: a
// transcription or muting failure is recorded, and without the profanity
// plugin nothing mutes at all. The edges are then made from what there is.
func TestEdgesProfanityCannotFinish(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	require.NoError(t, store.SetSilenceRanges(id, nil))
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	require.NoError(t, store.EnqueueProfanity(id))
	require.NoError(t, store.EnqueueTranscribe(id))
	require.False(t, edgesPending(t, store, id), "captions to come")
	pending, err := store.PendingEdges(10, false)
	require.NoError(t, err)
	assert.Equal(t, []int64{id}, pending, "no profanity plugin")

	// Whisper failed: the captions are not coming.
	require.NoError(t, store.MarkTranscribeError(id, "whisper-cli: not found"))
	assert.True(t, edgesPending(t, store, id), "transcription failed")
	// Captions came after all (a retry): muting can run again.
	require.NoError(t, store.SetCaptionsPath(id, "captions/1.vtt"))
	require.NoError(t, store.ClearNeedsTranscribe(id))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.TranscribeError)
	assert.False(t, edgesPending(t, store, id), "profanity to run")

	// Muting failed.
	require.NoError(t, store.MarkProfanityError(id, "tts: not configured"))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "tts: not configured", d.ProfanityError)
	assert.True(t, edgesPending(t, store, id), "muting failed")
	// New captions are a new input: muting is tried again.
	require.NoError(t, store.SetCaptionsPath(id, "captions/1b.vtt"))
	assert.False(t, edgesPending(t, store, id), "new captions")

	// A new download forgets both errors.
	require.NoError(t, store.MarkTranscribeError(id, "boom"))
	require.NoError(t, store.MarkProfanityError(id, "boom"))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", 100))
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.TranscribeError)
	assert.Equal(t, "", d.ProfanityError)
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
			require.NoError(t, store.MarkEdgesError(id, "abcdefabcdef", "boom"))
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
	require.NoError(t, store.RecordEdit(id, "abcdefabcdef", first, 600.5))
	require.NoError(t, store.RecordEdit(id, "abcdefabcdef", []hls.Cut{{Start: 9, End: 10}}, 1))
	got, dur, err := store.EditCuts(id, "abcdefabcdef")
	require.NoError(t, err)
	assert.Equal(t, first, got)
	assert.Equal(t, 600.5, dur)

	_, _, err = store.EditCuts(id, "000000000000")
	assert.True(t, errors.Is(err, feed.ErrEditNotFound))
	_, _, err = store.EditCuts(id+1, "abcdefabcdef")
	assert.True(t, errors.Is(err, feed.ErrEditNotFound))
}

func TestPackagedEdges(t *testing.T) {
	store, id := readyItem(t)
	edges, err := store.PackagedEdges()
	require.NoError(t, err)
	assert.Equal(t, 0, len(edges))
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	edges, err = store.PackagedEdges()
	require.NoError(t, err)
	assert.Equal(t, map[int64]feed.ItemEdges{id: {}}, edges)
	_, err = store.WantEdges(id)
	require.NoError(t, err)
	edges, err = store.PackagedEdges()
	require.NoError(t, err)
	assert.Equal(t, map[int64]feed.ItemEdges{id: {Pending: true}}, edges, "wanted, not made")
	require.NoError(t, store.SetEdges(id, "abcdefabcdef", nil))
	edges, err = store.PackagedEdges()
	require.NoError(t, err)
	assert.Equal(t, map[int64]feed.ItemEdges{id: {Edit: "abcdefabcdef"}}, edges)
	require.NoError(t, store.MarkEdgesError(id, "abcdefabcdef", "boom"))
	edges, err = store.PackagedEdges()
	require.NoError(t, err)
	assert.Equal(t, map[int64]feed.ItemEdges{id: {}}, edges, "failed edges are not kept")
}

func TestEdgesNotPendingUntilWanted(t *testing.T) {
	store, id := readyItem(t)
	require.NoError(t, store.SavePackage(id, "media/1.hls-audio.m4a", "", 300))
	require.NoError(t, store.SetSilenceRanges(id, nil))
	assert.False(t, edgesPending(t, store, id), "not wanted")
	_, err := store.WantEdges(id)
	require.NoError(t, err)
	assert.True(t, edgesPending(t, store, id))
}

func TestResetEdgesScopedToItems(t *testing.T) {
	store, a := edgesMade(t)
	b := secondItem(t, store, a)
	require.NoError(t, store.MarkDownloadReady(b, "media/2.mp3", 100))
	require.NoError(t, store.SavePackage(b, "media/2.hls-audio.m4a", "", 300))
	require.NoError(t, store.SetSilenceRanges(b, nil))
	_, err := store.WantEdges(b)
	require.NoError(t, err)
	require.NoError(t, store.SetEdges(b, "bbbbbbbbbbbb", nil))

	require.NoError(t, store.ResetEdges(a))
	da, err := store.GetDownload(a)
	require.NoError(t, err)
	db, err := store.GetDownload(b)
	require.NoError(t, err)
	assert.Equal(t, "", da.EdgesEdit)
	assert.Equal(t, "bbbbbbbbbbbb", db.EdgesEdit)
}

func TestWantEdgesSeveralIDs(t *testing.T) {
	store, a := readyItem(t)
	b := secondItem(t, store, a)
	wanted := func(id int64) bool {
		t.Helper()
		d, err := store.GetDownload(id)
		require.NoError(t, err)
		return d.EdgesWanted
	}
	changed, err := store.WantEdges(a)
	require.NoError(t, err)
	require.True(t, changed)
	assert.False(t, wanted(b))
	changed, err = store.WantEdges(a, b) // a mix of wanted and unwanted
	require.NoError(t, err)
	assert.True(t, changed)
	assert.True(t, wanted(a))
	assert.True(t, wanted(b))
	changed, err = store.WantEdges(a, b)
	require.NoError(t, err)
	assert.False(t, changed)
}

// secondItem adds another downloading item to the subscription of item a.
func secondItem(t *testing.T, store *feed.Store, a int64) int64 {
	t.Helper()
	it, err := store.GetItem(a)
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: it.SubscriptionID, GUID: "ep2",
		Type: "audio", Title: "Episode 2", MediaURL: "https://cdn.example.com/other.mp3"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	return id
}
