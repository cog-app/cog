package feed

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DetectKind guesses the subscription kind from a URL.
//
// Order:
//  1. YouTube hostnames -> "youtube".
//  2. HEAD/GET sniff: an XML/RSS/Atom/JSONFeed content-type -> "rss".
//  3. Anything else -> "url" (poll the page for content changes).
//
// Network errors fall back to "url" — the URL kind is the safest default
// because it just polls the page and detects body changes; the user can
// always change the kind manually later.
func DetectKind(ctx context.Context, rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "url"
	}
	host := strings.ToLower(u.Hostname())
	if host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be" {
		return "youtube"
	}

	// Fall through to a content sniff. HEAD first (cheap); if the server
	// disallows HEAD or returns no content-type, do a small GET.
	if kind := sniffKind(ctx, http.MethodHead, rawURL); kind != "" {
		return kind
	}
	if kind := sniffKind(ctx, http.MethodGet, rawURL); kind != "" {
		return kind
	}
	return "url"
}

func sniffKind(ctx context.Context, method, rawURL string) string {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, method, rawURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "cog-feed/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct == "" {
		return ""
	}
	switch {
	case strings.Contains(ct, "application/rss+xml"),
		strings.Contains(ct, "application/atom+xml"),
		strings.Contains(ct, "application/feed+json"),
		strings.Contains(ct, "application/xml"),
		strings.Contains(ct, "text/xml"):
		return "rss"
	case strings.Contains(ct, "text/html"),
		strings.Contains(ct, "application/xhtml"):
		return "url"
	}
	return ""
}
