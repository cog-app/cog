package dav

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/plugin"
)

// The birthday calendar: one read-only calendar per user, derived from the
// birthdays and anniversaries of the contacts in their own address books,
// with one yearly all-day event per date. DESIGN/dav-storage.md section 9.
//
// It is a real collection whose events are stored as rows by the backend
// that stores events, so the web UI, sync, feeds and sharing treat it like
// any calendar. It follows the address books by their sync state: for each
// book it keeps the version it has read (dav_birthday_cursors) and, when
// asked to catch up, reads only the cards changed and deleted since, so a
// card write costs a few queries however large the book, and a sync that
// failed or was skipped is made good by the next one.

// BirthdaysName is the path name of a user's birthday calendar.
const BirthdaysName = "contact_birthdays"

// CardDates is what the birthday calendar reads of a card.
type CardDates struct {
	URI  string
	Name string // FN, or what the card would be listed by
	// Birthday and Anniversary are the values as written, with their
	// parameters (Apple's X-APPLE-OMIT-YEAR). Anniversary is ANNIVERSARY,
	// or for a 3.0 card that has none, the vendor property clients write
	// instead (X-ANNIVERSARY, X-EVOLUTION-ANNIVERSARY, X-MS-ANNIVERSARY,
	// X-WAB-WEDDING_ANNIVERSARY).
	Birthday          string
	BirthdayParams    []contentline.Param
	Anniversary       string
	AnniversaryParams []contentline.Param
}

// DateSource is implemented by the backend that stores cards.
type DateSource interface {
	// CardDates returns the cards among uris (every card when uris is nil)
	// that exist in c.
	CardDates(owner string, c *Collection, uris []string) ([]CardDates, error)
}

// birthdayState serialises a user's birthday syncs within the process
// and holds them back during an import.
type birthdayState struct {
	mu   sync.Mutex
	held int
}

func (s *Server) birthdays(owner string) *birthdayState {
	v, _ := s.birthdayStates.LoadOrStore(owner, &birthdayState{})
	return v.(*birthdayState)
}

// holdBirthdays defers the owner's birthday syncs until the returned
// function is called, which syncs once: an import of many cards then reads
// them all in one pass.
func (s *Server) holdBirthdays(owner string) func() error {
	st := s.birthdays(owner)
	st.mu.Lock()
	st.held++
	st.mu.Unlock()
	return func() error {
		st.mu.Lock()
		st.held--
		st.mu.Unlock()
		return s.CardsChanged(owner)
	}
}

// Birthdays returns the user's birthday calendar, or nil when they have none.
func (s *Server) Birthdays(ownerID string) (*Collection, error) {
	cols, err := s.Collections.OfKind(ownerID, KindBirthdays)
	if err != nil || len(cols) == 0 {
		return nil, err
	}
	return &cols[0], nil
}

// EnableBirthdays creates the user's birthday calendar, filled from their
// contacts, or returns the one they have.
func (s *Server) EnableBirthdays(owner *User) (*Collection, error) {
	if c, err := s.Birthdays(owner.ID); err != nil || c != nil {
		return c, err
	}
	c := &Collection{
		OwnerID: owner.ID, Home: HomeCalendars, Kind: KindBirthdays, Name: BirthdaysName,
		Components: []Component{VEVENT}, DisplayName: "Birthdays", Color: "#e9322d",
	}
	if err := s.Collections.CreateNamed(c); err != nil {
		return nil, err
	}
	if err := s.syncBirthdaysOf(owner); err != nil {
		return c, err
	}
	return c, nil
}

// DisableBirthdays deletes the user's birthday calendar, if they have one.
func (s *Server) DisableBirthdays(owner *User) error {
	c, err := s.Birthdays(owner.ID)
	if err != nil || c == nil {
		return err
	}
	return s.DeleteCollection(owner.Username, c)
}

// CardsChanged follows a write to the owner's cards, from DAV, the web UI or
// an import: their birthday calendar, if they have one, and every other
// user's that reads one of their address books (shared with them) catch
// up. Backends call it after every card write they commit.
func (s *Server) CardsChanged(ownerUsername string) error {
	if !s.mounted() {
		return nil
	}
	u, err := s.Users.ByUsername(ownerUsername)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	st := s.birthdays(u.Username)
	st.mu.Lock()
	held := st.held > 0
	st.mu.Unlock()
	if held {
		return nil // the import syncs once it is done
	}
	errs := []error{s.syncBirthdaysOf(u)}
	readers, err := s.Collections.birthdayReaders(u.ID)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range readers {
		errs = append(errs, s.SyncBirthdays(id))
	}
	return errors.Join(errs...)
}

