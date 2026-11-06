package contacts

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/safeimage"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
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

// book returns the store holding a book the user may use, and whether they
// may write to it.
func (h *Pages) book(u *plugin.UserInfo, a *access, id int64) (*Store, *AddressBook, bool, error) {
	if b, ok := a.own[id]; ok {
		s, err := h.plugin.Store(u.Username)
		return s, &b, true, err
	}
	for _, sb := range a.shared {
		if sb.Book.ID == id {
			s, err := h.plugin.Store(sb.OwnerUsername)
			return s, &sb.Book, sb.Perms&plugin.PermWrite != 0, err
		}
	}
	return nil, nil, false, dav.ErrNotFound
}

// card finds a card by row ID in the user's own books, then in shared ones,
// with its vCard when full is set. Row IDs are per database, so the book the
// card is in decides which.
func (h *Pages) card(u *plugin.UserInfo, a *access, id int64, full bool) (*Store, *Card, *AddressBook, bool, error) {
	s, c, b, w, err := h.findCard(u, a, id)
	if err == nil && full {
		err = s.Load(c)
	}
	return s, c, b, w, err
}

func (h *Pages) findCard(u *plugin.UserInfo, a *access, id int64) (*Store, *Card, *AddressBook, bool, error) {
	s, err := h.plugin.Store(u.Username)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if c, err := s.Row(id); err == nil {
		if b, ok := a.own[c.AddressBookID]; ok {
			return s, c, &b, true, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil, false, err
	}
	for _, sb := range a.shared {
		owner, err := h.plugin.Store(sb.OwnerUsername)
		if err != nil {
			return nil, nil, nil, false, err
		}
		c, err := owner.Row(id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, nil, false, err
		}
		if c.AddressBookID == sb.Book.ID {
			return owner, c, &sb.Book, sb.Perms&plugin.PermWrite != 0, nil
		}
	}
	return nil, nil, nil, false, dav.ErrNotFound
}

// fail reports an error from a form post on the page it came from (PRG).
func fail(w http.ResponseWriter, r *http.Request, msg string) {
	plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: msg})
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

func httpError(w http.ResponseWriter, err error) {
	if errors.Is(err, dav.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// listView is what a page of contacts shows: all of them, one book's, or
// the starred ones.
type listView struct {
	Title    string
	Active   string // the sidebar item: "all", "starred", or a book's ID
	Path     string // the page's URL, without ?contact
	Books    []AddressBook
	Cards    []CardWithBook
	Filtered *AddressBook // the book shown, when one is
	Starred  bool
	CanWrite bool          // a book the user may add contacts to exists
	Writable []AddressBook // the books a new contact may go in
}

// Index shows all contacts, including contacts from shared address books,
// or one book's with ?book=.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	v := &listView{Title: "Contacts", Active: "all", Path: "/apps/contacts/", Books: a.books(), Writable: a.writableBooks()}
	v.CanWrite = len(v.Writable) > 0

	var filter int64 = -1
	if s := r.URL.Query().Get("book"); s != "" {
		if filter, err = strconv.ParseInt(s, 10, 64); err != nil {
			http.Error(w, "invalid book", http.StatusBadRequest)
			return
		}
		for i := range v.Books {
			if v.Books[i].ID == filter {
				v.Filtered = &v.Books[i]
			}
		}
		if v.Filtered == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		v.Title = v.Filtered.Name
		v.Active = s
		v.Path = "/apps/contacts/?book=" + s
	}
	own := a.own
	if filter >= 0 {
		own = map[int64]AddressBook{}
		if b, ok := a.own[filter]; ok {
			own[filter] = b
		}
	}
	store, err := h.plugin.Store(u.Username)
	if err != nil {
		httpError(w, err)
		return
	}
	if v.Cards, err = store.In(own, "", 500); err != nil {
		httpError(w, err)
		return
	}

	// Merge in contacts from shared address books.
	for _, sb := range a.shared {
		if filter >= 0 && filter != sb.Book.ID {
			continue
		}
		owner, err := h.plugin.Store(sb.OwnerUsername)
		if err != nil {
			httpError(w, err)
			return
		}
		sharedCards, err := owner.In(map[int64]AddressBook{sb.Book.ID: sb.Book}, "", 500)
		if err != nil {
			httpError(w, err)
			return
		}
		for i := range sharedCards {
			sharedCards[i].OwnerUsername = sb.OwnerUsername
			sharedCards[i].BookName = sb.Book.Name + " (" + sb.OwnerUsername + ")"
		}
		v.Cards = append(v.Cards, sharedCards...)
	}

	plugin.SetTitle(w, v.Title)
	h.render(w, r, contactsListPage(v, plugin.CSRFToken(r)))
}

// Starred shows starred contacts.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	u, a, err := h.access(r)
	if err != nil {
		httpError(w, err)
		return
	}
	store, err := h.plugin.Store(u.Username)
	if err != nil {
		httpError(w, err)
		return
	}
	v := &listView{Title: "Starred", Active: "starred", Path: "/apps/contacts/starred", Books: a.books(), Starred: true, Writable: a.writableBooks()}
	v.CanWrite = len(v.Writable) > 0
	if v.Cards, err = store.Starred(a.own, 500); err != nil {
		httpError(w, err)
		return
	}
	plugin.SetTitle(w, "Starred Contacts")
	h.render(w, r, contactsListPage(v, plugin.CSRFToken(r)))
}

