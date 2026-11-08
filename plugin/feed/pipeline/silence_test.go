package pipeline_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

// A ready download's silence is analysed once, in the background, and the
// ranges kept on its row.
func TestSilenceRecordsSkipRanges(t *testing.T) {
	abs, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	s, space := newPipeline(t)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "audio"})
	require.NoError(t, err)
	_, err = space.Own.Put("media/in.mp3", strings.NewReader("dummy"))
	require.NoError(t, err)
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, s.MarkDownloadStart(id))
	require.NoError(t, s.MarkDownloadReady(id, "media/in.mp3", 5))

	pending, err := s.PendingSilence(10)
	require.NoError(t, err)
	assert.Equal(t, []int64{id}, pending)

	require.NoError(t, pipeline.NewSilence(s, space).RunOnce(context.Background()))

	ranges, err := s.GetSilenceRanges(id)
	require.NoError(t, err)
	assert.Equal(t, []feed.Range{{Start: 12.5, End: 15, Action: "skip"}}, ranges)
	pending, err = s.PendingSilence(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
}
