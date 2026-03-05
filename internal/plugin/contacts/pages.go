package contacts

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

type navProvider interface {
	NavItems() []plugin.NavItem
}

// Pages holds the web UI handlers for the contacts plugin.
type Pages struct {
	plugin *Plugin
	secret []byte
	nav    navProvider
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin, secret []byte, nav navProvider) *Pages {
	return &Pages{plugin: p, secret: secret, nav: nav}
}

func (h *Pages) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, h.secret)
	}
	var isAdmin bool
	if u != nil {
		isAdmin = u.IsAdmin
	}

	var nav []ui.NavItem
	for _, item := range h.nav.NavItems() {
		active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
		nav = append(nav, ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		})
	}

	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         nav,
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
	}
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := auth.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

// Index shows all contacts.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	EnsureDefaultAddressBook(db)

	pd := h.pageData(r, "Contacts")
	books, _ := ListAddressBooks(db)
	cards, _ := AllCards(db, 500)

	// Filter by book if query param present.
	bookIDStr := r.URL.Query().Get("book")
	if bookIDStr != "" {
		bookID, _ := strconv.ParseInt(bookIDStr, 10, 64)
		var filtered []CardWithBook
		for _, c := range cards {
			if c.AddressBookID == bookID {
				filtered = append(filtered, c)
			}
		}
		cards = filtered
	}

	contactsIndexPage(pd, books, cards).Render(r.Context(), w)
}

// Settings shows address book management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pd := h.pageData(r, "Contacts Settings")
	books, _ := ListAddressBooks(db)
	contactsSettingsPage(pd, books).Render(r.Context(), w)
}

// ContactDetail returns a contact detail partial for the modal.
func (h *Pages) ContactDetail(w http.ResponseWriter, r *http.Request) {
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
	c, err := GetCard(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	book, _ := GetAddressBook(db, c.AddressBookID)
	books, _ := ListAddressBooks(db)

	pd := h.pageData(r, "Contact")
	contactDetailModal(pd, c, book, books).Render(r.Context(), w)
}

// CreateContact handles POST to create a new contact.
func (h *Pages) CreateContact(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bookID, _ := strconv.ParseInt(r.FormValue("address_book_id"), 10, 64)
	fn := r.FormValue("fn")
	email := r.FormValue("email")
	tel := r.FormValue("tel")

	uid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), fn)
	uri := uid + ".vcf"
	vcardData := GenerateVCard(uid, fn, email, tel)
	fields := VCardFields{UID: uid, FN: fn, Email: email, Tel: tel}

	UpsertCard(db, bookID, uid, uri, vcardData, fields)
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// UpdateContact handles POST to update an existing contact.
func (h *Pages) UpdateContact(w http.ResponseWriter, r *http.Request) {
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
	c, err := GetCard(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	fn := r.FormValue("fn")
	email := r.FormValue("email")
	tel := r.FormValue("tel")
	bookID, _ := strconv.ParseInt(r.FormValue("address_book_id"), 10, 64)
	if bookID == 0 {
		bookID = c.AddressBookID
	}

	vcardData := GenerateVCard(c.UID, fn, email, tel)
	fields := VCardFields{UID: c.UID, FN: fn, Email: email, Tel: tel}

	UpsertCard(db, bookID, c.UID, c.URI, vcardData, fields)
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// DeleteContact handles DELETE to remove a contact (HTMX).
func (h *Pages) DeleteContact(w http.ResponseWriter, r *http.Request) {
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
