package contacts

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"github.com/google/uuid"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "contacts"

// Plugin is the contacts plugin.
type Plugin struct {
	ctx plugin.PluginContext
	log *slog.Logger
	dav *dav.Server
}

// New creates a new Contacts plugin instance.
func New() *Plugin {
	return &Plugin{}
}

// SetDAV implements dav.Provider.
func (p *Plugin) SetDAV(s *dav.Server) { p.dav = s }

// DAVBackends implements dav.Provider: cards in any address book.
func (p *Plugin) DAVBackends() []dav.Backend { return []dav.Backend{newBackend(p)} }

func (p *Plugin) ID() string { return PluginID }

// DB returns the user's database for this plugin.
//
// The host owns the database: where it lives, how it is opened, and applying
// the schema registered in Register. This is a name for it, not a place that
// does any of that work.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	return p.ctx.PluginDB(username)
}

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	ctx.RegisterSchema(sqlFS)
	p.log = ctx.Logger()

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

	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, username string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	userID, err := p.userID(username)
	if err != nil {
		return err
	}
	books, err := p.bookMap(userID)
	if err != nil {
		return err
	}
	cards, err := StarredContacts(db, books, 5)
	if err != nil {
		return err
	}
	return starredContactsWidget(cards).Render(ctx, w)
}

// CreateCardByName creates a minimal contact card with just a name in the user's
// default address book. Returns the vCard UID of the created contact.
func (p *Plugin) CreateCardByName(username, name string) (string, error) {
	db, err := p.DB(username)
	if err != nil {
		return "", err
	}
	userID, err := p.userID(username)
	if err != nil {
		return "", err
	}
	books, err := p.Books(userID)
	if err != nil {
		return "", fmt.Errorf("list address books: %w", err)
	}
	if len(books) == 0 {
		return "", fmt.Errorf("no address book available")
	}

	now := p.ctx.Now()
	uid := uuid.NewString()
	fields := VCardFields{UID: uid, FN: name}
	fields.MarshalJSONFields()
	if _, err := SaveCard(db, books[0].ID, nil, GenerateVCardFull(fields, now), now); err != nil {
		return "", fmt.Errorf("create contact card: %w", err)
	}
	return uid, nil
}

// SetPhoto saves an image as a contact's photo. The contact is identified by
// vCard UID. Does nothing if the card is not found.
func (p *Plugin) SetPhoto(username, cardUID string, img image.Image) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	card, err := FindCardByUID(db, cardUID)
	if err != nil {
		return fmt.Errorf("find card %s: %w", cardUID, err)
	}
	photoDir := filepath.Join(p.ctx.DataDir(), "contacts", username, "photos")
	if err := os.MkdirAll(photoDir, 0755); err != nil {
		return fmt.Errorf("create photo dir: %w", err)
	}
	photoPath := filepath.Join(photoDir, card.UID+".jpg")
	f, err := os.Create(photoPath)
	if err != nil {
		return fmt.Errorf("create photo file: %w", err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		return fmt.Errorf("encode photo: %w", err)
	}
	return SetPhotoPath(db, card.ID, photoPath)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }
