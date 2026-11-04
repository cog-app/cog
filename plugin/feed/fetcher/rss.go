package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

var sanitizer = bluemonday.UGCPolicy()

// fetchRSS fetches an RSS/Atom feed and returns parsed items.
func fetchRSS(ctx context.Context, sub feed.Subscription) (feed.FetchResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return feed.FetchResult{}, fmt.Errorf("rss fetch new request: %w", err)
	}
	req.Header.Set("User-Agent", "Cog/1.0 (Feed Reader)")
	if sub.ETag != "" {
		req.Header.Set("If-None-Match", sub.ETag)
	}
	if sub.LastModified != "" {
		req.Header.Set("If-Modified-Since", sub.LastModified)
	}

	resp, err := feed.HTTP.Do(req)
	if err != nil {
		return feed.FetchResult{}, fmt.Errorf("rss fetch do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return feed.FetchResult{
			NotModified:  true,
			ETag:         sub.ETag,
			LastModified: sub.LastModified,
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return feed.FetchResult{}, fmt.Errorf("rss fetch HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return feed.FetchResult{}, fmt.Errorf("rss fetch read body: %w", err)
	}

	parser := gofeed.NewParser()
	parsed, err := parser.ParseString(string(body))
	if err != nil {
		return feed.FetchResult{}, fmt.Errorf("rss parse: %w", err)
	}

	etag := resp.Header.Get("ETag")
	lastMod := resp.Header.Get("Last-Modified")

	// Collect subscription metadata updates.
	updates := make(map[string]string)
	if parsed.Title != "" {
		updates["title"] = parsed.Title
	}
	if parsed.Description != "" {
		updates["description"] = parsed.Description
	}
	if parsed.Link != "" {
		updates["site_url"] = parsed.Link
	}
	if parsed.Image != nil && parsed.Image.URL != "" {
		updates["image_url"] = parsed.Image.URL
	}

	// Derive favicon URL from the site URL when not already set.
	if siteURL := updates["site_url"]; siteURL != "" {
		if u, err := url.Parse(siteURL); err == nil && u.Host != "" {
			updates["favicon_url"] = (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/favicon.ico"}).String()
		}
	}

	var items []feed.Item
	for _, entry := range parsed.Items {
		entryGUID := entry.GUID
		if entryGUID == "" {
			entryGUID = entry.Link
		}

		var publishedAt int64
		if entry.PublishedParsed != nil {
			publishedAt = entry.PublishedParsed.Unix()
		} else if entry.UpdatedParsed != nil {
			publishedAt = entry.UpdatedParsed.Unix()
		}

		author := ""
		if entry.Author != nil {
			author = entry.Author.Name
		}

		title := strings.TrimSpace(entry.Title)
		itemURL := strings.TrimSpace(entry.Link)

		// Text item: produce one if there is body content.
		content := entry.Content
		if content == "" {
			content = entry.Description
		}
		if content != "" {
			sanitized := sanitizer.Sanitize(content)
			items = append(items, feed.Item{
				GUID:        entryGUID,
				Type:        "text",
				Title:       title,
				Author:      author,
				URL:         itemURL,
				ContentHTML: sanitized,
				PublishedAt: publishedAt,
			})
		}

		// Media items: one per enclosure.
		for _, enc := range entry.Enclosures {
			if enc.URL == "" {
				continue
			}
			mediaType := enclosureMediaType(enc.Type)
			guid := enclosureGUID(entryGUID, enc.URL)
			items = append(items, feed.Item{
				GUID:        guid,
				Type:        mediaType,
				Title:       title,
				Author:      author,
				URL:         itemURL,
				MediaURL:    enc.URL,
				MediaMIME:   enc.Type,
				PublishedAt: publishedAt,
			})
		}
	}

	return feed.FetchResult{
		Items:               items,
		ETag:                etag,
		LastModified:        lastMod,
		SubscriptionUpdates: updates,
	}, nil
}

// enclosureMediaType maps a MIME type to a feed item type ("audio", "video", or "audio" as default).
func enclosureMediaType(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	default:
		return "audio"
	}
}

// enclosureGUID produces a stable GUID for an enclosure by appending a hash of the URL.
func enclosureGUID(entryGUID, encURL string) string {
	h := sha256.Sum256([]byte(encURL))
	return entryGUID + "#" + hex.EncodeToString(h[:6])
}
