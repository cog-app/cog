package fetcher_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

func TestURLFetchEmitsItemOnFirstAndChange(t *testing.T) {
	var body atomic.Pointer[string]
	v1 := "<html><body>v1</body></html>"
	body.Store(&v1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(*body.Load()))
	}))
	defer srv.Close()

	sub := feed.Subscription{Kind: "url", URL: srv.URL}

	out1, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out1.Items) != 1 {
		t.Fatalf("want 1 item on first fetch, got %d", len(out1.Items))
	}
	hash1 := out1.Items[0].ContentHash
	if hash1 == "" {
		t.Fatal("expected content_hash on first fetch")
	}
	if out1.ETag != hash1 {
		t.Fatalf("expected ETag == ContentHash, got etag=%q hash=%q", out1.ETag, hash1)
	}

	sub.ETag = hash1
	out2, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if !out2.NotModified {
		t.Fatal("expected NotModified on unchanged poll")
	}
	if len(out2.Items) != 0 {
		t.Fatalf("expected no items on unchanged poll, got %d", len(out2.Items))
	}

	v2 := "<html><body>v2 NEW CONTENT</body></html>"
	body.Store(&v2)
	out3, err := fetcher.Fetch(context.Background(), sub)
	require.NoError(t, err)
	if len(out3.Items) != 1 {
		t.Fatalf("want 1 item on change, got %d", len(out3.Items))
	}
	if out3.Items[0].ContentHash == hash1 {
		t.Fatal("expected new hash after change")
	}
}

func TestURLFetchPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello <script>alert(1)</script>"))
	}))
	defer srv.Close()

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{Kind: "url", URL: srv.URL})
	require.NoError(t, err)
	if len(out.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(out.Items))
	}
	if out.Items[0].Type != "text" {
		t.Errorf("want text type, got %s", out.Items[0].Type)
	}
	// Plain text should be safely encoded (not raw-injected).
	if got := out.Items[0].ContentHTML; got == "" || got == "hello <script>alert(1)</script>" {
		t.Errorf("plain text not safely encoded: %q", got)
	}
}
