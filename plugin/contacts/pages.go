package contacts

import (
	"database/sql"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the contacts plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

func (h *Pages) csrfToken(r *http.Request) string {
	return plugin.CSRFTokenFromContext(r.Context())
}

func setTitle(w http.ResponseWriter, title string) {
	w.Header().Set("X-Page-Title", title)
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := plugin.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

// Index shows all contacts, including contacts from shared address books.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	EnsureDefaultAddressBook(db)

	setTitle(w, "Contacts")
	books, _ := ListAddressBooks(db)

	var cards []CardWithBook
	if bookIDStr := r.URL.Query().Get("book"); bookIDStr != "" {
		bookID, _ := strconv.ParseInt(bookIDStr, 10, 64)
		cards, _ = CardsByAddressBook(db, bookID, 500)
	} else {
		cards, _ = AllCards(db, 500)
	}

	// Merge in shared address books and their contacts.
	for _, sb := range h.plugin.sharedBooks(u.ID) {
		ownerDB, err := h.plugin.DB(sb.OwnerUsername)
		if err != nil {
			h.plugin.log.Error("shared db", "user", sb.OwnerUsername, "err", err)
			continue
		}
		books = append(books, *sb.Book)

		bookIDStr := r.URL.Query().Get("book")
		if bookIDStr != "" {
			bookID, _ := strconv.ParseInt(bookIDStr, 10, 64)
			if bookID != sb.Book.ID {
				continue
			}
		}
		sharedCards, _ := CardsByAddressBook(ownerDB, sb.Book.ID, 500)
		for i := range sharedCards {
			sharedCards[i].OwnerUsername = sb.OwnerUsername
			sharedCards[i].BookName = sb.Book.Name + " (" + sb.OwnerUsername + ")"
		}
		cards = append(cards, sharedCards...)
	}

	contactsIndexPage(books, cards, h.csrfToken(r)).Render(r.Context(), w)
}

// Starred shows starred contacts.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setTitle(w, "Starred Contacts")
	books, _ := ListAddressBooks(db)
	cards, _ := StarredContacts(db, 500)
	contactsStarredPage(books, cards).Render(r.Context(), w)
}

// Settings shows address book management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setTitle(w, "Contacts Settings")
	books, _ := ListAddressBooks(db)
	contactsSettingsPage(books, h.csrfToken(r)).Render(r.Context(), w)
}

// ContactDetail returns a contact detail partial for the modal.
func (h *Pages) ContactDetail(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Try the user's own DB first.
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		// Not in own DB — check shared address books.
		c, db, err = h.findSharedCard(u.ID, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	}

	book, _ := GetAddressBook(db, c.AddressBookID)
	books, _ := ListAddressBooks(db)

	contactDetailModal(c, book, books, h.csrfToken(r)).Render(r.Context(), w)
}

// findSharedCard searches shared address books for a card by ID.
// Returns the card, the owner's DB, and any error.
func (h *Pages) findSharedCard(userID string, cardID int64) (*Card, *sql.DB, error) {
	for _, sb := range h.plugin.sharedBooks(userID) {
		ownerDB, err := h.plugin.DB(sb.OwnerUsername)
		if err != nil {
			continue
		}
		c, err := GetCard(ownerDB, cardID)
		if err != nil {
			continue
		}
		// Verify the card belongs to the shared book.
		if c.AddressBookID == sb.Book.ID {
			return c, ownerDB, nil
		}
	}
	return nil, nil, fmt.Errorf("not found")
}

// CreateContact handles POST to create a new contact.
func (h *Pages) CreateContact(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	bookID, _ := strconv.ParseInt(r.FormValue("address_book_id"), 10, 64)

	// Determine which DB to write to — own or shared.
	targetDB, err := h.resolveWritableDB(u, bookID)
	if err != nil {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	fn := r.FormValue("fn")
	org := r.FormValue("org")
	title := r.FormValue("title")
	note := r.FormValue("note")

	emails := parseTypedValues(r, "email_type", "email_value")
	phones := parseTypedValues(r, "phone_type", "phone_value")

	uid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), fn)
	uri := uid + ".vcf"

	fields := VCardFields{
		UID:    uid,
		FN:     fn,
		Org:    org,
		Title:  title,
		Note:   note,
		Emails: emails,
		Phones: phones,
	}
	if len(emails) > 0 {
		fields.Email = emails[0].Value
	}
	if len(phones) > 0 {
		fields.Tel = phones[0].Value
	}
	fields.MarshalJSONFields()

	vcardData := GenerateVCardFull(fields)
	UpsertCard(targetDB, bookID, uid, uri, vcardData, fields)
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// resolveWritableDB returns the DB that owns bookID.
// If bookID is in the user's own DB, returns it.
// If bookID is in a shared address book with PermWrite, returns the owner's DB.
// Otherwise returns an error.
func (h *Pages) resolveWritableDB(u *plugin.UserInfo, bookID int64) (*sql.DB, error) {
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		return nil, err
	}
	if _, err := GetAddressBook(db, bookID); err == nil {
		return db, nil // own book
	}
	// Check shared books.
	for _, sb := range h.plugin.sharedBooks(u.ID) {
		if sb.Book.ID == bookID {
			if sb.Perms&plugin.PermWrite == 0 {
				return nil, fmt.Errorf("read-only share")
			}
			ownerDB, err := h.plugin.DB(sb.OwnerUsername)
			if err != nil {
				return nil, err
			}
			return ownerDB, nil
		}
	}
	return nil, fmt.Errorf("address book not found")
}

