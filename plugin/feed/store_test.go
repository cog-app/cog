package feed_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func newStore(t *testing.T) *feed.Store {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	return feed.NewStore(db)
}

func TestFoldersCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateFolder("Development")
	require.NoError(t, err)
	if id == 0 {
		t.Fatal("zero id")
	}
	fs, err := s.ListFolders()
	require.NoError(t, err)
	if len(fs) != 1 || fs[0].Name != "Development" {
		t.Fatalf("got %+v", fs)
	}
	require.NoError(t, s.RenameFolder(id, "Dev"))
	require.NoError(t, s.DeleteFolder(id))
	fs, _ = s.ListFolders()
	if len(fs) != 0 {
		t.Fatalf("folders not deleted: %+v", fs)
	}
}

func TestSubscriptionsCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateSubscription(feed.Subscription{
		Kind:            "rss",
		URL:             "https://example.com/feed.xml",
		Title:           "Example",
		PollIntervalSec: 1800,
		NextCheckAt:     time.Now().Unix(),
	})
	require.NoError(t, err)
	sub, err := s.GetSubscription(id)
	require.NoError(t, err)
	if sub.URL != "https://example.com/feed.xml" {
		t.Fatalf("got %+v", sub)
	}
	require.NoError(t, s.PauseSubscription(id, true))

	subs, err := s.ListDueSubscriptions(time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	if len(subs) != 0 {
		t.Fatal("paused subscriptions should not be due")
	}

	require.NoError(t, s.PauseSubscription(id, false))
	subs, err = s.ListDueSubscriptions(time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	if len(subs) != 1 {
		t.Fatalf("want 1 due, got %d", len(subs))
	}
}

func TestItemsAndTags(t *testing.T) {
	s := newStore(t)
	subID, err := s.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://x/feed.xml", PollIntervalSec: 1800,
	})
	require.NoError(t, err)

	id, inserted, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "guid-1",
		Type:           "text",
		Title:          "Hello",
		ContentHTML:    "<p>body</p>",
		PublishedAt:    1000,
	})
	require.NoError(t, err)
	if !inserted || id == 0 {
		t.Fatalf("expected insert, got id=%d inserted=%v", id, inserted)
	}

	id2, inserted2, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "guid-1", Type: "text", Title: "dup",
	})
	require.NoError(t, err)
	if inserted2 {
		t.Fatal("expected dedup")
	}
	if id2 != id {
		t.Fatalf("dedup returned wrong id: %d vs %d", id2, id)
	}

	require.NoError(t, s.AddTag(id, "sunday"))
	require.NoError(t, s.AddTag(id, "weekly"))
	tags, err := s.ItemTags(id)
	require.NoError(t, err)
	if len(tags) != 2 {
		t.Fatalf("got tags %v", tags)
	}

	// AddTag is idempotent on (item, tag).
	require.NoError(t, s.AddTag(id, "sunday"))
	tags, _ = s.ItemTags(id)
	if len(tags) != 2 {
		t.Fatalf("AddTag not idempotent: %v", tags)
	}

	require.NoError(t, s.RemoveTag(id, "sunday"))
	tags, _ = s.ItemTags(id)
	if len(tags) != 1 || tags[0] != "weekly" {
		t.Fatalf("after remove: %v", tags)
	}

	require.NoError(t, s.MarkRead(id, true))
	it, err := s.GetItem(id)
	require.NoError(t, err)
	if !it.IsRead {
		t.Fatal("expected read")
	}

	require.NoError(t, s.MarkStarred(id, true))
	it, _ = s.GetItem(id)
	if !it.IsStarred {
		t.Fatal("expected starred")
	}

	require.NoError(t, s.DeleteItem(id))
	if _, err := s.GetItem(id); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestFiltersCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateFilter(feed.Filter{
		SortOrder:   1,
		Query:       "kind:rss AND tag:morning",
		Action:      "download",
		ActionValue: "",
	})
	require.NoError(t, err)
	fs, err := s.ListFilters()
	require.NoError(t, err)
	if len(fs) != 1 || fs[0].ID != id {
		t.Fatalf("got %+v", fs)
	}

	fs[0].Action = "skip"
	require.NoError(t, s.UpdateFilter(fs[0]))
	fs, _ = s.ListFilters()
	if fs[0].Action != "skip" {
		t.Fatalf("update did not apply: %+v", fs)
	}
	require.NoError(t, s.DeleteFilter(id))
}