// writableBooks lists the books a new contact may go in: the user's own,
// then shared ones they may write to.
func (a *access) writableBooks() []AddressBook {
	var out []AddressBook
	for _, b := range a.books() {
		if _, ok := a.own[b.ID]; ok {
			out = append(out, b)
		}
	}
	for _, sb := range a.shared {
		if sb.Perms&plugin.PermWrite != 0 {
			out = append(out, sb.Book)
		}
	}
	return out
}

// render writes a page, logging a failure: the response has begun.
func (h *Pages) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	if err := c.Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render contacts page", "err", err)
	}
}

// Settings shows address book management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	books, err := h.plugin.Books(u.ID)
	if err != nil {
		httpError(w, err)
		return
	}
	davURL := ""
	if h.plugin.dav != nil {
		davURL = strings.TrimSuffix(h.plugin.dav.BaseURL, "/") + "/remote.php/dav/"
	}
	plugin.SetTitle(w, "Contacts Settings")
	h.render(w, r, contactsSettingsPage(books, davURL, u.Username, plugin.CSRFToken(r)))
}

// ContactDetail returns the contact dialog for the page to open.
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
	_, c, book, writable, err := h.card(u, a, id, true)
	if err != nil {
		httpError(w, err)
		return
	}
	d := &detailView{Card: c, Book: book, Writable: writable, CanStar: book.OwnerID == u.ID, Now: h.plugin.ctx.Now()}
	if writable {
		// A card moves only within its owner's books.
		for _, b := range a.writableBooks() {
			if b.OwnerID == book.OwnerID {
				d.Books = append(d.Books, b)
			}
		}
	}
	h.render(w, r, contactDialog(d, plugin.CSRFToken(r)))
}

// detailView is what the contact dialog shows.
type detailView struct {
	Card     *Card
	Book     *AddressBook
	Books    []AddressBook // where the card can move to
	Writable bool
	CanStar  bool
	Now      time.Time
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
	s, _, writable, err := h.book(u, a, bookID)
	if err != nil || !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	f, err := parseContactForm(r)
	if err != nil {
		fail(w, r, err.Error())
		return
	}
	now := h.plugin.ctx.Now()
	c := NewCard(now)
	applyForm(c, f, now)
	nameFromFN(c)
	if _, err := s.Save(bookID, nil, c, now); err != nil {
		httpError(w, err)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Added " + c.FN + "."})
	http.Redirect(w, r, "/apps/contacts/", http.StatusSeeOther)
}

// UpdateContact handles POST to update an existing contact. It changes only
// what the form shows (applyForm). It can move the contact to another book
// in the same database: the user's own, or the same owner's.
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
	s, c, book, writable, err := h.card(u, a, id, true)
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
	f, err := parseContactForm(r)
	if err != nil {
		fail(w, r, err.Error())
		return
	}
	now := h.plugin.ctx.Now()
	applyForm(c.VCard, f, now)
	if _, err := s.Save(bookID, c, c.VCard, now); err != nil {
		httpError(w, err)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Saved " + c.VCard.FN + "."})
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
	s, c, _, writable, err := h.card(u, a, id, false)
	if err != nil {
		httpError(w, err)
		return
	}
	if !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	if err := s.Delete(c, h.plugin.ctx.Now()); err != nil {
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

// UpdateAddressBookHandler handles POST to rename or recolor an address
// book.
func (h *Pages) UpdateAddressBookHandler(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	b, err := h.plugin.Book(u.ID, id)
	if err != nil {
		httpError(w, err)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = b.Name
	}
	color := r.FormValue("color")
	if color == "" {
		color = b.Color
	}
	if err := h.plugin.UpdateBook(u.ID, id, name, color); err != nil {
		httpError(w, err)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Saved " + name + "."})
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

// UploadPhoto handles a contact photo upload. The photo becomes the card's
// PHOTO, re-encoded as a JPEG, so DAV clients get it too.
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
	s, c, _, writable, err := h.card(u, a, id, true)
	if err != nil {
		httpError(w, err)
		return
	}
	if !writable {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		fail(w, r, "The photo is too large (5 MB at most).")
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		fail(w, r, "Choose a photo to upload.")
		return
	}
	defer file.Close()
	if ct := header.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
		fail(w, r, "Only image files can be a contact's photo.")
		return
	}
	img, _, err := safeimage.Decode(file)
	if err != nil {
		fail(w, r, "The photo could not be read as an image.")
		return
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		httpError(w, fmt.Errorf("encode photo: %w", err))
		return
	}
	now := h.plugin.ctx.Now()
	setPhoto(c.VCard, buf.Bytes(), now)
	if _, err := s.Save(c.AddressBookID, c, c.VCard, now); err != nil {
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
	s, c, _, _, err := h.card(u, a, id, false)
	if err != nil {
		httpError(w, err)
		return
	}
	if !c.HasPhoto() {
		http.NotFound(w, r)
		return
	}
	data, ct, err := s.Photo(r.Context(), c)
	if _, ok := errors.AsType[*remoteError](err); ok {
		h.plugin.log.Warn("contact photo unavailable", "card", c.ID, "err", err)
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	if data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if c.PhotoURI == "" {
		// A stored photo's name is its content; a cached one can change.
		w.Header().Set("ETag", `"`+c.PhotoHash+`"`)
	}
	if _, err := w.Write(data); err != nil {
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
	s, c, book, _, err := h.card(u, a, id, false)
	if err != nil {
		httpError(w, err)
		return
	}
	if book.OwnerID != u.ID {
		// Starring is the owner's own; a sharee's star would land on the
		// owner's card.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	newStarred := !c.IsStarred
	if err := s.SetStarred(c.ID, newStarred); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return updated star button for HTMX swap.
	ui.Render(w, r, ui.Star(newStarred, fmt.Sprintf("/apps/contacts/contacts/%d/star", id)))
}
