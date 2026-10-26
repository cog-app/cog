package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// AddressBook is a DAV address book collection, as the contacts UI shows it.
// Collections belong to the DAV core (internal/dav); this is a view of one.
type AddressBook struct {
	ID          int64  // dav.CollectionID
	UID         string // the collection's path name
	Name        string // display name
	Description string
	Color       string
	SortOrder   int
	OwnerID     string
}

func bookFrom(c *dav.Collection) AddressBook {
	return AddressBook{
		ID: int64(c.ID), UID: c.Name, Name: c.Title(), Description: c.Description,
		Color: c.Color, SortOrder: c.SortOrder, OwnerID: c.OwnerID,
	}
}

// Card is a single contact card with indexed fields and raw vCard blob.
type Card struct {
	ID            int64
	AddressBookID int64 // dav.CollectionID
	UID           string
	URI           string
	Version       int64
	FN            string
	Email         string
	Tel           string
	Org           string
	Title         string
	Note          string
	EmailsJSON    string
	PhonesJSON    string
	AddressesJSON string
	PhotoPath     string
	IsStarred     bool
	VCardData     string
	Rev           string
	CreatedAt     int64
	UpdatedAt     int64
}

// CardWithBook extends Card with address book metadata for display.
type CardWithBook struct {
	Card
	BookName      string
	BookColor     string
	OwnerUsername string // non-empty when from a shared address book
}

// cardColumns is the list of columns selected for card queries.
const cardColumns = `id, collection_id, uid, uri, version, fn, email, tel, org, title, note, emails_json, phones_json, addresses_json, photo_path, is_starred, vcard_data, rev, created_at, updated_at`

func scanCard(row interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	if err := row.Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.Version, &c.FN, &c.Email, &c.Tel,
		&c.Org, &c.Title, &c.Note, &c.EmailsJSON, &c.PhonesJSON, &c.AddressesJSON,
		&c.PhotoPath, &c.IsStarred, &c.VCardData, &c.Rev, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func queryCards(db *sql.DB, query string, args ...any) ([]Card, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query cards: %w", err)
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan card: %w", err)
		}
		cards = append(cards, *c)
	}
	return cards, rows.Err()
}

// --- Card queries ---

func ListCards(db *sql.DB, bookID int64) ([]Card, error) {
	return queryCards(db, `SELECT `+cardColumns+` FROM cards WHERE collection_id = ? ORDER BY fn`, bookID)
}

func GetCardByUID(db *sql.DB, bookID int64, uid string) (*Card, error) {
	return scanCard(db.QueryRow(`SELECT `+cardColumns+` FROM cards WHERE collection_id = ? AND uid = ?`, bookID, uid))
}

func GetCardByURI(db *sql.DB, bookID int64, uri string) (*Card, error) {
	return scanCard(db.QueryRow(`SELECT `+cardColumns+` FROM cards WHERE collection_id = ? AND uri = ?`, bookID, uri))
}

func GetCard(db *sql.DB, id int64) (*Card, error) {
	return scanCard(db.QueryRow(`SELECT `+cardColumns+` FROM cards WHERE id = ?`, id))
}

// FindCardByUID returns a card by its vCard UID across all address books.
func FindCardByUID(db *sql.DB, uid string) (*Card, error) {
	return scanCard(db.QueryRow(`SELECT `+cardColumns+` FROM cards WHERE uid = ? LIMIT 1`, uid))
}

// inBooks limits a query to the given address books; nil means every card in
// the database. Rows of a deleted collection whose purge failed are never in
// the map, so they stay invisible.
func inBooks(books map[int64]AddressBook) (string, []any) {
	if books == nil {
		return "", nil
	}
	if len(books) == 0 {
		return " AND 0", nil
	}
	args := make([]any, 0, len(books))
	for id := range books {
		args = append(args, id)
	}
	return " AND collection_id IN (?" + strings.Repeat(",?", len(args)-1) + ")", args
}

func withBooks(cards []Card, books map[int64]AddressBook) []CardWithBook {
	out := make([]CardWithBook, len(cards))
	for i, c := range cards {
		b := books[c.AddressBookID]
		out[i] = CardWithBook{Card: c, BookName: b.Name, BookColor: b.Color}
	}
	return out
}

// CardsIn returns the cards in books, by name; filter is an extra SQL
// condition on cards ("" for none) with its arguments.
func CardsIn(db *sql.DB, books map[int64]AddressBook, filter string, limit int, args ...any) ([]CardWithBook, error) {
	in, inArgs := inBooks(books)
	where := "1"
	if filter != "" {
		where = filter
	}
	cards, err := queryCards(db, `SELECT `+cardColumns+` FROM cards WHERE `+where+in+` ORDER BY fn LIMIT ?`,
		append(append(args, inArgs...), limit)...)
	if err != nil {
		return nil, err
	}
	return withBooks(cards, books), nil
}

// StarredContacts returns starred contacts in books.
func StarredContacts(db *sql.DB, books map[int64]AddressBook, limit int) ([]CardWithBook, error) {
	return CardsIn(db, books, "is_starred = 1", limit)
}

// SearchCards returns cards in books (nil for all) matching a name query.
func SearchCards(db *sql.DB, books map[int64]AddressBook, query string, limit int) ([]CardWithBook, error) {
	return CardsIn(db, books, "fn LIKE '%' || ? || '%'", limit, query)
}

