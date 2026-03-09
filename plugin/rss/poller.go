package rss

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// poll runs the background polling loop, waking every 60 seconds.
func (p *Plugin) poll(ctx context.Context) {
	defer close(p.done)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Do an initial poll shortly after startup.
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.pollAllUsers()
		case <-ticker.C:
			p.pollAllUsers()
		}
	}
}

// pollAllUsers scans the data directory for users with RSS databases and polls their feeds.
func (p *Plugin) pollAllUsers() {
	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		p.log.Error("poll scan dataDir", "err", err)
		return
	}

	now := time.Now().Unix()

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()

		// Check if the user has an RSS database.
		dbPath := filepath.Join(p.ctx.DataDir(), username, "app_data", PluginID, "plugin.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		p.pollUser(username, now)
	}
}

func (p *Plugin) pollUser(username string, now int64) {
	db, err := p.DB(username)
	if err != nil {
		p.log.Error("poll db", "user", username, "err", err)
		return
	}

	feeds, err := FeedsDue(db, now)
	if err != nil {
		p.log.Error("poll feeds", "user", username, "err", err)
		return
	}

	for i := range feeds {
		if err := fetchFeed(p.client, db, &feeds[i]); err != nil {
			p.log.Error("fetch feed", "url", feeds[i].URL, "user", username, "err", err)
		}
	}
}

// FetchNow immediately fetches a feed for a user (used after adding a new feed).
func (p *Plugin) FetchNow(username string, feedID int64) {
	db, err := p.DB(username)
	if err != nil {
		return
	}
	feed, err := GetFeed(db, feedID)
	if err != nil {
		return
	}
	if err := fetchFeed(p.client, db, feed); err != nil {
		p.log.Error("immediate fetch", "url", feed.URL, "err", err)
	}
}
