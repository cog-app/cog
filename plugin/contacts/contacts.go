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

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "contacts"

// Plugin is the contacts plugin.
type Plugin struct {
	ctx plugin.PluginContext
	log *slog.Logger
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
			p.log.Error("list shared books", "err", err)
			break
		}
		if rec.ResourceType != "contacts:address_book" {
			continue
		}
		ownerUser, err := users.GetByID(rec.OwnerUserID)
		if err != nil {
			p.log.Error("lookup user", "user_id", rec.OwnerUserID, "err", err)
			continue
		}
		ownerUsername := ownerUser.Username
		ownerDB, err := p.DB(ownerUsername)
		if err != nil {
			p.log.Error("db for user", "user", ownerUsername, "err", err)
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
	ctx.RegisterDAVHandler("addressbooks", carddav)

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

// CreateCardByName creates a minimal contact card with just a name in the user's
// default address book. Returns the vCard UID of the created contact.
func (p *Plugin) CreateCardByName(username, name string) (string, error) {
	db, err := p.DB(username)
	if err != nil {
		return "", err
	}
	if err := EnsureDefaultAddressBook(db); err != nil {
		return "", fmt.Errorf("ensure default address book: %w", err)
	}
	books, err := ListAddressBooks(db)
	if err != nil || len(books) == 0 {
		return "", fmt.Errorf("no address book available")
	}
	bookID := books[0].ID

	now := p.ctx.Now()
	uid := fmt.Sprintf("%d-%s", now.UnixNano(), name)
	uri := uid + ".vcf"
	fields := VCardFields{UID: uid, FN: name}
	fields.MarshalJSONFields()
	vcardData := GenerateVCardFull(fields, now)
	if _, err := UpsertCard(db, bookID, uid, uri, vcardData, fields); err != nil {
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
	_, err = db.Exec(`UPDATE cards SET photo_path = ? WHERE id = ?`, photoPath, card.ID)
	return err
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
