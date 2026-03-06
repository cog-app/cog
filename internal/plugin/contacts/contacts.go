package contacts

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool templ generate -log-level warn

const PluginID = "contacts"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
	NavItems() []cogplugin.NavItem
}

// userLookup resolves a user ID to user information.
// Satisfied by *user.Store from internal/user.
type userLookup interface {
	GetByID(id string) (*user.User, error)
}

// Plugin is the contacts plugin.
type Plugin struct {
	plugin.Protected
	registry dbProvider
	dataDir  string
	shares   *share.Store
	users    userLookup
	secret   []byte
}

// New creates a new Contacts plugin instance.
func New(registry dbProvider, dataDir string, shares *share.Store, users userLookup, secret []byte) *Plugin {
	return &Plugin{registry: registry, dataDir: dataDir, shares: shares, users: users, secret: secret}
}

// sharedAddressBook describes an address book shared to the current user.
type sharedAddressBook struct {
	OwnerUsername string
	Book         *AddressBook
	Perms        uint32
}

// sharedBooks returns address books shared to the given user (by user ID).
func (p *Plugin) sharedBooks(userID string) []sharedAddressBook {
	var result []sharedAddressBook
	for rec, err := range p.shares.ListBySubject(userID) {
		if err != nil {
			log.Printf("contacts: list shared books: %v", err)
			break
		}
		if rec.ResourceType != "contacts:address_book" {
			continue
		}
		ownerUser, err := p.users.GetByID(rec.OwnerUserID)
		if err != nil {
			log.Printf("contacts: lookup user %s: %v", rec.OwnerUserID, err)
			continue
		}
		ownerUsername := ownerUser.Username
		ownerDB, err := p.DB(ownerUsername)
		if err != nil {
			log.Printf("contacts: db for %s: %v", ownerUsername, err)
			continue
		}
		book, err := GetAddressBookByUID(ownerDB, rec.ResourceID)
		if err != nil {
			continue
		}
		result = append(result, sharedAddressBook{
			OwnerUsername: ownerUsername,
			Book:         book,
			Perms:        rec.Permissions,
		})
	}
	return result
}

// checkBookAccess checks if userID has at least requiredPerm on an address book
// owned by ownerUsername. Returns true if the user IS the owner, or if a share grants access.
func (p *Plugin) checkBookAccess(userID, ownerUsername, bookUID string, requiredPerm uint32) bool {
	decision, err := p.shares.CheckAccess(userID, "contacts:address_book", bookUID)
	if err != nil {
		return false
	}
	return decision.Allowed && (decision.EffectivePerms&requiredPerm) == requiredPerm
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
		PermissionLevels: []cogplugin.PermissionLevel{
			{Label: "Read-only", Permissions: cogplugin.PermRead},
			{Label: "Read/Write", Permissions: cogplugin.PermRead | cogplugin.PermWrite},
		},
	})

	// Web UI routes
	pages := NewPages(p, p.secret, p.registry)
	registrar.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	registrar.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	registrar.RegisterAPI("contacts/{id}", http.HandlerFunc(pages.ContactDetail))
	registrar.RegisterAction("POST", "contacts/{id}", http.HandlerFunc(pages.UpdateContact))
	registrar.RegisterAction("POST", "contacts", http.HandlerFunc(pages.CreateContact))
	registrar.RegisterAction("DELETE", "contacts/{id}", http.HandlerFunc(pages.DeleteContact))
	registrar.RegisterAPI("photos/{id}", http.HandlerFunc(pages.ServePhoto))
	registrar.RegisterAction("POST", "contacts/{id}/photo", http.HandlerFunc(pages.UploadPhoto))
	registrar.RegisterAction("POST", "books", http.HandlerFunc(pages.CreateAddressBookHandler))
	registrar.RegisterAction("DELETE", "books/{id}", http.HandlerFunc(pages.DeleteAddressBookHandler))

	// CardDAV endpoints
	carddav := NewCardDAVHandler(p)
	registrar.RegisterRoute("/remote.php/dav/addressbooks/", carddav)

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
	if err := migrateV2(db); err != nil {
		return nil, fmt.Errorf("migrate v2 contacts db for %s: %w", username, err)
	}
	if err := migrateV3(db); err != nil {
		return nil, fmt.Errorf("migrate v3 contacts db for %s: %w", username, err)
	}
	return db, nil
}

func migrateV2(db *sql.DB) error {
	// Check if org column exists already.
	var hasOrg bool
	rows, err := db.Query(`PRAGMA table_info(cards)`)
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
		if name == "org" {
			hasOrg = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasOrg {
		return nil
	}

	stmts := []string{
		`ALTER TABLE cards ADD COLUMN org TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cards ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cards ADD COLUMN note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE cards ADD COLUMN emails_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE cards ADD COLUMN phones_json TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE cards ADD COLUMN addresses_json TEXT NOT NULL DEFAULT '[]'`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func migrateV3(db *sql.DB) error {
	var hasPhoto bool
	rows, err := db.Query(`PRAGMA table_info(cards)`)
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
		if name == "photo_path" {
			hasPhoto = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if hasPhoto {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE cards ADD COLUMN photo_path TEXT NOT NULL DEFAULT ''`)
	return err
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
