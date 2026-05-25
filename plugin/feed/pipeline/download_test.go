package pipeline_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

func newPipelineStore(t *testing.T) *feed.Store {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	return feed.NewStore(db)
}

func TestDownloadHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("AAAAAAAA"))
	}))
	defer srv.Close()

	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "audio",
		MediaURL: srv.URL, MediaMIME: "audio/mpeg",
	})
	require.NoError(t, s.EnqueueDownload(id))

	dir := t.TempDir()
	dl := pipeline.NewDownloader(s, dir)
	require.NoError(t, dl.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q", d.State)
	}
	b, err := os.ReadFile(d.LocalPath)
	require.NoError(t, err)
	if string(b) != "AAAAAAAA" {
		t.Fatalf("body %q", string(b))
	}
	if filepath.Dir(d.LocalPath) != dir {
		t.Errorf("local_path not under root: %s", d.LocalPath)
	}
	if d.Bytes != 8 {
		t.Errorf("bytes: %d", d.Bytes)
	}
}

func TestDownloadHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "audio", MediaURL: srv.URL,
	})
	require.NoError(t, s.EnqueueDownload(id))

	dl := pipeline.NewDownloader(s, t.TempDir())
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

	s := newPipelineStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://yt/channel"})
	id, _, _ := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "vid1", Type: "video",
		MediaURL: "https://www.youtube.com/watch?v=vid1",
	})
	require.NoError(t, s.EnqueueDownload(id))

	dl := pipeline.NewDownloader(s, t.TempDir())
	require.NoError(t, dl.RunOnce(context.Background()))

	d, _ := s.GetDownload(id)
	if d.State != "ready" {
		t.Fatalf("state %q error=%q", d.State, d.Error)
	}
	if _, err := os.Stat(d.LocalPath); err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
}
