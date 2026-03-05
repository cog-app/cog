// Package rss implements an RSS/Atom feed reader plugin with NextCloud News API compatibility.
package rss

import (
	"context"
	"database/sql"
	"fmt"
	"html"
	"io"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

const PluginID = "rss"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
}

// Plugin is the RSS feed reader plugin.
type Plugin struct {
	registry dbProvider
	dataDir  string
	cancel   context.CancelFunc
	done     chan struct{}
	client   *http.Client
}

// New creates a new RSS plugin instance.
func New(registry dbProvider, dataDir string) *Plugin {
	return &Plugin{
		registry: registry,
		dataDir:  dataDir,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("non-HTTP redirect")
				}
				return nil
			},
		},
	}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar plugin.Registrar) error {
	registrar.RegisterNavItem(plugin.NavItem{Label: "News", URL: "/apps/rss", Icon: "rss"})
	registrar.RegisterWidget(plugin.Widget{
		Title:    "Recent Articles",
		Priority: 20,
		Render:   p.renderWidget,
	})
	registrar.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "items",
		EnrichSQL:    "SELECT rowid, title, url, content_html FROM items WHERE rowid IN (?)",
		Fields: []plugin.SearchField{
			{Column: "title", Field: "name"},
			{Column: "url", Field: "subtext"},
			{Column: "content_html", Field: "content"},
		},
	})
	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go p.poll(ctx)
	return nil
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// DB returns the RSS plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate rss db for %s: %w", username, err)
	}
	return db, nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	items, _ := UnreadItems(db, 5)
	if len(items) == 0 {
		_, _ = io.WriteString(w, `<p class="text-muted">No unread articles.</p>`)
		return nil
	}
	for _, it := range items {
		_, _ = fmt.Fprintf(w, `<div style="padding:0.4rem 0;border-bottom:1px solid var(--color-border)"><a href="/apps/rss/feed/%d" style="font-weight:500">%s</a><div class="text-muted" style="font-size:0.85rem">%s</div></div>`,
			it.FeedID, html.EscapeString(it.Title), html.EscapeString(it.FeedTitle))
	}
	return nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS folders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		);

		CREATE TABLE IF NOT EXISTS feeds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL,
			url TEXT NOT NULL UNIQUE,
			site_url TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			favicon_url TEXT NOT NULL DEFAULT '',
			etag TEXT NOT NULL DEFAULT '',
			last_modified TEXT NOT NULL DEFAULT '',
			poll_interval_sec INTEGER NOT NULL DEFAULT 1800,
			next_check_at INTEGER NOT NULL DEFAULT 0,
			last_checked_at INTEGER NOT NULL DEFAULT 0,
			last_success_at INTEGER NOT NULL DEFAULT 0,
			error_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			paused INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (unixepoch())
		);

		CREATE TABLE IF NOT EXISTS items (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
			guid TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			author TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			content_html TEXT NOT NULL DEFAULT '',
			published_at INTEGER NOT NULL DEFAULT 0,
			fetched_at INTEGER NOT NULL DEFAULT (unixepoch()),
			is_read INTEGER NOT NULL DEFAULT 0,
			is_starred INTEGER NOT NULL DEFAULT 0,
			content_hash TEXT NOT NULL DEFAULT '',
			UNIQUE(feed_id, guid)
		);

		CREATE INDEX IF NOT EXISTS idx_items_feed_id ON items(feed_id);
		CREATE INDEX IF NOT EXISTS idx_items_unread ON items(is_read, published_at DESC);
		CREATE INDEX IF NOT EXISTS idx_items_starred ON items(is_starred, published_at DESC);
		CREATE INDEX IF NOT EXISTS idx_feeds_next_check ON feeds(next_check_at);
	`)
	return err
}