func TestDownloadsAndPlayback(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u1"})
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, err)

	require.NoError(t, s.EnqueueDownload(id))
	dls, err := s.PendingDownloads(10)
	require.NoError(t, err)
	if len(dls) != 1 || dls[0].ItemID != id {
		t.Fatalf("got %+v", dls)
	}
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/path/file.mp3", 12345))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	if d.State != "ready" || d.LocalPath != "/path/file.mp3" || d.Bytes != 12345 {
		t.Fatalf("got %+v", d)
	}

	require.NoError(t, s.SetCleanAudioPath(id, "/path/clean.mp3"))
	require.NoError(t, s.SetCaptionsPath(id, "/path/captions.vtt"))
	require.NoError(t, s.EnqueueProfanity(id))
	require.NoError(t, s.EnqueueTranscribe(id))

	d, _ = s.GetDownload(id)
	if d.CleanAudioPath == "" || d.CaptionsPath == "" {
		t.Fatalf("paths not set: %+v", d)
	}

	pending, err := s.PendingProfanity(10)
	require.NoError(t, err)
	if len(pending) != 1 {
		t.Fatalf("want 1 profanity pending, got %d", len(pending))
	}
	pendingT, err := s.PendingTranscribe(10)
	require.NoError(t, err)
	if len(pendingT) != 1 {
		t.Fatalf("want 1 transcribe pending, got %d", len(pendingT))
	}

	require.NoError(t, s.ClearNeedsProfanity(id))
	require.NoError(t, s.ClearNeedsTranscribe(id))
	pending, _ = s.PendingProfanity(10)
	if len(pending) != 0 {
		t.Fatal("profanity not cleared")
	}

	require.NoError(t, s.MarkDownloadError(id, "boom"))
	d, _ = s.GetDownload(id)
	if d.State != "error" || d.Error == "" {
		t.Fatalf("got %+v", d)
	}

	require.NoError(t, s.SetPlayback(id, 42.5, false))
	pb, err := s.GetPlayback(id)
	require.NoError(t, err)
	if pb.ProgressSec != 42.5 || pb.Completed {
		t.Fatalf("got %+v", pb)
	}
}

func TestPlaylistsCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreatePlaylist("Morning", "type:audio AND tag:morning")
	require.NoError(t, err)
	pls, err := s.ListPlaylists()
	require.NoError(t, err)
	if len(pls) != 1 || pls[0].ID != id {
		t.Fatalf("got %+v", pls)
	}
	pls[0].Name = "Morning Edition"
	require.NoError(t, s.UpdatePlaylist(pls[0]))
	pls, _ = s.ListPlaylists()
	if pls[0].Name != "Morning Edition" {
		t.Fatal("update did not apply")
	}
	require.NoError(t, s.DeletePlaylist(id))
}

func TestListItemsBySubscription(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://y/feed.xml"})
	for i := range 3 {
		_, _, err := s.UpsertItem(feed.Item{
			SubscriptionID: subID,
			GUID:           fmt.Sprintf("g%d", i),
			Type:           "text",
			Title:          fmt.Sprintf("t%d", i),
			PublishedAt:    int64(1000 + i),
		})
		require.NoError(t, err)
	}
	items, err := s.ListItemsBySubscription(subID, 10)
	require.NoError(t, err)
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
}

func TestPendingTranscribeReady(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})

	// Queued — should not appear.
	id1, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio"})
	require.NoError(t, s.EnqueueTranscribe(id1))

	// Ready — should appear.
	id2, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id2))
	require.NoError(t, s.MarkDownloadStart(id2))
	require.NoError(t, s.MarkDownloadReady(id2, "/x", 1))
	require.NoError(t, s.EnqueueTranscribe(id2))

	pending, err := s.PendingTranscribeReady(10)
	require.NoError(t, err)
	if len(pending) != 1 || pending[0] != id2 {
		t.Fatalf("got %v want [%d]", pending, id2)
	}
}

