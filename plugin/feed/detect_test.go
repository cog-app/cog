package feed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestDetectKindYouTube(t *testing.T) {
	cases := []string{
		"https://www.youtube.com/@example",
		"https://youtube.com/channel/UCabc",
		"https://m.youtube.com/playlist?list=PL1",
		"https://youtu.be/abc123",
	}
	for _, u := range cases {
		if got := feed.DetectKind(context.Background(), u); got != "youtube" {
			t.Errorf("%s -> %s want youtube", u, got)
		}
	}
}

func TestDetectKindRSSByContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	}))
	defer srv.Close()
	if got := feed.DetectKind(context.Background(), srv.URL); got != "rss" {
		t.Errorf("got %s want rss", got)
	}
}

func TestDetectKindAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
	}))
	defer srv.Close()
	if got := feed.DetectKind(context.Background(), srv.URL); got != "rss" {
		t.Errorf("got %s want rss", got)
	}
}

func TestDetectKindHTMLFallsBackToURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}))
	defer srv.Close()
	if got := feed.DetectKind(context.Background(), srv.URL); got != "url" {
		t.Errorf("got %s want url", got)
	}
}

func TestDetectKindUnreachableFallsBackToURL(t *testing.T) {
	// Use a port that's almost certainly not listening.
	got := feed.DetectKind(context.Background(), "http://127.0.0.1:1/nope")
	if got != "url" {
		t.Errorf("got %s want url", got)
	}
}
