package calendar

import (
	"context"
	"database/sql"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool templ generate -log-level warn

const PluginID = "calendar"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
}

// Plugin is the calendar plugin.
type Plugin struct {
	plugin.Protected
	registry dbProvider
}

// New creates a new Calendar plugin instance.
func New(registry dbProvider) *Plugin {
	return &Plugin{registry: registry}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar plugin.Registrar) error {
	registrar.RegisterNavItem(plugin.NavItem{Label: "Calendar", URL: "/apps/calendar", Icon: "calendar"})
	registrar.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "events",
		EnrichSQL:    "SELECT rowid, summary, location, description FROM events WHERE rowid IN (?)",
		Fields: []plugin.SearchField{
			{Column: "summary", Field: "name"},
			{Column: "location", Field: "subtext"},
			{Column: "description", Field: "content"},
		},
	})
	registrar.RegisterResource(plugin.ResourceDef{
		Type:     "calendar",
		Table:    "calendars",
		IDColumn: "uid",
	})
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the calendar plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate calendar db for %s: %w", username, err)
	}
	if err := migrateV2(db); err != nil {
		return nil, fmt.Errorf("migrate v2 calendar db for %s: %w", username, err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS calendars (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT 'Personal',
			color TEXT NOT NULL DEFAULT '#0082c9',
			description TEXT NOT NULL DEFAULT '',
			sort_order INTEGER NOT NULL DEFAULT 0,
			ctag INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		);

		CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
			uid TEXT NOT NULL,
			etag TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			location TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			dtstart INTEGER NOT NULL DEFAULT 0,
			dtend INTEGER NOT NULL DEFAULT 0,
			is_all_day INTEGER NOT NULL DEFAULT 0,
			ics_data TEXT NOT NULL,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
			UNIQUE(calendar_id, uid)
		);

		CREATE INDEX IF NOT EXISTS idx_events_calendar ON events(calendar_id);
		CREATE INDEX IF NOT EXISTS idx_events_dtstart ON events(dtstart);
		CREATE INDEX IF NOT EXISTS idx_events_cal_start ON events(calendar_id, dtstart);
	`)
	return err
}

func migrateV2(db *sql.DB) error {
	// Check if sync_token column exists.
	var hasSyncToken bool
	rows, err := db.Query(`PRAGMA table_info(calendars)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "sync_token" {
			hasSyncToken = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !hasSyncToken {
		if _, err := db.Exec(`ALTER TABLE calendars ADD COLUMN sync_token INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS deleted_events (
			calendar_id INTEGER NOT NULL,
			uid TEXT NOT NULL,
			deleted_at_token INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_deleted_events_cal ON deleted_events(calendar_id, deleted_at_token);
	`)
	return err
}