// SyncBirthdays brings one user's birthday calendar, if they have one, up
// to date with the address books they own and those shared with them.
func (s *Server) SyncBirthdays(userID string) error {
	if !s.mounted() {
		return nil
	}
	u, err := s.Users.ByID(userID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.syncBirthdaysOf(u)
}

// SweepBirthdays brings every birthday calendar up to date. Shares reach
// users through groups, whose members change without any card or share
// changing; the calendar plugin sweeps now and then for them.
func (s *Server) SweepBirthdays() error {
	cols, err := s.Collections.query(selectCollections+` WHERE c.kind = ? ORDER BY c.id`, KindBirthdays)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cols {
		errs = append(errs, s.SyncBirthdays(c.OwnerID))
	}
	return errors.Join(errs...)
}

// ShareChanged follows a share of collection id made, changed or deleted:
// push subscribers learn that the collection's privileges and the homes it
// appears in changed; and when it shares an address book, the birthday
// calendars of the users it reaches follow. A share with one user syncs
// theirs; a group's or everyone's, all of them.
func (s *Server) ShareChanged(subjectType, subjectID string, id CollectionID) error {
	c, err := s.Collections.ByID(id)
	if errors.Is(err, ErrNotFound) {
		// A deleted collection: deleting it already told its readers.
		return nil
	}
	if err != nil {
		return err
	}
	if subjectType != "PUBLIC" {
		s.sharePushChanged(subjectType, subjectID, c)
	}
	if c.Home != HomeAddressBooks {
		return nil
	}
	switch subjectType {
	case "USER":
		return s.SyncBirthdays(subjectID)
	case "PUBLIC":
		return nil // a link reaches no user's calendar
	}
	return s.SweepBirthdays()
}

// mounted reports whether the host has wired the server: without it no
// plugin stores DAV data.
func (s *Server) mounted() bool { return s.Users != nil && s.Backends != nil }

func (s *Server) syncBirthdaysOf(u *User) error {
	st := s.birthdays(u.Username)
	st.mu.Lock()
	defer st.mu.Unlock()
	return s.syncBirthdays(u)
}

// birthdayBook is one address book's part of a sync.
type birthdayBook struct {
	id      CollectionID
	ownerID string // the book's owner
	full    bool   // every card read: events of cards not among them go
	cards   []CardDates
	gone    []string // URIs of cards deleted since the cursor
	cursor  int64    // the version read
}

// readable is an address book the birthday calendar reads, with its
// owner's username, which names the database its cards are in.
type readable struct {
	Collection
	owner string
}

func (s *Server) syncBirthdays(u *User) error {
	bc, err := s.Birthdays(u.ID)
	if err != nil || bc == nil {
		return err
	}
	var events, cards Backend
	var dates DateSource
	for _, b := range s.Backends() {
		if slices.Contains(b.Components(), VEVENT) {
			events = b
		}
		if ds, ok := b.(DateSource); ok {
			cards, dates = b, ds
		}
	}
	if events == nil || cards == nil {
		return nil // caught up when both are enabled again
	}
	own, err := s.Collections.List(u.ID, HomeAddressBooks)
	if err != nil {
		return err
	}
	var books []readable
	for _, c := range own {
		books = append(books, readable{c, u.Username})
	}
	shared, err := s.Collections.Shared(s.Shares, s.username, u.ID, VCARD)
	if err != nil {
		return err
	}
	for _, sc := range shared {
		if sc.Permissions&plugin.PermRead != 0 {
			books = append(books, readable{sc.Collection, sc.OwnerUsername})
		}
	}
	cursors, err := s.Collections.birthdayCursors(bc.ID)
	if err != nil {
		return err
	}
	// One query per owner of the books read.
	byOwner := map[string][]CollectionID{}
	for _, b := range books {
		byOwner[b.owner] = append(byOwner[b.owner], b.ID)
	}
	states := map[CollectionID]davsql.State{}
	for owner, ids := range byOwner {
		st, err := cards.States(owner, ids)
		if err != nil {
			return err
		}
		maps.Copy(states, st)
	}

	var work []birthdayBook
	for i := range books {
		book := &books[i]
		st := states[book.ID]
		cur, had := cursors[book.ID]
		delete(cursors, book.ID)
		if had && cur == st.Token {
			continue
		}
		bb := birthdayBook{id: book.ID, ownerID: book.OwnerID, cursor: st.Token}
		if !had || cur < st.Floor || cur > st.Token {
			bb.full = true
			if bb.cards, err = dates.CardDates(book.owner, &book.Collection, nil); err != nil {
				return err
			}
		} else {
			ch, err := cards.Changes(book.owner, &book.Collection, cur, false)
			if err != nil {
				return err
			}
			bb.cursor, bb.gone = ch.State.Token, ch.Deleted
			uris := make([]string, len(ch.Changed))
			for i, r := range ch.Changed {
				uris[i] = r.URI
			}
			if len(uris) > 0 {
				if bb.cards, err = dates.CardDates(book.owner, &book.Collection, uris); err != nil {
					return err
				}
			}
			// A card changed and then deleted is among the deletions.
			for _, uri := range uris {
				if !slices.ContainsFunc(bb.cards, func(c CardDates) bool { return c.URI == uri }) {
					bb.gone = append(bb.gone, uri)
				}
			}
		}
		work = append(work, bb)
	}
	// Books that are gone, or no longer shared, take their events with
	// them.
	var removed []CollectionID
	for id := range cursors {
		removed = append(removed, id)
	}
	owner := u.Username
	if len(work) == 0 && len(removed) == 0 {
		return nil
	}

	want := map[string]*PutRequest{}
	var candidates []string // URIs that may hold an event to replace or delete
	wholeBooks := map[CollectionID]bool{}
	for _, id := range removed {
		wholeBooks[id] = true
	}
	for _, bb := range work {
		if bb.full {
			wholeBooks[bb.id] = true
		}
		for _, c := range bb.cards {
			for _, ev := range birthdayEvents(bb.id, c) {
				req, err := ev.request()
				if err != nil {
					return err
				}
				want[req.URI] = req
			}
			candidates = append(candidates, birthdayURIs(bb.id, c.URI)...)
		}
		for _, uri := range bb.gone {
			candidates = append(candidates, birthdayURIs(bb.id, uri)...)
		}
	}
	have := map[string][]byte{}
	if len(wholeBooks) > 0 {
		// Listing the calendar names what each whole book had.
		list, err := events.List(owner, bc, false)
		if err != nil {
			return err
		}
		for _, r := range list {
			if id, ok := birthdayBookOf(r.URI); ok && wholeBooks[id] {
				candidates = append(candidates, r.URI)
			}
		}
	}
	if len(candidates) > 0 {
		list, err := events.Get(owner, bc, candidates, true)
		if err != nil {
			return err
		}
		for _, r := range list {
			have[r.URI] = r.Data
		}
	}
	var puts []*PutRequest
	for _, uri := range slices.Sorted(func(yield func(string) bool) {
		for uri := range want {
			if !yield(uri) {
				return
			}
		}
	}) {
		if data, ok := have[uri]; !ok || string(data) != string(want[uri].Data) {
			puts = append(puts, want[uri])
		}
	}
	var deletes []string
	for uri := range have {
		if _, ok := want[uri]; !ok {
			deletes = append(deletes, uri)
		}
	}
	slices.Sort(deletes)
	if err := writeBatch(events, owner, bc, puts, deletes); err != nil {
		return err
	}
	return s.Collections.saveBirthdayCursors(bc.ID, work, removed)
}

// birthdayCursors returns the version the birthday calendar has read of
// each address book.
func (s *Store) birthdayCursors(id CollectionID) (map[CollectionID]int64, error) {
	rows, err := s.db.Query(`SELECT book_id, version FROM dav_birthday_cursors WHERE collection_id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("birthday cursors: %w", err)
	}
	defer rows.Close()
	out := map[CollectionID]int64{}
	for rows.Next() {
		var book CollectionID
		var v int64
		if err := rows.Scan(&book, &v); err != nil {
			return nil, fmt.Errorf("birthday cursors: %w", err)
		}
		out[book] = v
	}
	return out, rows.Err()
}

// birthdayReaders returns the users other than ownerID whose birthday
// calendars read one of ownerID's address books.
func (s *Store) birthdayReaders(ownerID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT c.owner_user_id FROM dav_birthday_cursors b
		JOIN dav_collections c ON c.id = b.collection_id
		WHERE b.book_owner_id = ? AND c.owner_user_id != ?`, ownerID, ownerID)
	if err != nil {
		return nil, fmt.Errorf("birthday readers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("birthday readers: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) saveBirthdayCursors(id CollectionID, work []birthdayBook, removed []CollectionID) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("save birthday cursors: %w", err)
	}
	defer tx.Rollback()
	for _, bb := range work {
		if _, err := tx.Exec(`INSERT INTO dav_birthday_cursors (collection_id, book_id, book_owner_id, version) VALUES (?, ?, ?, ?)
			ON CONFLICT(collection_id, book_id) DO UPDATE SET version = excluded.version`, id, bb.id, bb.ownerID, bb.cursor); err != nil {
			return fmt.Errorf("save birthday cursor: %w", err)
		}
	}
	for _, book := range removed {
		if _, err := tx.Exec(`DELETE FROM dav_birthday_cursors WHERE collection_id = ? AND book_id = ?`, id, book); err != nil {
			return fmt.Errorf("drop birthday cursor: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save birthday cursors: %w", err)
	}
	return nil
}

// Birthday event kinds, which end their URIs.
const (
	kindBirthday    = "bday"
	kindAnniversary = "anniversary"
)

// birthdayStem names the event for one date of a card: by the address
// book and the card's URI, never its content, so editing a card replaces
// its event in place and clients keep their copy.
func birthdayStem(book CollectionID, cardURI, kind string) string {
	sum := sha256.Sum256([]byte(cardURI))
	return "b" + book.String() + "-" + hex.EncodeToString(sum[:12]) + "-" + kind
}

// birthdayURIs are the URIs a card's events may have.
func birthdayURIs(book CollectionID, cardURI string) []string {
	return []string{
		birthdayStem(book, cardURI, kindBirthday) + ".ics",
		birthdayStem(book, cardURI, kindAnniversary) + ".ics",
	}
}

// birthdayBookOf returns the address book a birthday event's URI names.
func birthdayBookOf(uri string) (CollectionID, bool) {
	rest, ok := strings.CutPrefix(uri, "b")
	if !ok {
		return 0, false
	}
	id, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return CollectionID(n), err == nil
}

// birthdayEvent is one yearly event of a card.
type birthdayEvent struct {
	stem    string
	summary string
	date    vcard.Date
}

func birthdayEvents(book CollectionID, c CardDates) []birthdayEvent {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "Contact"
	}
	var out []birthdayEvent
	if d, ok := vcard.ParseDate(c.Birthday, c.BirthdayParams); ok {
		out = append(out, birthdayEvent{birthdayStem(book, c.URI, kindBirthday), "🎂 " + name, d})
	}
	if d, ok := vcard.ParseDate(c.Anniversary, c.AnniversaryParams); ok {
		out = append(out, birthdayEvent{birthdayStem(book, c.URI, kindAnniversary), "💍 " + name, d})
	}
	return out
}

// request builds the event as Cog stores and serves it: an all-day event
// on the date, yearly. A known year is in the summary and starts the
// series (from 1900 at the earliest, since recurrence is not expanded much
// further than that); without one the series starts in 1970. A birthday on
// February 29 falls on the last day of February in other years.
func (ev birthdayEvent) request() (*PutRequest, error) {
	year := ev.date.Year
	summary := ev.summary
	if year != 0 {
		summary += " (" + strconv.Itoa(year) + ")"
	}
	switch {
	case year == 0 && ev.date.Month == 2 && ev.date.Day == 29:
		year = 1972
	case year == 0:
		year = 1970
	case year < 1900 && ev.date.Month == 2 && ev.date.Day == 29:
		year = 1904
	case year < 1900:
		year = 1900
	}
	first := time.Date(year, ev.date.Month, ev.date.Day, 0, 0, 0, 0, time.UTC)
	start, next := first.Format("20060102"), first.AddDate(0, 0, 1).Format("20060102")
	rule := "FREQ=YEARLY"
	if ev.date.Month == time.February && ev.date.Day == 29 {
		rule = "FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=-1"
	}
	date := []contentline.Param{{Name: "VALUE", Values: []string{"DATE"}}}
	cal := &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{
		{Name: "VERSION", Value: "2.0"},
		{Name: "PRODID", Value: "-//Cog//Birthdays//EN"},
	}, Components: []*contentline.Component{{Name: "VEVENT", Props: []contentline.Property{
		{Name: "UID", Value: ev.stem},
		// Fixed, so regenerating an unchanged card changes nothing.
		{Name: "DTSTAMP", Value: start + "T000000Z"},
		{Name: "DTSTART", Params: date, Value: start},
		{Name: "DTEND", Params: date, Value: next},
		{Name: "RRULE", Value: rule},
		{Name: "SUMMARY", Value: escapeText(summary)},
		{Name: "TRANSP", Value: "TRANSPARENT"},
		{Name: "CLASS", Value: "PUBLIC"},
	}}}}
	o, err := ical.Decode(cal, ical.VEVENT)
	if err != nil {
		return nil, fmt.Errorf("birthday event %s: %w", ev.stem, err)
	}
	enc, data, err := encodeObject(o)
	if err != nil {
		return nil, err
	}
	return &PutRequest{URI: ev.stem + ".ics", UID: ev.stem, Component: VEVENT, Data: data, Object: enc}, nil
}
