package ncapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverFeedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/feed.xml":
			w.Header().Set("Content-Type", "application/rss+xml")
			w.WriteHeader(http.StatusOK)
		default:
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`<!DOCTYPE html><html><head>
<link rel="alternate" type="application/rss+xml" href="/feed.xml" title="RSS Feed"/>
</head><body></body></html>`))
		}
	}))
	defer srv.Close()

	got, err := DiscoverFeedURL(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("DiscoverFeedURL error: %v", err)
	}
	want := srv.URL + "/feed.xml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDiscoverFeedURL_atom_preferred(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html><html><head>
<link rel="alternate" type="application/rss+xml" href="/rss.xml"/>
<link rel="alternate" type="application/atom+xml" href="/atom.xml"/>
</head><body></body></html>`))
	}))
	defer srv.Close()

	got, err := DiscoverFeedURL(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("DiscoverFeedURL error: %v", err)
	}
	want := srv.URL + "/atom.xml"
	if got != want {
		t.Errorf("got %q (want atom preferred %q)", got, want)
	}
}

func TestDiscoverFeedURL_none(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html><html><head><title>No feeds here</title></head><body></body></html>`))
	}))
	defer srv.Close()

	got, err := DiscoverFeedURL(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("DiscoverFeedURL error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty result, got %q", got)
	}
}
