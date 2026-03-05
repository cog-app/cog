package contacts

import (
	"context"
	"database/sql"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

const PluginID = "contacts"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
}

// Plugin is the contacts plugin.
type Plugin struct {
	plugin.Protected
	registry dbProvider
}

// New creates a new Contacts plugin instance.
func New(registry dbProvider) *Plugin {
	return &Plugin{registry: registry}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar cogplugin.Registrar) error {
	registrar.RegisterNavItem(cogplugin.NavItem{Label: "Contacts", URL: "/apps/contacts", Icon: "contacts"})
	registrar.RegisterSearchSource(cogplugin.SearchSourceDef{
		PrimaryTable: "cards",
		EnrichSQL:    "SELECT rowid, fn, email, tel FROM cards WHERE rowid IN (?)",
		Fields: []cogplugin.SearchField{
			{Column: "fn", Field: "name"},
			{Column: "email", Field: "subtext"},
			{Column: "tel", Field: "content"},
		},
	})
	registrar.RegisterResource(cogplugin.ResourceDef{
		Type:     "address_book",
		Table:    "address_books",
		IDColumn: "uid",
	})
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the contacts plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, fmt.Errorf("migrate contacts db for %s: %w", username, err)
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS address_books (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			uid TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL DEFAULT 'Contacts',
			description TEXT NOT NULL DEFAULT '',
			color TEXT NOT NULL DEFAULT '#0082c9',
			sort_order INTEGER NOT NULL DEFAULT 0,
			sync_token INTEGER NOT NULL DEFAULT 0,
			ctag INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch())
		);

		CREATE TABLE IF NOT EXISTS cards (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			address_book_id INTEGER NOT NULL REFERENCES address_books(id) ON DELETE CASCADE,
			uid TEXT NOT NULL,
			uri TEXT NOT NULL,
			etag TEXT NOT NULL,
			fn TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			tel TEXT NOT NULL DEFAULT '',
			vcard_data TEXT NOT NULL,
			rev TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (unixepoch()),
			updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
			UNIQUE(address_book_id, uid)
		);

		CREATE INDEX IF NOT EXISTS idx_cards_book ON cards(address_book_id);
		CREATE INDEX IF NOT EXISTS idx_cards_uid ON cards(uid);
		CREATE INDEX IF NOT EXISTS idx_cards_uri ON cards(uri);

		CREATE TABLE IF NOT EXISTS deleted_cards (
			address_book_id INTEGER NOT NULL,
			uid TEXT NOT NULL,
			deleted_at_token INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_deleted_cards_book ON deleted_cards(address_book_id, deleted_at_token);
	`)
	return err
}
