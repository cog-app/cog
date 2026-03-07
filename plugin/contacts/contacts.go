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
)

//go:generate go tool templ generate -log-level warn
//go:generate go tool migratesql .

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

const PluginID = "contacts"

// Plugin is the contacts plugin.
type Plugin struct {
	plugin.Protected
	ctx plugin.PluginContext
}

// New creates a new Contacts plugin instance.
func New() *Plugin {
	return &Plugin{}
}

// sharedAddressBook describes an address book shared to the current user.
type sharedAddressBook struct {
	OwnerUsername string
	Book          *AddressBook
	Perms         uint32
}

// sharedBooks returns address books shared to the given user (by user ID).
func (p *Plugin) sharedBooks(userID string) []sharedAddressBook {
	shares := p.ctx.ShareStore()
	if shares == nil {
		return nil
	}
	users := p.ctx.UserLookup()
	if users == nil {
		return nil
	}
	var result []sharedAddressBook
	for rec, err := range shares.ListBySubject(userID) {
		if err != nil {
			log.Printf("contacts: list shared books: %v", err)
			break
		}
		if rec.ResourceType != "contacts:address_book" {
			continue
		}
		ownerUser, err := users.GetByID(rec.OwnerUserID)
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
			Book:          book,
			Perms:         rec.Permissions,
		})
	}
	return result
}

// checkBookAccess checks if userID has at least requiredPerm on an address book
// owned by ownerUsername. Returns true if the user IS the owner, or if a share grants access.
func (p *Plugin) checkBookAccess(userID, ownerUsername, bookUID string, requiredPerm uint32) bool {
	shares := p.ctx.ShareStore()
	if shares == nil {
		return false
	}
	decision, err := shares.CheckAccess(userID, "contacts:address_book", bookUID)
	if err != nil {
		return false
	}
	return decision.Allowed && (decision.EffectivePerms&requiredPerm) == requiredPerm
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx

	ctx.RegisterNavItem(plugin.NavItem{Label: "Contacts", URL: "/apps/contacts", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2"></path><circle cx="9" cy="7" r="4"></circle><path d="M23 21v-2a4 4 0 0 0-3-3.87"></path><path d="M16 3.13a4 4 0 0 1 0 7.75"></path></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Starred Contacts",
		Priority: 40,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "cards",
		EnrichSQL:    "SELECT rowid, fn, email, tel FROM cards WHERE rowid IN (?)",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "address_book",
		Table:    "address_books",
		IDColumn: "uid",
		PermissionLevels: []plugin.PermissionLevel{
			{Label: "Read-only", Permissions: plugin.PermRead},
			{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
		},
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	ctx.RegisterAPI("contacts/{id}", http.HandlerFunc(pages.ContactDetail))
	ctx.RegisterAction("POST", "contacts/{id}", http.HandlerFunc(pages.UpdateContact))
	ctx.RegisterAction("POST", "contacts", http.HandlerFunc(pages.CreateContact))
	ctx.RegisterAction("DELETE", "contacts/{id}", http.HandlerFunc(pages.DeleteContact))
	ctx.RegisterAPI("photos/{id}", http.HandlerFunc(pages.ServePhoto))
	ctx.RegisterAction("POST", "contacts/{id}/photo", http.HandlerFunc(pages.UploadPhoto))
	ctx.RegisterAction("POST", "books", http.HandlerFunc(pages.CreateAddressBookHandler))
	ctx.RegisterAction("DELETE", "books/{id}", http.HandlerFunc(pages.DeleteAddressBookHandler))

	ctx.RegisterAction("POST", "contacts/{id}/star", http.HandlerFunc(pages.ToggleStar))

	// CardDAV endpoints
	carddav := NewCardDAVHandler(p)
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("/remote.php/dav/addressbooks/", carddav)
	}

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
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate contacts db for %s: %w", username, err)
	}
	return db, nil
}
