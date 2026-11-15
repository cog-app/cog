package feed_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// A failed resolve says what yt-dlp said, not only its exit status.
func TestResolveYouTubeStreamURLSaysWhy(t *testing.T) {
	dir := t.TempDir()
	stub := "#!/bin/sh\necho 'ERROR: [youtube] abc: Requested format is not available' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(stub), 0o755))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	_, err := feed.ResolveYouTubeStreamURL(context.Background(), "https://www.youtube.com/watch?v=abc", false)
	if err == nil || !strings.Contains(err.Error(), "Requested format is not available") {
		t.Fatalf("err = %v, want yt-dlp's message", err)
	}
}
