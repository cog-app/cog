package pipeline_test

import (
	"context"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

func TestTranscribeStubClearsFlag(t *testing.T) {
	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "/tmp/x.mp3", 1))
	require.NoError(t, s.EnqueueTranscribe(id))

	w := pipeline.NewTranscribe(s)
	require.NoError(t, w.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.NeedsTranscribe {
		t.Error("needs_transcribe not cleared")
	}
	if d.CaptionsPath != "" {
		t.Errorf("stub should leave captions_path empty: %q", d.CaptionsPath)
	}
}
