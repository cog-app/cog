package media

import (
	"context"
	"fmt"
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
	p.setScanRunning("scanning...")

	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		p.log.Error("poll scan dataDir", "err", err)
		p.setScanDone(err.Error())
		return
	}

	now := p.ctx.Now().Unix()
	var lastErr string

	// Count users with media DBs.
	var users []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dbPath := filepath.Join(p.ctx.DataDir(), entry.Name(), "app_data", PluginID, "plugin.db")
		if _, err := os.Stat(dbPath); err == nil {
			users = append(users, entry.Name())
		}
	}

	for i, username := range users {
		p.setScanProgress(fmt.Sprintf("user %d/%d: %s", i+1, len(users), username))
		p.pollUser(ctx, username, now)
	}

	p.setScanDone(lastErr)
}

func (p *Plugin) pollUser(ctx context.Context, username string, now int64) {
	db, err := p.DB(username)
	if err != nil {
		p.log.Error("poll db", "user", username, "err", err)
		p.addScanWarning(fmt.Sprintf("%s: db error: %s", username, err))
		return
	}

	sources, err := SourcesDue(db, now)
	if err != nil {
		p.log.Error("poll sources", "user", username, "err", err)
		p.addScanWarning(fmt.Sprintf("%s: list sources: %s", username, err))
		return
	}

	for i := range sources {
		p.setScanProgress(fmt.Sprintf("%s: %d/%d %s", username, i+1, len(sources), sources[i].Title))
		if err := p.fetchSource(ctx, db, username, &sources[i]); err != nil {
			p.log.Error("fetch source", "url", sources[i].URL, "user", username, "err", err)
			p.addScanWarning(fmt.Sprintf("%s: %s: %s", username, sources[i].Title, err))
		}
	}
}
