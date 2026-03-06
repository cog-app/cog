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

	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool migratesql .

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

const PluginID = "rss"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
	NavItems() []plugin.NavItem
}

// Plugin is the RSS feed reader plugin.
type Plugin struct {
	registry dbProvider
	dataDir  string
	secret   []byte
	cancel   context.CancelFunc
	done     chan struct{}
	client   *http.Client
}

// New creates a new RSS plugin instance.
func New(registry dbProvider, dataDir string, secret []byte) *Plugin {
	return &Plugin{
		registry: registry,
		dataDir:  dataDir,
		secret:   secret,
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
		Title:    "Recent Unread Articles",
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

	// Web UI routes
	pages := NewPages(p, p.secret, p.registry)
	registrar.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	registrar.RegisterPage("feed/{id}", http.HandlerFunc(pages.Feed))
	registrar.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	registrar.RegisterPage("folder/{id}", http.HandlerFunc(pages.FolderView))
	registrar.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	registrar.RegisterPage("opml/export", http.HandlerFunc(pages.ExportOPML))
	registrar.RegisterAction("POST", "feeds", http.HandlerFunc(pages.AddFeed))
	registrar.RegisterAction("DELETE", "feeds/{id}", http.HandlerFunc(pages.DeleteFeed))
	registrar.RegisterAction("POST", "feeds/{id}/read", http.HandlerFunc(pages.MarkFeedRead))
	registrar.RegisterAction("POST", "feeds/{id}/refresh", http.HandlerFunc(pages.RefreshFeed))
	registrar.RegisterAction("POST", "feeds/{id}/auto-read", http.HandlerFunc(pages.SetAutoReadPattern))
	registrar.RegisterAction("POST", "folders", http.HandlerFunc(pages.AddFolder))
	registrar.RegisterAction("DELETE", "folders/{id}", http.HandlerFunc(pages.DeleteFolder))
	registrar.RegisterAction("POST", "items/{id}/read", http.HandlerFunc(pages.ToggleRead))
	registrar.RegisterAction("POST", "items/{id}/mark-read", http.HandlerFunc(pages.MarkRead))
	registrar.RegisterAction("POST", "items/{id}/star", http.HandlerFunc(pages.ToggleStar))
	registrar.RegisterAction("POST", "items/read-all", http.HandlerFunc(pages.MarkAllRead))
	registrar.RegisterAction("POST", "opml/import", http.HandlerFunc(pages.ImportOPML))

	// NextCloud News API compatibility
	newsAPI := NewNewsAPI(p)
	registrar.RegisterRoute("/index.php/apps/news/api/", newsAPI)

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
	if err := cogplugin.Migrate(db, sqlFS); err != nil {
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
