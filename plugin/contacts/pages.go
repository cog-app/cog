package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
	"github.com/google/uuid"
)

// Pages holds the web UI handlers for the contacts plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

// access is the request user's view of their own and shared address books.
type access struct {
	own    map[int64]AddressBook
	shared []sharedAddressBook
}

func (h *Pages) access(r *http.Request) (*plugin.UserInfo, *access, error) {
	u := plugin.UserFromContext(r.Context())
	own, err := h.plugin.bookMap(u.ID)
	if err != nil {
		return nil, nil, err
	}
	shared, err := h.plugin.sharedBooks(u.ID)
	if err != nil {
		return nil, nil, err
	}
	return u, &access{own: own, shared: shared}, nil
}

// books lists own books, by sort order, then shared ones.
func (a *access) books() []AddressBook {
	var books []AddressBook
	for _, b := range a.own {
		books = append(books, b)
	}
	slices.SortFunc(books, func(x, y AddressBook) int {
		if x.SortOrder != y.SortOrder {
			return x.SortOrder - y.SortOrder
		}
		return strings.Compare(x.Name, y.Name)
	})
	for _, sb := range a.shared {
		books = append(books, sb.Book)
	}
	return books
}

// book returns the database holding a book the user may use, and whether
// they may write to it.
func (h *Pages) book(u *plugin.UserInfo, a *access, id int64) (*sql.DB, *AddressBook, bool, error) {
	if b, ok := a.own[id]; ok {
		db, err := h.plugin.DB(u.Username)
		return db, &b, true, err
	}
	for _, sb := range a.shared {
		if sb.Book.ID == id {
			db, err := h.plugin.DB(sb.OwnerUsername)
			return db, &sb.Book, sb.Perms&plugin.PermWrite != 0, err
		}
	}
	return nil, nil, false, dav.ErrNotFound
}

