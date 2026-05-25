// Package feed implements a unified feed reader plugin.
package feed

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/ncapi"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "feed"

// Plugin is the feed reader plugin.
type Plugin struct {
	plugin.Protected
	ctx plugin.PluginContext
	log *slog.Logger
}

// New creates a new feed plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "items",
		EnrichSQL:    "SELECT rowid, title, url, content_html FROM items WHERE rowid IN (?)",
	})

	// NextCloud News API compatibility
	newsAPI := ncapi.New(p)
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("/index.php/apps/news/api/", newsAPI)
	}

	return nil
}

func (p *Plugin) Start(_ context.Context) error {
	return nil
}

func (p *Plugin) Stop() error {
	return nil
}

// TriggerFetch schedules an immediate background fetch for a subscription.
// This is a no-op placeholder until the poller exposes an on-demand fetch API.
func (p *Plugin) TriggerFetch(username string, subID int64) {
	p.log.Debug("feed: TriggerFetch requested", "username", username, "subID", subID)
}

// DB returns the feed plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate feed db for %s: %w", username, err)
	}
	return db, nil
}
