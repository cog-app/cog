package media

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/mmcdole/gofeed"
)

// youtubeHosts are hostnames that indicate a YouTube URL.
var youtubeHosts = map[string]bool{
	"youtube.com":     true,
	"www.youtube.com": true,
	"m.youtube.com":   true,
	"youtu.be":        true,
}

var feedLinkTypes = map[string]bool{
	"application/rss+xml":   true,
	"application/atom+xml":  true,
	"application/feed+json": true,
}

// DetectSourceKind determines whether a URL is a podcast RSS feed,
// YouTube channel/playlist, or a generic URL. For HTML pages, it
// discovers feed links via <link rel="alternate"> and follows them.
func DetectSourceKind(client *http.Client, rawURL string) (SourceKind, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	if youtubeHosts[host] {
		return SourceYouTube, rawURL, nil
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return SourceURL, rawURL, nil
	}
	req.Header.Set("User-Agent", "Cog/1.0 (Media Queue)")
	resp, err := client.Do(req)
	if err != nil {
		return SourceURL, rawURL, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return SourceURL, rawURL, nil
	}

	// Try parsing directly as a feed.
	parser := gofeed.NewParser()
	if feed, err := parser.ParseString(string(body)); err == nil {
		return classifyFeed(feed), rawURL, nil
	}

	// Not a feed — if it's HTML, look for <link rel="alternate"> feed links.
	ct := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(ct)
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return SourceURL, rawURL, nil
	}

	feedURL := discoverFeedLink(string(body), u)
	if feedURL == "" {
		return SourceURL, rawURL, nil
	}

	// Fetch the discovered feed to confirm it's valid.
	kind, err := fetchAndClassify(client, feedURL)
	if err != nil {
		return SourceURL, rawURL, nil
	}
	return kind, feedURL, nil
}

// discoverFeedLink extracts the first feed URL from HTML <link rel="alternate"> tags.
func discoverFeedLink(html string, base *url.URL) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return ""
	}

	var feedURL string
	doc.Find(`link[rel="alternate"]`).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		typ, _ := s.Attr("type")
		if !feedLinkTypes[typ] {
			return true
		}
		href, exists := s.Attr("href")
		if !exists || href == "" {
			return true
		}
		ref, err := url.Parse(href)
		if err != nil {
			return true
		}
		feedURL = base.ResolveReference(ref).String()
		return false
	})
	return feedURL
}

// fetchAndClassify fetches a URL and classifies the feed.
func fetchAndClassify(client *http.Client, feedURL string) (SourceKind, error) {
	req, err := http.NewRequest("GET", feedURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Cog/1.0 (Media Queue)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	parser := gofeed.NewParser()
	feed, err := parser.ParseString(string(body))
	if err != nil {
		return "", fmt.Errorf("not a valid feed")
	}
	return classifyFeed(feed), nil
}

// classifyFeed returns SourcePodcast for any valid feed (with or without enclosures).
func classifyFeed(feed *gofeed.Feed) SourceKind {
	return SourcePodcast
}
