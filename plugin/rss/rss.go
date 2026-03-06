// Package rss implements an RSS/Atom feed reader plugin with NextCloud News API compatibility.
package rss

//go:generate go tool templ generate -log-level warn

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"net/http"
	"time"

	internalplugin "git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool migratesql .

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

const PluginID = "rss"

// Plugin is the RSS feed reader plugin.
type Plugin struct {
	ctx    plugin.PluginContext
	cancel context.CancelFunc
	done   chan struct{}
	client *http.Client
}

// New creates a new RSS plugin instance.
func New() *Plugin {
	return &Plugin{
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

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx

	ctx.RegisterNavItem(plugin.NavItem{Label: "News", URL: "/apps/rss", Icon: "rss"})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Unread Articles",
		Priority: 20,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "items",
		EnrichSQL:    "SELECT rowid, title, url, content_html FROM items WHERE rowid IN (?)",
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("feed/{id}", http.HandlerFunc(pages.Feed))
	ctx.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	ctx.RegisterPage("folder/{id}", http.HandlerFunc(pages.FolderView))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	ctx.RegisterAPI("opml/export", http.HandlerFunc(pages.ExportOPML))
	ctx.RegisterAction("POST", "feeds", http.HandlerFunc(pages.AddFeed))
	ctx.RegisterAction("DELETE", "feeds/{id}", http.HandlerFunc(pages.DeleteFeed))
	ctx.RegisterAction("POST", "feeds/{id}/read", http.HandlerFunc(pages.MarkFeedRead))
	ctx.RegisterAction("POST", "feeds/{id}/refresh", http.HandlerFunc(pages.RefreshFeed))
	ctx.RegisterAction("POST", "feeds/{id}/auto-read", http.HandlerFunc(pages.SetAutoReadPattern))
	ctx.RegisterAction("POST", "folders", http.HandlerFunc(pages.AddFolder))
	ctx.RegisterAction("DELETE", "folders/{id}", http.HandlerFunc(pages.DeleteFolder))
	ctx.RegisterAction("POST", "items/{id}/read", http.HandlerFunc(pages.ToggleRead))
	ctx.RegisterAction("POST", "items/{id}/mark-read", http.HandlerFunc(pages.MarkRead))
	ctx.RegisterAction("POST", "items/{id}/star", http.HandlerFunc(pages.ToggleStar))
	ctx.RegisterAction("POST", "items/read-all", http.HandlerFunc(pages.MarkAllRead))
	ctx.RegisterAction("POST", "opml/import", http.HandlerFunc(pages.ImportOPML))

	// NextCloud News API compatibility
	newsAPI := NewNewsAPI(p)
	if priv, ok := ctx.(internalplugin.PrivilegedContext); ok {
		priv.RegisterRoute("/index.php/apps/news/api/", newsAPI)
	}

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
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
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
	return unreadArticlesWidget(items).Render(ctx, w)
}