// card finds a card by row ID in the user's own books, then in shared ones.
// Row IDs are per database, so the book the card is in decides which.
func (h *Pages) card(u *plugin.UserInfo, a *access, id int64) (*sql.DB, *Card, *AddressBook, bool, error) {
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if c, err := GetCard(db, id); err == nil {
		if b, ok := a.own[c.AddressBookID]; ok {
			return db, c, &b, true, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, false, err
	}
	for _, sb := range a.shared {
		ownerDB, err := h.plugin.DB(sb.OwnerUsername)
		if err != nil {
			return nil, nil, nil, false, err
		}
		c, err := GetCard(ownerDB, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, nil, false, err
		}
		if c.AddressBookID == sb.Book.ID {
			return ownerDB, c, &sb.Book, sb.Perms&plugin.PermWrite != 0, nil
		}
	}
	return nil, nil, nil, false, dav.ErrNotFound
}

func httpError(w http.ResponseWriter, err error) {
	if errors.Is(err, dav.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// Index shows all contacts, including contacts from shared address books.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	plugin.SetTitle(w, "Contacts")

	var filter int64 = -1
	if v := r.URL.Query().Get("book"); v != "" {
		if filter, err = strconv.ParseInt(v, 10, 64); err != nil {
			http.Error(w, "invalid book", http.StatusBadRequest)
			return
		}
	}
	own := a.own
	if filter >= 0 {
		own = map[int64]AddressBook{}
		if b, ok := a.own[filter]; ok {
			own[filter] = b
		}
	}
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		httpError(w, err)
		return
	}
	cards, err := CardsIn(db, own, "", 500)
	if err != nil {
		httpError(w, err)
		return
	}

	// Merge in contacts from shared address books.
	for _, sb := range a.shared {
		if filter >= 0 && filter != sb.Book.ID {
			continue
		}
		ownerDB, err := h.plugin.DB(sb.OwnerUsername)
		if err != nil {
			httpError(w, err)
			return
		}
		sharedCards, err := CardsIn(ownerDB, map[int64]AddressBook{sb.Book.ID: sb.Book}, "", 500)
		if err != nil {
			httpError(w, err)
			return
		}
		for i := range sharedCards {
			sharedCards[i].OwnerUsername = sb.OwnerUsername
			sharedCards[i].BookName = sb.Book.Name + " (" + sb.OwnerUsername + ")"
		}
		cards = append(cards, sharedCards...)
	}

	contactsIndexPage(a.books(), cards, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Starred shows starred contacts.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		httpError(w, err)
		return
	}
	cards, err := StarredContacts(db, a.own, 500)
	if err != nil {
		httpError(w, err)
		return
	}
	plugin.SetTitle(w, "Starred Contacts")
	contactsStarredPage(a.books(), cards).Render(r.Context(), w)
}

// Settings shows address book management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	books, err := h.plugin.Books(u.ID)
	if err != nil {
		httpError(w, err)
		return
	}
	plugin.SetTitle(w, "Contacts Settings")
	contactsSettingsPage(books, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ContactDetail returns a contact detail partial for the modal.
func (h *Pages) ContactDetail(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	_, c, book, _, err := h.card(u, a, id)
	if err != nil {
		httpError(w, err)
		return
	}
	contactDetailModal(c, book, a.books(), plugin.CSRFToken(r)).Render(r.Context(), w)
}

func contactForm(r *http.Request, uid string) VCardFields {
	emails := parseTypedValues(r, "email_type", "email_value")
	phones := parseTypedValues(r, "phone_type", "phone_value")
	fields := VCardFields{
		UID:    uid,
		FN:     r.FormValue("fn"),
		Org:    r.FormValue("org"),
		Title:  r.FormValue("title"),
		Note:   r.FormValue("note"),
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
	return fields
}

// CreateContact handles POST to create a new contact, in one of the user's
// own books or a shared one they may write to.
func (h *Pages) CreateContact(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	bookID, err := strconv.ParseInt(r.FormValue("address_book_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid address book", http.StatusBadRequest)
		return
	}
	db, _, writable, err := h.book(u, a, bookID)
	if err != nil || !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	fields := contactForm(r, uuid.NewString())
	now := h.plugin.ctx.Now()
	if _, err := SaveCard(db, bookID, nil, GenerateVCardFull(fields, now), now); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// UpdateContact handles POST to update an existing contact. It can move the
// contact to another book in the same database: the user's own, or the same
// owner's.
func (h *Pages) UpdateContact(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	db, c, book, writable, err := h.card(u, a, id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	bookID := c.AddressBookID
	if v := r.FormValue("address_book_id"); v != "" && v != "0" {
		if bookID, err = strconv.ParseInt(v, 10, 64); err != nil {
			http.Error(w, "invalid address book", http.StatusBadRequest)
			return
		}
	}
	if bookID != c.AddressBookID {
		_, target, ok, err := h.book(u, a, bookID)
		if err != nil || !ok || target.OwnerID != book.OwnerID {
			http.Error(w, "access denied", http.StatusForbidden)
			return
		}
	}
	fields := contactForm(r, c.UID)
	now := h.plugin.ctx.Now()
	if _, err := SaveCard(db, bookID, c, GenerateVCardFull(fields, now), now); err != nil {
		httpError(w, err)
		return
	}
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// DeleteContact handles DELETE to remove a contact (HTMX).
func (h *Pages) DeleteContact(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	db, c, _, writable, err := h.card(u, a, id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	if err := DeleteCardByID(db, c.ID, h.plugin.ctx.Now()); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// CreateAddressBookHandler handles POST to create a new address book.
func (h *Pages) CreateAddressBookHandler(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	if name != "" {
		if _, err := h.plugin.CreateBook(u.ID, r.FormValue("uid"), name, "", r.FormValue("color")); err != nil {
			httpError(w, err)
			return
		}
	}
	http.Redirect(w, r, "/apps/contacts/settings", http.StatusSeeOther)
}

// DeleteAddressBookHandler handles DELETE to remove an address book (HTMX).
func (h *Pages) DeleteAddressBookHandler(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.plugin.DeleteBook(u.Username, u.ID, id); err != nil {
		httpError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// UploadPhoto handles a contact photo upload.
func (h *Pages) UploadPhoto(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	db, c, _, writable, err := h.card(u, a, id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
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

	if err := SetPhotoPath(db, c.ID, photoPath); err != nil {
		httpError(w, err)
		return
	}

	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// ServePhoto serves a contact's photo image.
func (h *Pages) ServePhoto(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	_, c, _, _, err := h.card(u, a, id)
	if err != nil {
		httpError(w, err)
		return
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
	if _, err := io.Copy(w, f); err != nil {
		h.plugin.log.Error("serve photo", "card", c.ID, "err", err)
	}
}

// ToggleStar toggles the starred state of a contact and returns the updated star button HTML.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	db, c, book, _, err := h.card(u, a, id)
	if err != nil || book.OwnerID != u.ID {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	newStarred := !c.IsStarred
	if err := SetStarred(db, c.ID, newStarred); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return updated star button for HTMX swap.
	ui.Star(newStarred, fmt.Sprintf("/apps/contacts/contacts/%d/star", id)).Render(r.Context(), w)
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
