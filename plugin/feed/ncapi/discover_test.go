package ncapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// allowLoopback lets discovery reach an httptest server for one test.
func allowLoopback(t *testing.T) {
	safehttp.SetAllowPrivate(true)
	t.Cleanup(func() { safehttp.SetAllowPrivate(false) })
}

// Discovery must not reach the host's own network, however the URL got past
// validation.
func TestDiscoverFeedURL_blocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("discovery reached a loopback server")
	}))
	defer srv.Close()

	if _, err := DiscoverFeedURL(context.Background(), srv.URL+"/"); err == nil {
		t.Fatal("DiscoverFeedURL succeeded against loopback")
	}
	if got := sniffContentType(context.Background(), srv.URL+"/"); got != "" {
		t.Fatalf("sniffContentType = %q against loopback, want \"\"", got)
	}
}

func TestDiscoverFeedURL(t *testing.T) {
	allowLoopback(t)
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
	allowLoopback(t)
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
	allowLoopback(t)
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
