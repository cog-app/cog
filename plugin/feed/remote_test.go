package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestCheckURL(t *testing.T) {
	for _, ok := range []string{"https://example.com/feed.xml", "http://example.com"} {
		require.NoError(t, feed.CheckURL(ok))
	}
	// A string yt-dlp would read as an option, and URLs that are not the
	// web, are refused.
	for _, bad := range []string{"--exec=touch /tmp/x", "-o/tmp/x", "file:///etc/passwd", "ftp://example.com/", "https://", ""} {
		require.True(t, feed.CheckURL(bad) != nil, "%q accepted", bad)
	}
}
