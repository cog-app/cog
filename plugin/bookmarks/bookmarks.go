// Package bookmarks implements a bookmark management plugin with NextCloud Bookmarks API compatibility.
package bookmarks

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "bookmarks"

// Plugin is the bookmarks plugin.
type Plugin struct {
	ctx plugin.PluginContext
	log *slog.Logger
}

// New creates a new Bookmarks plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

// SchemaFS lets the host migrate each per-user database as it opens it.
func (p *Plugin) SchemaFS() fs.FS { return sqlFS }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{Label: "Bookmarks", URL: "/apps/bookmarks", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M19 21l-7-5-7 5V5a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2z"></path></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Bookmarks",
		Priority: 35,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "bookmarks",
		EnrichSQL:    "SELECT rowid, title, url, description FROM bookmarks WHERE rowid IN (?)",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "folder",
		Table:    "folders",
		IDColumn: "id",
		PermissionLevels: []plugin.PermissionLevel{
			{Label: "Read-only", Permissions: plugin.PermRead},
			{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
		},
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("folder/{id}", http.HandlerFunc(pages.FolderView))
	ctx.RegisterPage("tags", http.HandlerFunc(pages.TagsView))
	ctx.RegisterPage("tag/{tag}", http.HandlerFunc(pages.TagView))
	ctx.RegisterPage("bookmark/{id}", http.HandlerFunc(pages.BookmarkView))
	ctx.RegisterPage("bookmark/{id}/edit", http.HandlerFunc(pages.BookmarkEdit))
	ctx.RegisterPage("new", http.HandlerFunc(pages.NewBookmark))
	ctx.RegisterAction("POST", "bookmarks", http.HandlerFunc(pages.CreateBookmark))
	ctx.RegisterAction("POST", "bookmarks/{id}", http.HandlerFunc(pages.UpdateBookmark))
	ctx.RegisterAction("DELETE", "bookmarks/{id}", http.HandlerFunc(pages.DeleteBookmark))
	ctx.RegisterAction("POST", "bookmarks/{id}/click", http.HandlerFunc(pages.RecordClick))
	ctx.RegisterAction("POST", "folders", http.HandlerFunc(pages.CreateFolder))
	ctx.RegisterAction("POST", "folders/{id}", http.HandlerFunc(pages.UpdateFolder))
	ctx.RegisterAction("DELETE", "folders/{id}", http.HandlerFunc(pages.DeleteFolder))
	ctx.RegisterAction("POST", "import", http.HandlerFunc(pages.ImportBookmarks))
	ctx.RegisterAPI("export", http.HandlerFunc(pages.ExportBookmarks))

	// NextCloud Bookmarks API compatibility
	api := NewBookmarksAPI(p)
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("/index.php/apps/bookmarks/public/rest/v2/", api)
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the bookmarks plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate bookmarks db for %s: %w", username, err)
	}
	return db, nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	bms, _ := RecentBookmarks(db, 5)
	return recentBookmarksWidget(bms).Render(ctx, w)
}
