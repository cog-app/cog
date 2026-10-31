package ncapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// client fetches the URLs users subscribe to. It dials only public
// addresses, checked after resolution, so a redirect or a rebinding DNS
// name cannot point discovery at the host's own network.
var client = safehttp.Client(10 * time.Second)

// DiscoverFeedURL attempts to find a feed URL on an HTML page by scanning
// <link rel="alternate" type="application/rss+xml|atom+xml"> elements.
// Returns ("", nil) if no feed link is present.
func DiscoverFeedURL(ctx context.Context, pageURL string) (string, error) {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "cog-feed/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}

	base, err := url.Parse(pageURL)
	if err != nil {
		return "", err
	}

	var atomHref, rssHref string
	doc.Find(`link[rel="alternate"]`).Each(func(_ int, s *goquery.Selection) {
		typ := strings.ToLower(s.AttrOr("type", ""))
		href := s.AttrOr("href", "")
		if href == "" {
			return
		}
		switch {
		case strings.Contains(typ, "application/atom+xml") && atomHref == "":
			atomHref = href
		case strings.Contains(typ, "application/rss+xml") && rssHref == "":
			rssHref = href
		}
	})

	// Prefer atom, fall back to rss.
	chosen := atomHref
	if chosen == "" {
		chosen = rssHref
	}
	if chosen == "" {
		return "", nil
	}

	// Resolve relative URLs against the page base.
	ref, err := url.Parse(chosen)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

// sniffContentType performs a HEAD (then GET fallback) to determine if a URL
// looks like a feed. Returns "feed" if it appears to be RSS/Atom/JSONFeed,
// "html" if it's an HTML page, or "" if undetermined.
func sniffContentType(ctx context.Context, rawURL string) string {
	for _, method := range []string{http.MethodHead, http.MethodGet} {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		req, err := http.NewRequestWithContext(rctx, method, rawURL, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", "cog-feed/1.0")
		resp, err := client.Do(req)
		cancel()
		if err != nil {
			continue
		}
		resp.Body.Close()
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if ct == "" {
			continue
		}
		switch {
		case strings.Contains(ct, "application/rss+xml"),
			strings.Contains(ct, "application/atom+xml"),
			strings.Contains(ct, "application/feed+json"),
			strings.Contains(ct, "application/xml"),
			strings.Contains(ct, "text/xml"):
			return "feed"
		case strings.Contains(ct, "text/html"),
			strings.Contains(ct, "application/xhtml"):
			return "html"
		}
	}
	return ""
}

// resolveToFeedURL takes a user-supplied URL and returns the best feed URL.
// If the URL already serves feed content, it is returned as-is. If it's an
// HTML page, HTML discovery is attempted. Returns an error if no feed is found.
func resolveToFeedURL(ctx context.Context, rawURL string) (string, error) {
	kind := sniffContentType(ctx, rawURL)
	if kind == "feed" {
		return rawURL, nil
	}
	// Attempt HTML discovery for HTML pages or undetermined content.
	feedURL, err := DiscoverFeedURL(ctx, rawURL)
	if err != nil {
		return "", fmt.Errorf("feed discovery failed: %w", err)
	}
	if feedURL == "" {
		return "", fmt.Errorf("no feed found at %s", rawURL)
	}
	return feedURL, nil
}