// SetStarred sets or clears the starred flag on a contact. Starring is Cog's
// own and not in the vCard, so it does not change the card's version.
func SetStarred(db *sql.DB, cardID int64, starred bool) error {
	_, err := db.Exec(`UPDATE cards SET is_starred = ? WHERE id = ?`, starred, cardID)
	return err
}

// SetPhotoPath records where a contact's photo is stored. The photo is not in
// the stored vCard yet, so it does not change the card's version.
func SetPhotoPath(db *sql.DB, cardID int64, path string) error {
	if _, err := db.Exec(`UPDATE cards SET photo_path = ? WHERE id = ?`, path, cardID); err != nil {
		return fmt.Errorf("set photo path: %w", err)
	}
	return nil
}

// --- Writes ---
//
// Every write to a vCard bumps the collection's version in the same
// transaction (davsql), whether it comes from DAV or the web UI.

// errUIDConflict is returned when another card in the book has the UID.
type errUIDConflict struct{ uri string }

func (e *errUIDConflict) Error() string { return "UID already used by " + e.uri }

// putCard stores a vCard at uri inside tx and returns its ID, version and
// whether it was created. fields must be parsed from vcard.
func putCard(tx *sql.Tx, bookID int64, uri, uid, vcard string, fields VCardFields, cond davsql.Conditions, now int64) (id, version int64, created bool, err error) {
	var cur *int64
	var curVersion int64
	switch err := tx.QueryRow(`SELECT version FROM cards WHERE collection_id = ? AND uri = ?`, bookID, uri).Scan(&curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, false, fmt.Errorf("read card %s: %w", uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return 0, 0, false, dav.ErrPrecondition
	}
	var other string
	switch err := tx.QueryRow(`SELECT uri FROM cards WHERE collection_id = ? AND uid = ? AND uri != ?`, bookID, uid, uri).Scan(&other); {
	case err == nil:
		return 0, 0, false, &errUIDConflict{other}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, false, fmt.Errorf("check UID %s: %w", uid, err)
	}
	version, err = davsql.Bump(tx, bookID)
	if err != nil {
		return 0, 0, false, err
	}
	err = tx.QueryRow(`INSERT INTO cards (collection_id, uid, uri, version, fn, email, tel, org, title, note,
			emails_json, phones_json, addresses_json, vcard_data, rev, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			uid = excluded.uid, version = excluded.version, fn = excluded.fn, email = excluded.email,
			tel = excluded.tel, org = excluded.org, title = excluded.title, note = excluded.note,
			emails_json = excluded.emails_json, phones_json = excluded.phones_json,
			addresses_json = excluded.addresses_json, vcard_data = excluded.vcard_data,
			rev = excluded.rev, updated_at = excluded.updated_at
		RETURNING id`,
		bookID, uid, uri, version, fields.FN, fields.Email, fields.Tel, fields.Org, fields.Title, fields.Note,
		fields.EmailsJSON, fields.PhonesJSON, fields.AddressesJSON, vcard, fields.Rev, now, now).Scan(&id)
	if err != nil {
		return 0, 0, false, fmt.Errorf("store card %s: %w", uri, err)
	}
	if err := davsql.Revive(tx, bookID, uri); err != nil {
		return 0, 0, false, err
	}
	return id, version, cur == nil, nil
}

// deleteCard removes the card at uri inside tx and leaves a tombstone.
func deleteCard(tx *sql.Tx, bookID int64, uri string, cond davsql.Conditions, now int64) error {
	var version int64
	err := tx.QueryRow(`SELECT version FROM cards WHERE collection_id = ? AND uri = ?`, bookID, uri).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return dav.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read card %s: %w", uri, err)
	}
	if err := cond.Check(&version); err != nil {
		return dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM cards WHERE collection_id = ? AND uri = ?`, bookID, uri); err != nil {
		return fmt.Errorf("delete card %s: %w", uri, err)
	}
	_, err = davsql.Delete(tx, bookID, uri, now)
	return err
}

// SaveCard stores a vCard written by Cog (web UI, other plugins, tests) in
// an address book. from is the card being edited, or nil for a new one;
// moving it to another book deletes it from the old one in the same
// transaction.
func SaveCard(db *sql.DB, bookID int64, from *Card, vcard string, now time.Time) (*Card, error) {
	fields, err := ParseVCard(vcard)
	if err != nil {
		return nil, err
	}
	uri := fields.UID + ".vcf"
	if from != nil {
		uri = from.URI
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("save card: %w", err)
	}
	defer tx.Rollback()
	if from != nil && from.AddressBookID != bookID {
		if err := deleteCard(tx, from.AddressBookID, from.URI, davsql.Conditions{}, now.Unix()); err != nil {
			return nil, err
		}
	}
	id, _, _, err := putCard(tx, bookID, uri, fields.UID, vcard, fields, davsql.Conditions{}, now.Unix())
	if err != nil {
		return nil, err
	}
	if from != nil && from.AddressBookID != bookID {
		// Cog's own columns follow the card.
		if _, err := tx.Exec(`UPDATE cards SET is_starred = ?, photo_path = ? WHERE id = ?`, from.IsStarred, from.PhotoPath, id); err != nil {
			return nil, fmt.Errorf("move card: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("save card: %w", err)
	}
	return GetCard(db, id)
}

// DeleteCardByID removes a card and leaves a tombstone for DAV clients.
func DeleteCardByID(db *sql.DB, id int64, now time.Time) error {
	c, err := GetCard(db, id)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete card: %w", err)
	}
	defer tx.Rollback()
	if err := deleteCard(tx, c.AddressBookID, c.URI, davsql.Conditions{}, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
