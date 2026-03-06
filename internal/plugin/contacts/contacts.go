package contacts

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool templ generate -log-level warn
//go:generate go tool migratesql .

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

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
	registrar.RegisterWidget(cogplugin.Widget{
		Title:    "Starred Contacts",
		Priority: 40,
		Render:   p.renderWidget,
	})
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
	registrar.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	registrar.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	registrar.RegisterAPI("contacts/{id}", http.HandlerFunc(pages.ContactDetail))
	registrar.RegisterAction("POST", "contacts/{id}", http.HandlerFunc(pages.UpdateContact))
	registrar.RegisterAction("POST", "contacts", http.HandlerFunc(pages.CreateContact))
	registrar.RegisterAction("DELETE", "contacts/{id}", http.HandlerFunc(pages.DeleteContact))
	registrar.RegisterAPI("photos/{id}", http.HandlerFunc(pages.ServePhoto))
	registrar.RegisterAction("POST", "contacts/{id}/photo", http.HandlerFunc(pages.UploadPhoto))
	registrar.RegisterAction("POST", "books", http.HandlerFunc(pages.CreateAddressBookHandler))
	registrar.RegisterAction("DELETE", "books/{id}", http.HandlerFunc(pages.DeleteAddressBookHandler))

	registrar.RegisterAction("POST", "contacts/{id}/star", http.HandlerFunc(pages.ToggleStar))

	// CardDAV endpoints
	carddav := NewCardDAVHandler(p)
	registrar.RegisterRoute("/remote.php/dav/addressbooks/", carddav)

	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	cards, _ := StarredContacts(db, 5)
	return starredContactsWidget(cards).Render(ctx, w)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the contacts plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := cogplugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate contacts db for %s: %w", username, err)
	}
	return db, nil
}
