package media

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

// fetchPodcast fetches an RSS podcast feed and upserts episodes.
func fetchPodcast(client *http.Client, db *sql.DB, src *Source, clock func() time.Time) error {
	req, err := http.NewRequest("GET", src.URL, nil)
	if err != nil {
		return RecordSourceError(db, src, err, clock().Unix())
	}
	req.Header.Set("User-Agent", "Cog/1.0 (Media Queue)")
	if src.ETag != "" {
		req.Header.Set("If-None-Match", src.ETag)
	}
	if src.LastModified != "" {
		req.Header.Set("If-Modified-Since", src.LastModified)
	}

	resp, err := client.Do(req)
	if err != nil {
		return RecordSourceError(db, src, err, clock().Unix())
	}
	defer resp.Body.Close()

	now := clock().Unix()

	if resp.StatusCode == http.StatusNotModified {
		return RecordSourceSuccess(db, src, src.ETag, src.LastModified, now)
	}
	if resp.StatusCode != http.StatusOK {
		return RecordSourceError(db, src, fmt.Errorf("HTTP %d", resp.StatusCode), now)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return RecordSourceError(db, src, err, now)
	}

	parser := gofeed.NewParser()
	feed, err := parser.ParseString(string(body))
	if err != nil {
		return RecordSourceError(db, src, err, now)
	}

	// Update source metadata.
	if src.Title == "" && feed.Title != "" {
		UpdateSourceMeta(db, src.ID, feed.Title, feed.Description, feedImageURL(feed))
	}

	// Upsert episodes.
	for _, item := range feed.Items {
		guid := item.GUID
		if guid == "" {
			guid = item.Link
		}
		if guid == "" {
			continue
		}

		ep := episodeFromFeedItem(item, src.ID, guid, now)
		id, err := UpsertEpisode(db, ep)
		if err != nil {
			return fmt.Errorf("upsert episode %q: %w", guid, err)
		}
		if id > 0 {
			AutoTagEpisode(db, id, ep.MediaType)
			if !ApplyFilters(db, src.Title, id, ep) {
				QueueIfNew(db, id)
			}
		}
	}

	etag := resp.Header.Get("ETag")
	lastMod := resp.Header.Get("Last-Modified")
	return RecordSourceSuccess(db, src, etag, lastMod, now)
}

func episodeFromFeedItem(item *gofeed.Item, sourceID int64, guid string, now int64) *Episode {
	ep := &Episode{
		SourceID:  sourceID,
		GUID:      guid,
		Title:     strings.TrimSpace(item.Title),
		MediaType: MediaAudio,
	}

	if item.Description != "" {
		ep.Description = item.Description
	}
	if item.Content != "" && len(item.Content) > len(ep.Description) {
		ep.Description = item.Content
	}

	// Extract media URL and type from enclosures.
	for _, enc := range item.Enclosures {
		if enc.URL == "" {
			continue
		}
		ep.MediaURL = enc.URL
		if strings.HasPrefix(enc.Type, "video/") {
			ep.MediaType = MediaVideo
		}
		break
	}

	// Fallback: use link if no enclosure.
	if ep.MediaURL == "" && item.Link != "" {
		ep.MediaURL = item.Link
	}

	// Extract duration from iTunes extension.
	if item.ITunesExt != nil && item.ITunesExt.Duration != "" {
		ep.DurationSec = parseDuration(item.ITunesExt.Duration)
	}

	// Extract thumbnail.
	if item.Image != nil && item.Image.URL != "" {
		ep.ThumbnailURL = item.Image.URL
	} else if item.ITunesExt != nil && item.ITunesExt.Image != "" {
		ep.ThumbnailURL = item.ITunesExt.Image
	}

	// Published date.
	if item.PublishedParsed != nil {
		ep.PublishedAt = item.PublishedParsed.Unix()
	} else if item.UpdatedParsed != nil {
		ep.PublishedAt = item.UpdatedParsed.Unix()
	} else {
		ep.PublishedAt = now
	}

	return ep
}

// parseDuration parses iTunes duration which can be "HH:MM:SS", "MM:SS", or just seconds.
func parseDuration(s string) int {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		h, _ := strconv.Atoi(parts[0])
		m, _ := strconv.Atoi(parts[1])
		sec, _ := strconv.Atoi(parts[2])
		return h*3600 + m*60 + sec
	case 2:
		m, _ := strconv.Atoi(parts[0])
		sec, _ := strconv.Atoi(parts[1])
		return m*60 + sec
	default:
		sec, _ := strconv.Atoi(s)
		return sec
	}
}

func feedImageURL(feed *gofeed.Feed) string {
	if feed.Image != nil && feed.Image.URL != "" {
		return feed.Image.URL
	}
	if feed.ITunesExt != nil && feed.ITunesExt.Image != "" {
		return feed.ITunesExt.Image
	}
	return ""
}
