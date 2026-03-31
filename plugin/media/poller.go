package media

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
			p.pollAllUsers(ctx)
		case <-ticker.C:
			p.pollAllUsers(ctx)
		}
	}
}

// pollAllUsers scans the data directory for users with media databases and polls their sources.
func (p *Plugin) pollAllUsers(ctx context.Context) {
	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		p.log.Error("poll scan dataDir", "err", err)
		return
	}

	now := p.ctx.Now().Unix()

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()

		// Check if the user has a media database.
		dbPath := filepath.Join(p.ctx.DataDir(), username, "app_data", PluginID, "plugin.db")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		p.pollUser(ctx, username, now)
	}
}

func (p *Plugin) pollUser(ctx context.Context, username string, now int64) {
	db, err := p.DB(username)
	if err != nil {
		p.log.Error("poll db", "user", username, "err", err)
		return
	}

	sources, err := SourcesDue(db, now)
	if err != nil {
		p.log.Error("poll sources", "user", username, "err", err)
		return
	}

	for i := range sources {
		if err := p.fetchSource(ctx, db, &sources[i]); err != nil {
			p.log.Error("fetch source", "url", sources[i].URL, "user", username, "err", err)
		}
	}
}
