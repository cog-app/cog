package feed

import (
	"database/sql"
	"fmt"
	"time"
)

// Seed creates sample feed data for a user: one folder, two subscriptions, and
// a mix of text/audio items (some read, some starred, one tagged) so the feed
// UI has something to render in development. No-op if items already exist.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed feed: %w", err)
	}
	store := NewStore(db)

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&count); err != nil {
		return fmt.Errorf("seed feed: count items: %w", err)
	}
	if count > 0 {
		return nil
	}

	folderID, err := store.CreateFolder("Tech")
	if err != nil {
		return fmt.Errorf("seed feed: folder: %w", err)
	}

	news, err := store.CreateSubscription(Subscription{
		Kind: "rss", URL: "https://news.example.com/feed.xml", Title: "Daily News",
		SiteURL: "https://news.example.com", FolderID: nullInt(folderID),
	})
	if err != nil {
		return fmt.Errorf("seed feed: subscription news: %w", err)
	}
	pod, err := store.CreateSubscription(Subscription{
		Kind: "rss", URL: "https://pod.example.com/feed.xml", Title: "The Podcast",
		SiteURL: "https://pod.example.com", FolderID: nullInt(folderID),
	})
	if err != nil {
		return fmt.Errorf("seed feed: subscription pod: %w", err)
	}

	now := time.Now()
	// Text items on the news subscription.
	textItems := []struct {
		guid, title, body string
		ageHours          int
	}{
		{"news-1", "Hello World: a deep dive", "<p>An article about hello world and deep dives.</p>", 1},
		{"news-2", "Quarterly Report", "<p>Numbers went up and to the right.</p>", 26},
		{"news-3", "Old Announcement", "<p>This happened a while ago.</p>", 24 * 40},
	}
	for _, ti := range textItems {
		id, _, err := store.UpsertItem(Item{
			SubscriptionID: news, GUID: ti.guid, Type: "text", Title: ti.title,
			Author: "Staff", ContentHTML: ti.body, URL: "https://news.example.com/" + ti.guid,
			PublishedAt: now.Add(-time.Duration(ti.ageHours) * time.Hour).Unix(),
		})
		if err != nil {
			return fmt.Errorf("seed feed: item %s: %w", ti.guid, err)
		}
		if ti.guid == "news-2" {
			if err := store.MarkRead(id, true); err != nil { // one already-read item
				return fmt.Errorf("seed feed: mark %s read: %w", ti.guid, err)
			}
		}
	}

	// Audio items on the podcast (give the player neighbors to navigate).
	audioItems := []struct {
		guid, title string
		dur         int64
		ageHours    int
	}{
		{"pod-1", "Episode 1: Getting Started", 1800, 2},
		{"pod-2", "Episode 2: Going Deeper", 3600, 28},
		{"pod-3", "Episode 3: Wrapping Up", 600, 50},
	}
	for _, ai := range audioItems {
		_, _, err := store.UpsertItem(Item{
			SubscriptionID: pod, GUID: ai.guid, Type: "audio", Title: ai.title,
			Author:      "Hosts",
			MediaURL:    "https://download.samplelib.com/mp3/sample-15s.mp3",
			MediaMIME:   "audio/mpeg",
			DurationSec: ai.dur,
			URL:         "https://pod.example.com/" + ai.guid,
			PublishedAt: now.Add(-time.Duration(ai.ageHours) * time.Hour).Unix(),
		})
		if err != nil {
			return fmt.Errorf("seed feed: item %s: %w", ai.guid, err)
		}
	}

	p.log.Info("seeded feed data", "user", username)
	return nil
}

func nullInt(v int64) sql.NullInt64 {
	return sql.NullInt64{Int64: v, Valid: true}
}
