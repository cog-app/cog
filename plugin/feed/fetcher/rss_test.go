package fetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

func TestRSSTextOnly(t *testing.T) {
	body, err := os.ReadFile("testdata/sample-text.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	sub := feed.Subscription{Kind: "rss", URL: srv.URL}
	out, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out.Items) != 2 {
		t.Fatalf("want 2 text items, got %d", len(out.Items))
	}
	for _, it := range out.Items {
		if it.Type != "text" {
			t.Errorf("want type=text, got %s", it.Type)
		}
		if it.ContentHTML == "" {
			t.Errorf("want content_html populated")
		}
	}
}

func TestRSSWithAudioEnclosure(t *testing.T) {
	body, err := os.ReadFile("testdata/sample-enclosure.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{Kind: "rss", URL: srv.URL})
	require.NoError(t, err)

	// An episode is one item: the enclosure, carrying the show notes when the
	// entry has any. No separate text item.
	if len(out.Items) != 2 {
		t.Fatalf("want 2 items, got %d: %+v", len(out.Items), out.Items)
	}
	for _, it := range out.Items {
		if it.Type != "audio" || it.MediaMIME != "audio/mpeg" || it.MediaURL == "" {
			t.Errorf("%s: want an audio/mpeg episode, got type %q mime %q url %q", it.Title, it.Type, it.MediaMIME, it.MediaURL)
		}
	}
	// The episode is its entry, by the entry's GUID.
	if got := out.Items[0].GUID; got != "https://example.com/podcast/ep1" {
		t.Errorf("episode one GUID: got %q", got)
	}
	if got := out.Items[0].ContentHTML; !strings.Contains(got, "Episode one show notes.") {
		t.Errorf("episode one notes: got %q", got)
	}
	if got := out.Items[1].ContentHTML; got != "" {
		t.Errorf("episode two has no notes, got %q", got)
	}
}

// Only the first of an entry's enclosures is the entry, with its GUID and
// body; a further one is named by its URL's hash.
func TestRSSNotesOnFirstEnclosure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>x</title>
			<item><title>Ep</title><guid>ep</guid><description>notes</description>
			<enclosure url="https://example.com/ep.mp3" type="audio/mpeg"/>
			<enclosure url="https://example.com/ep.mp4" type="video/mp4"/></item>
			</channel></rss>`))
	}))
	defer srv.Close()

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{Kind: "rss", URL: srv.URL})
	require.NoError(t, err)
	if len(out.Items) != 2 {
		t.Fatalf("want 2 items, got %+v", out.Items)
	}
	if out.Items[0].Type != "audio" || out.Items[0].ContentHTML != "notes" || out.Items[0].GUID != "ep" {
		t.Errorf("first enclosure: %+v", out.Items[0])
	}
	if out.Items[1].Type != "video" || out.Items[1].ContentHTML != "" || !strings.HasPrefix(out.Items[1].GUID, "ep#") {
		t.Errorf("second enclosure: %+v", out.Items[1])
	}
}

func TestRSSEtag304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == "etag-1" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", "etag-1")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>x</title></channel></rss>`))
	}))
	defer srv.Close()

	sub := feed.Subscription{Kind: "rss", URL: srv.URL, ETag: "etag-1"}
	out, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if !out.NotModified {
		t.Fatal("expected NotModified")
	}
	if len(out.Items) != 0 {
		t.Fatalf("expected 0 items on 304, got %d", len(out.Items))
	}
}

func TestRSSThumbnailsAndDurations(t *testing.T) {
	body, err := os.ReadFile("testdata/sample-images.xml")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{Kind: "rss", URL: srv.URL})
	require.NoError(t, err)
	require.Equal(t, 4, len(out.Items))

	for i, want := range []struct {
		title, thumb string
		dur          int64
	}{
		{"Episode With Art", "https://example.com/art.jpg", 3725},
		{"Episode With Art", "https://example.com/art.jpg", 3725},
		{"Bare Episode", "", 0},
		{"Article", "https://example.com/posts/pic.png", 0},
	} {
		it := out.Items[i]
		if it.Title != want.title || it.ThumbnailURL != want.thumb || it.DurationSec != want.dur {
			t.Errorf("item %d (%s): thumbnail %q duration %d, want %q %d", i, it.Title, it.ThumbnailURL, it.DurationSec, want.thumb, want.dur)
		}
	}
}
