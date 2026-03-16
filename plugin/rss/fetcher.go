package rss

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/microcosm-cc/bluemonday"
	"github.com/mmcdole/gofeed"
)

var sanitizer = bluemonday.UGCPolicy()

var feedLinkTypes = map[string]bool{
	"application/rss+xml":   true,
	"application/atom+xml":  true,
	"application/feed+json": true,
}

// validateFeedURL checks that a URL is safe to fetch (no SSRF).
func validateFeedURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("empty hostname")
	}
	// Reject private/loopback IPs.
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("host %s resolves to private/loopback address %s", host, ipStr)
		}
	}
	return nil
}

// ResolveURL fetches the given URL, determines if it's a feed or HTML page,
// and returns the actual feed URL. If the URL is already a feed, it returns
// it unchanged. If it's HTML, it extracts <link rel="alternate"> feed URLs
// and returns the first valid one. Returns an error if no feed can be found.
func ResolveURL(client *http.Client, rawURL string) (string, error) {
	if err := validateFeedURL(rawURL); err != nil {
		return "", err
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Cog/1.0 (RSS Reader)")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	// Try parsing as a feed first.
	if _, err := gofeed.NewParser().ParseString(string(body)); err == nil {
		return rawURL, nil
	}

	// Not a feed — check if it's HTML.
	ct := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(ct)
	if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
		return "", errors.New("not a valid feed")
	}

	// Parse HTML and look for feed links.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return "", errors.New("not a valid feed")
	}

	base, _ := url.Parse(rawURL)

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

	if feedURL == "" {
		return "", errors.New("no feed found at URL")
	}
	if err := validateFeedURL(feedURL); err != nil {
		return "", err
	}
	return feedURL, nil
}

// fetchFeed fetches and processes a single feed, upserting items into the DB.
func fetchFeed(client *http.Client, db *sql.DB, feed *Feed, clock func() time.Time) error {
	if err := validateFeedURL(feed.URL); err != nil {
		return recordError(db, feed, err, clock)
	}
	req, err := http.NewRequest("GET", feed.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Cog/1.0 (RSS Reader)")
	if feed.ETag != "" {
		req.Header.Set("If-None-Match", feed.ETag)
	}
	if feed.LastModified != "" {
		req.Header.Set("If-Modified-Since", feed.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return recordError(db, feed, err, clock)
	}
	defer resp.Body.Close()

	now := clock().Unix()

	if resp.StatusCode == http.StatusNotModified {
		_, err := db.Exec(`UPDATE feeds SET last_checked_at = ?, last_success_at = ?,
			next_check_at = ?, error_count = 0, last_error = '' WHERE id = ?`,
			now, now, now+int64(feed.PollIntervalSec), feed.ID)
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return recordError(db, feed, fmt.Errorf("HTTP %d", resp.StatusCode), clock)
	}

	// Limit body to 10MB.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return recordError(db, feed, err, clock)
	}

	parser := gofeed.NewParser()
	parsed, err := parser.ParseString(string(body))
	if err != nil {
		return recordError(db, feed, err, clock)
	}

	// Update feed metadata.
	title := feed.Title
	if title == "" && parsed.Title != "" {
		title = parsed.Title
	}
	siteURL := feed.SiteURL
	if siteURL == "" && parsed.Link != "" {
		siteURL = parsed.Link
	}

	etag := resp.Header.Get("ETag")
	lastMod := resp.Header.Get("Last-Modified")

	_, err = db.Exec(`UPDATE feeds SET title = ?, site_url = ?, etag = ?, last_modified = ?,
		last_checked_at = ?, last_success_at = ?, next_check_at = ?,
		error_count = 0, last_error = '' WHERE id = ?`,
		title, siteURL, etag, lastMod, now, now, now+int64(feed.PollIntervalSec), feed.ID)
	if err != nil {
		return err
	}

	// Upsert items.
	for _, item := range parsed.Items {
		guid := item.GUID
		if guid == "" {
			guid = item.Link
		}
		if guid == "" {
			continue
		}

		content := item.Content
		if content == "" {
			content = item.Description
		}
		content = sanitizer.Sanitize(content)

		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))

		var publishedAt int64
		if item.PublishedParsed != nil {
			publishedAt = item.PublishedParsed.Unix()
		} else if item.UpdatedParsed != nil {
			publishedAt = item.UpdatedParsed.Unix()
		} else {
			publishedAt = now
		}

		author := ""
		if item.Author != nil {
			author = item.Author.Name
		}

		itemTitle := strings.TrimSpace(item.Title)
		itemURL := strings.TrimSpace(item.Link)

		_, err := db.Exec(`INSERT INTO items (feed_id, guid, title, author, url, content_html, published_at, content_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(feed_id, guid) DO UPDATE SET
				title = excluded.title,
				author = excluded.author,
				url = excluded.url,
				content_html = CASE WHEN excluded.content_hash != items.content_hash THEN excluded.content_html ELSE items.content_html END,
				content_hash = excluded.content_hash,
				published_at = excluded.published_at`,
			feed.ID, guid, itemTitle, author, itemURL, content, publishedAt, hash)
		if err != nil {
			return fmt.Errorf("upsert item %q: %w", guid, err)
		}
	}

	// Auto-mark-read: apply regex pattern if configured.
	if feed.AutoReadPattern != "" {
		if re, err := regexp.Compile(feed.AutoReadPattern); err == nil {
			autoMarkReadByRegex(db, feed.ID, re)
		}
	}

	return nil
}

// autoMarkReadByRegex marks unread items matching the regex as read.
// We do this in Go since SQLite's REGEXP requires a custom function.
func autoMarkReadByRegex(db *sql.DB, feedID int64, re *regexp.Regexp) {
	rows, err := db.Query(`SELECT id, title FROM items WHERE feed_id = ? AND is_read = 0`, feedID)
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			continue
		}
		if re.MatchString(title) {
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		MarkItemsRead(db, ids)
	}
}

func recordError(db *sql.DB, feed *Feed, fetchErr error, clock func() time.Time) error {
	now := clock().Unix()
	newCount := feed.ErrorCount + 1
	// Exponential backoff: min(1800 * 2^count, 86400)
	backoff := int64(1800)
	for i := 0; i < newCount && backoff < 86400; i++ {
		backoff *= 2
	}
	if backoff > 86400 {
		backoff = 86400
	}
	_, err := db.Exec(`UPDATE feeds SET last_checked_at = ?, error_count = ?, last_error = ?,
		next_check_at = ? WHERE id = ?`,
		now, newCount, fetchErr.Error(), now+backoff, feed.ID)
	if err != nil {
		return err
	}
	return fetchErr
}
