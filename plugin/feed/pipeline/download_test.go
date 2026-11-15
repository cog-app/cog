package pipeline_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

func newPipeline(t *testing.T) (*feed.Store, feed.Space) {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	space, err := p.(*feed.Plugin).Space(u.User.Username)
	require.NoError(t, err)
	return feed.NewStore(db), space
}

// readStored returns the contents of a file a downloads row names.
func readStored(t *testing.T, space feed.Space, name string) string {
	t.Helper()
	f, err := space.Own.Open(feed.Name(name))
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}

func TestDownloadHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("AAAAAAAA"))
	}))
	defer srv.Close()

	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "audio",
		MediaURL: srv.URL, MediaMIME: "audio/mpeg",
	})
	require.NoError(t, s.EnqueueDownload(id))

	dl := pipeline.NewDownloader(s, space)
	require.NoError(t, dl.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q", d.State)
	}
	if got := readStored(t, space, d.LocalPath); got != "AAAAAAAA" {
		t.Fatalf("body %q", got)
	}
	if !strings.HasPrefix(d.LocalPath, "media/") {
		t.Errorf("local_path is not a media name: %s", d.LocalPath)
	}
	if d.Bytes != 8 {
		t.Errorf("bytes: %d", d.Bytes)
	}
	entries, err := space.Cache.ReadDir("downloads")
	require.NoError(t, err)
	require.Equal(t, 0, len(entries), "download left in the cache")

	// Collect keeps what a row names, and removes what none does.
	_, err = space.Own.Put("media/orphan.mp3", strings.NewReader("x"))
	require.NoError(t, err)
	require.NoError(t, pipeline.Collect(s, space))
	_, err = space.Own.Stat("media/orphan.mp3")
	require.True(t, errors.Is(err, fs.ErrNotExist), "orphan kept: %v", err)
	require.Equal(t, "AAAAAAAA", readStored(t, space, d.LocalPath))
	require.NoError(t, s.DeleteItem(id))
	require.NoError(t, pipeline.Collect(s, space))
	_, err = space.Own.Stat(d.LocalPath)
	require.True(t, errors.Is(err, fs.ErrNotExist), "deleted item's media kept: %v", err)
}

func TestDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "audio", MediaURL: srv.URL,
	})
	require.NoError(t, s.EnqueueDownload(id))

	dl := pipeline.NewDownloader(s, space)
	_ = dl.RunOnce(context.Background())

	d, _ := s.GetDownload(id)
	if d.State != "error" {
		t.Fatalf("state %q", d.State)
	}
	if d.Error == "" {
		t.Errorf("expected error string")
	}
}

func TestDownloadYouTube(t *testing.T) {
	abs, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://yt/channel"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "vid1", Type: "video",
		MediaURL: "https://www.youtube.com/watch?v=vid1",
	})
	require.NoError(t, s.EnqueueDownload(id))

	dl := pipeline.NewDownloader(s, space)
	require.NoError(t, dl.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q error=%q", d.State, d.Error)
	}
	if _, err := space.Own.Stat(d.LocalPath); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	// YouTube serves video and audio apart: the download merges H.264 and
	// AAC into an mp4, which phones and browsers play.
	if !strings.HasSuffix(d.LocalPath, ".mp4") {
		t.Errorf("local path %q, want an .mp4", d.LocalPath)
	}
	args := ytdlpArgs(t, space, d.LocalPath)
	for _, want := range []string{"--merge-output-format mp4", "bv*[height<=1080][vcodec^=avc1]+ba[ext=m4a]"} {
		if !strings.Contains(args, want) {
			t.Errorf("yt-dlp args %q lack %q", args, want)
		}
	}
}

// An audio-only YouTube item downloads its m4a audio.
func TestDownloadYouTubeAudio(t *testing.T) {
	abs, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	s, space := newPipeline(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://yt/channel"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "vid1", Type: "audio",
		MediaURL: "https://www.youtube.com/watch?v=vid1",
	})
	require.NoError(t, s.EnqueueDownload(id))
	require.NoError(t, pipeline.NewDownloader(s, space).RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q error=%q", d.State, d.Error)
	}
	if !strings.HasSuffix(d.LocalPath, ".m4a") {
		t.Errorf("local path %q, want an .m4a", d.LocalPath)
	}
	if args := ytdlpArgs(t, space, d.LocalPath); !strings.Contains(args, "ba[ext=m4a]/ba") || strings.Contains(args, "merge-output-format") {
		t.Errorf("yt-dlp args %q, want the m4a audio alone", args)
	}
}

// ytdlpArgs reads the arguments the stub yt-dlp wrote into its download.
func ytdlpArgs(t *testing.T, space feed.Space, name string) string {
	t.Helper()
	f, err := space.Own.Open(feed.Name(name))
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}