func TestPendingProfanityReady(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})

	// Item 1: queued + needs_profanity. Should NOT appear.
	id1, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio"})
	require.NoError(t, s.EnqueueProfanity(id1))

	// Item 2: ready + needs_profanity. Should appear.
	id2, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id2))
	require.NoError(t, s.MarkDownloadStart(id2))
	require.NoError(t, s.MarkDownloadReady(id2, "/x", 1))
	require.NoError(t, s.EnqueueProfanity(id2))

	pending, err := s.PendingProfanityReady(10)
	require.NoError(t, err)
	if len(pending) != 1 || pending[0] != id2 {
		t.Fatalf("got %v want [%d]", pending, id2)
	}
}

func TestPendingProfanityWithCaptions(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})

	// Ready but no captions — should NOT appear.
	id1, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id1))
	require.NoError(t, s.MarkDownloadStart(id1))
	require.NoError(t, s.MarkDownloadReady(id1, "/x", 1))
	require.NoError(t, s.EnqueueProfanity(id1))

	// Ready + has captions — should appear.
	id2, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id2))
	require.NoError(t, s.MarkDownloadStart(id2))
	require.NoError(t, s.MarkDownloadReady(id2, "/x", 1))
	require.NoError(t, s.SetCaptionsPath(id2, "/captions/g2.vtt"))
	require.NoError(t, s.EnqueueProfanity(id2))

	pending, err := s.PendingProfanityWithCaptions(10)
	require.NoError(t, err)
	if len(pending) != 1 || pending[0] != id2 {
		t.Fatalf("got %v want [%d]", pending, id2)
	}
}

func TestPlayerPrefsRoundTrip(t *testing.T) {
	s := newStore(t)

	// Defaults when unset.
	got, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	if got.Speed != 1.0 {
		t.Errorf("Speed: got %v want 1.0", got.Speed)
	}
	if got.SkipBackSec != 15 {
		t.Errorf("SkipBackSec: got %v want 15", got.SkipBackSec)
	}
	if got.SkipFwdSec != 30 {
		t.Errorf("SkipFwdSec: got %v want 30", got.SkipFwdSec)
	}
	if got.Volume != 1.0 {
		t.Errorf("Volume: got %v want 1.0", got.Volume)
	}
	if !got.AutoplayNext {
		t.Errorf("AutoplayNext: got false want true")
	}
	if got.SkipSilence {
		t.Errorf("SkipSilence: got true want false")
	}

	// Persist and read back.
	want := feed.PlayerPrefs{Speed: 1.5, SkipBackSec: 10, SkipFwdSec: 45, Volume: 0.8, AutoplayNext: false, SkipSilence: true}
	require.NoError(t, s.SavePlayerPrefs(want))
	got, err = s.LoadPlayerPrefs()
	require.NoError(t, err)
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestSilenceRangesRoundTrip(t *testing.T) {
	s := newStore(t)

	subID, err := s.CreateSubscription(feed.Subscription{URL: "http://x", Kind: "rss", Title: "X"})
	require.NoError(t, err)
	itemID, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "audio", Title: "Ep1", MediaURL: "http://x/1.mp3"})
	require.NoError(t, err)
	require.NoError(t, s.EnqueueDownload(itemID))
	require.NoError(t, s.MarkDownloadReady(itemID, "/tmp/1.mp3", 0))

	// Before computing: state is "unknown" (empty), no ranges.
	d, err := s.GetDownload(itemID)
	require.NoError(t, err)
	if d.SilenceState != "" {
		t.Fatalf("SilenceState = %q, want empty", d.SilenceState)
	}

	want := []feed.Range{{Start: 1.0, End: 2.5, Action: "skip"}, {Start: 10, End: 11, Action: "skip"}}
	require.NoError(t, s.SetSilenceRanges(itemID, want))

	d, err = s.GetDownload(itemID)
	require.NoError(t, err)
	if d.SilenceState != "done" {
		t.Fatalf("SilenceState = %q, want done", d.SilenceState)
	}

	got, err := s.GetSilenceRanges(itemID)
	require.NoError(t, err)
	if len(got) != len(want) {
		t.Fatalf("got %d ranges, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("range %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