// UpdateContact handles POST to update an existing contact.
func (h *Pages) UpdateContact(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Find the card in own DB or shared.
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		// Try shared books.
		c, db, err = h.findSharedCard(u.ID, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// For shared cards, verify write permission.
		book, _ := GetAddressBook(db, c.AddressBookID)
		if book != nil && !h.plugin.checkBookAccess(u.ID, "", book.UID, plugin.PermWrite) {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
	}

	fn := r.FormValue("fn")
	org := r.FormValue("org")
	title := r.FormValue("title")
	note := r.FormValue("note")
	bookID, _ := strconv.ParseInt(r.FormValue("address_book_id"), 10, 64)
	if bookID == 0 {
		bookID = c.AddressBookID
	}

	emails := parseTypedValues(r, "email_type", "email_value")
	phones := parseTypedValues(r, "phone_type", "phone_value")

	fields := VCardFields{
		UID:    c.UID,
		FN:     fn,
		Org:    org,
		Title:  title,
		Note:   note,
		Emails: emails,
		Phones: phones,
	}
	if len(emails) > 0 {
		fields.Email = emails[0].Value
	}
	if len(phones) > 0 {
		fields.Tel = phones[0].Value
	}
	fields.MarshalJSONFields()

	vcardData := GenerateVCardFull(fields)
	UpsertCard(db, bookID, c.UID, c.URI, vcardData, fields)
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// DeleteContact handles DELETE to remove a contact (HTMX).
func (h *Pages) DeleteContact(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		// Try shared books.
		c, db, err = h.findSharedCard(u.ID, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Verify write permission on the shared book.
		book, _ := GetAddressBook(db, c.AddressBookID)
		if book != nil && !h.plugin.checkBookAccess(u.ID, "", book.UID, plugin.PermWrite) {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
	}
	DeleteCardByID(db, id)
	w.WriteHeader(http.StatusOK)
}

// CreateAddressBookHandler handles POST to create a new address book.
func (h *Pages) CreateAddressBookHandler(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	uid := r.FormValue("uid")
	name := r.FormValue("name")
	color := r.FormValue("color")
	if uid == "" {
		uid = name
	}
	if name != "" {
		CreateAddressBook(db, uid, name, "", color)
	}
	http.Redirect(w, r, "/apps/contacts/settings", http.StatusSeeOther)
}

// DeleteAddressBookHandler handles DELETE to remove an address book (HTMX).
func (h *Pages) DeleteAddressBookHandler(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteAddressBook(db, id)
	w.WriteHeader(http.StatusOK)
}

// UploadPhoto handles a contact photo upload.
func (h *Pages) UploadPhoto(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		c, db, err = h.findSharedCard(u.ID, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		book, _ := GetAddressBook(db, c.AddressBookID)
		if book != nil && !h.plugin.checkBookAccess(u.ID, "", book.UID, plugin.PermWrite) {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
	}

	if err := r.ParseMultipartForm(5 << 20); err != nil { // 5MB max
		http.Error(w, "file too large", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
		return
	}
	defer file.Close()

	// Validate content type.
	ct := header.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "image/") {
		http.Error(w, "only image files are accepted", http.StatusBadRequest)
		return
	}

	// Decode to validate it is a real image.
	img, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, "invalid image", http.StatusBadRequest)
		return
	}

	// Save as JPEG.
	photoDir := filepath.Join(h.plugin.ctx.DataDir(), "contacts", u.Username, "photos")
	if err := os.MkdirAll(photoDir, 0755); err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	photoPath := filepath.Join(photoDir, c.UID+".jpg")

	f, err := os.Create(photoPath)
	if err != nil {
		http.Error(w, "save error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 85}); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}

	// Update card photo_path in database.
	_, _ = db.Exec(`UPDATE cards SET photo_path = ? WHERE id = ?`, photoPath, id)

	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// ServePhoto serves a contact's photo image.
func (h *Pages) ServePhoto(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		c, _, err = h.findSharedCard(u.ID, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if c.PhotoPath == "" {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(c.PhotoPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	io.Copy(w, f)
}

// ToggleStar toggles the starred state of a contact and returns the updated star button HTML.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := GetCard(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	newStarred := !c.IsStarred
	if err := SetStarred(db, id, newStarred); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return updated star button HTML for HTMX swap.
	icon := "☆"
	if newStarred {
		icon = "★"
	}
	fmt.Fprintf(w, `<button class="btn btn-outline btn-sm" hx-post="/apps/contacts/contacts/%d/star" hx-swap="outerHTML" title="Toggle star">%s</button>`, id, icon)
}

// parseTypedValues extracts paired type/value form arrays into a slice of TypedValue.
func parseTypedValues(r *http.Request, typeKey, valueKey string) []TypedValue {
	if err := r.ParseForm(); err != nil {
		return nil
	}
	types := r.Form[typeKey]
	values := r.Form[valueKey]
	var result []TypedValue
	for i := range values {
		if values[i] == "" {
			continue
		}
		typ := ""
		if i < len(types) {
			typ = types[i]
		}
		result = append(result, TypedValue{Type: typ, Value: values[i]})
	}
	return result
}
