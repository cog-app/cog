package fetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

	var textCount, audioCount int
	for _, it := range out.Items {
		switch it.Type {
		case "text":
			textCount++
			if it.ContentHTML == "" {
				t.Error("text item missing content")
			}
		case "audio":
			audioCount++
			if it.MediaURL == "" {
				t.Error("audio item missing media_url")
			}
			if it.MediaMIME != "audio/mpeg" {
				t.Errorf("audio mime: got %q", it.MediaMIME)
			}
		}
	}
	if textCount != 1 || audioCount != 1 {
		t.Fatalf("want 1 text + 1 audio, got %d text %d audio", textCount, audioCount)
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
