package contacts

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
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

// Card is one contact as lists show it, from its rows; VCard holds the
// whole card when it was loaded.
type Card struct {
	ID            int64
	AddressBookID int64 // dav.CollectionID
	UID           string
	URI           string
	Version       int64
	FN            string // the name shown: FN, or N, or the organisation
	Email         string // the preferred email, or the first
	Tel           string // the preferred phone, or the first
	Org           string
	Title         string
	PhotoHash     string // "" without a photo
	PhotoURI      string // the photo's URL, when it is given by one
	IsStarred     bool
	CreatedAt     int64
	UpdatedAt     int64

	VCard *vcard.Card // nil unless loaded
}

// HasPhoto reports whether the card has a photo.
func (c *Card) HasPhoto() bool { return c.PhotoHash != "" }

// CardWithBook extends Card with address book metadata for display.
type CardWithBook struct {
	Card
	BookName      string
	BookColor     string
	OwnerUsername string // non-empty when from a shared address book
}

// Store is one user's contacts: their database and their photo files. Every
// write goes through it, so the photo files follow the rows.
type Store struct {
	DB     *sql.DB
	photos *photoStore
	fetch  *http.Client // for photos given by URL
	log    *slog.Logger
}

// Store returns a user's contacts.
func (p *Plugin) Store(username string) (*Store, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return &Store{DB: db, photos: p.photoStore(username), fetch: p.fetch, log: p.log}, nil
}

// preferred orders a card's values: by PREF, then TYPE=pref, then as
// written.
const preferred = `ORDER BY CASE WHEN pref > 0 THEN pref ELSE 101 END, instr(lower(types), '"pref"') = 0, position LIMIT 1`

// cardColumns are the columns of Card, from cards c.
const cardColumns = `c.id, c.collection_id, c.uid, c.uri, c.version, c.fn, c.n_given, c.n_family, c.org, c.title,
	coalesce((SELECT value FROM card_emails WHERE card_id = c.id ` + preferred + `), ''),
	coalesce((SELECT value FROM card_phones WHERE card_id = c.id ` + preferred + `), ''),
	c.photo_hash, c.photo_uri, c.is_starred, c.created_at, c.updated_at`

func scanCard(row interface{ Scan(...any) error }) (*Card, error) {
	var c Card
	var given, family sql.NullString
	if err := row.Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.Version, &c.FN, &given, &family, &c.Org, &c.Title,
		&c.Email, &c.Tel, &c.PhotoHash, &c.PhotoURI, &c.IsStarred, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	if c.FN == "" {
		n := vcard.Name{Given: listOf(given.String), Family: listOf(family.String)}
		c.FN = n.Formatted()
	}
	if c.FN == "" {
		c.FN = c.Org
	}
	return &c, nil
}

func queryCards(q davsql.Querier, where string, args ...any) ([]Card, error) {
	rows, err := q.Query(`SELECT `+cardColumns+` FROM cards c WHERE `+where, args...)
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

// List returns the cards in an address book, by name.
func (s *Store) List(bookID int64) ([]Card, error) {
	return queryCards(s.DB, `c.collection_id = ? ORDER BY c.fn, c.id`, bookID)
}

// Get returns a card by row ID, with its vCard. It returns sql.ErrNoRows
// when there is none.
func (s *Store) Get(id int64) (*Card, error) {
	c, err := s.Row(id)
	if err != nil {
		return nil, err
	}
	return c, s.Load(c)
}

// Row returns a card by row ID, without its vCard.
func (s *Store) Row(id int64) (*Card, error) {
	return scanCard(s.DB.QueryRow(`SELECT `+cardColumns+` FROM cards c WHERE c.id = ?`, id))
}

// FindByUID returns a card by its vCard UID in any address book, without
// its vCard.
func (s *Store) FindByUID(uid string) (*Card, error) {
	return scanCard(s.DB.QueryRow(`SELECT `+cardColumns+` FROM cards c WHERE c.uid = ? ORDER BY c.id LIMIT 1`, uid))
}

// Load reads a card's vCard, photo included.
func (s *Store) Load(c *Card) error {
	cards, err := loadCards(s.DB, s.photos, []int64{c.ID})
	if err != nil {
		return err
	}
	if c.VCard = cards[c.ID]; c.VCard == nil {
		return sql.ErrNoRows
	}
	return nil
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
	return " AND c.collection_id IN (?" + strings.Repeat(",?", len(args)-1) + ")", args
}

func withBooks(cards []Card, books map[int64]AddressBook) []CardWithBook {
	out := make([]CardWithBook, len(cards))
	for i, c := range cards {
		b := books[c.AddressBookID]
		out[i] = CardWithBook{Card: c, BookName: b.Name, BookColor: b.Color}
	}
	return out
}

// In returns the cards in books, by name; filter is an extra SQL condition
// on cards c ("" for none) with its arguments.
func (s *Store) In(books map[int64]AddressBook, filter string, limit int, args ...any) ([]CardWithBook, error) {
	in, inArgs := inBooks(books)
	where := "1"
	if filter != "" {
		where = filter
	}
	cards, err := queryCards(s.DB, where+in+` ORDER BY c.fn, c.id LIMIT ?`, append(append(args, inArgs...), limit)...)
	if err != nil {
		return nil, err
	}
	return withBooks(cards, books), nil
}

// Starred returns starred contacts in books.
func (s *Store) Starred(books map[int64]AddressBook, limit int) ([]CardWithBook, error) {
	return s.In(books, "c.is_starred = 1", limit)
}

// Search returns cards in books (nil for all) whose name matches a query.
func (s *Store) Search(books map[int64]AddressBook, query string, limit int) ([]CardWithBook, error) {
	return s.In(books, "c.fn LIKE '%' || ? || '%'", limit, query)
}

// SetStarred sets or clears the starred flag on a contact. Starring is Cog's
// own and not in the vCard, so it does not change the card's version.
func (s *Store) SetStarred(cardID int64, starred bool) error {
	if _, err := s.DB.Exec(`UPDATE cards SET is_starred = ? WHERE id = ?`, starred, cardID); err != nil {
		return fmt.Errorf("star card: %w", err)
	}
	return nil
}

// Photo returns a card's photo and its MIME type, sniffed from the bytes:
// nil when it has none, or none that is an image. A photo given by URL is
// fetched (and cached); when that fails the error is a *remoteError.
func (s *Store) Photo(ctx context.Context, c *Card) ([]byte, string, error) {
	var data []byte
	var err error
	if c.PhotoURI != "" {
		data, err = s.photos.remote(ctx, s.fetch, c.PhotoHash, c.PhotoURI)
	} else {
		data, err = s.photos.read(c.PhotoHash)
	}
	if err != nil || data == nil || !isImage(data) {
		return nil, "", err
	}
	return data, http.DetectContentType(data), nil
}

// --- Writes ---

// write runs fn in a transaction, then removes the photo files fn reports
// no card may use any more. The rows are committed whether or not that
// cleanup succeeds; a failure is logged, and leaves a file behind.
func (s *Store) write(fn func(tx *sql.Tx) (garbage []string, err error)) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("write contacts: %w", err)
	}
	defer tx.Rollback()
	garbage, err := fn(tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write contacts: %w", err)
	}
	if err := s.collect(garbage); err != nil {
		s.log.Error("remove unused contact photos", "err", err)
	}
	return nil
}

// collect removes the files of photos no card uses, holding the database's
// write lock while it checks and removes them. A card write stores its photo
// file only once it holds that lock too (storeCard), so the two never
// interleave, whichever process or replica runs them.
func (s *Store) collect(hashes []string) error {
	hashes = slices.DeleteFunc(slices.Clone(hashes), func(h string) bool { return h == "" })
	if len(hashes) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A write that changes nothing still takes the write lock.
	if _, err := tx.Exec(`UPDATE cards SET photo_hash = photo_hash WHERE 0`); err != nil {
		return err
	}
	if err := s.photos.collect(tx, hashes...); err != nil {
		return err
	}
	return tx.Commit()
}

// put stores a card from DAV.
func (s *Store) put(collID int64, uri string, c *vcard.Card, cond davsql.Conditions, now time.Time) (*storedCard, error) {
	var st *storedCard
	err := s.write(func(tx *sql.Tx) ([]string, error) {
		var err error
		st, err = storeCard(tx, s.photos, collID, uri, c, cond, now.Unix())
		if err != nil {
			return nil, err
		}
		return []string{st.Garbage}, nil
	})
	return st, err
}

// Save stores a card written by Cog (web UI, other plugins, tests) in an
// address book. from is the card being edited, or nil for a new one; moving
// it to another book deletes it from the old one in the same transaction,
// and Cog's own columns follow it.
func (s *Store) Save(bookID int64, from *Card, c *vcard.Card, now time.Time) (*Card, error) {
	if c.UID == "" {
		return nil, fmt.Errorf("card has no UID")
	}
	uri := c.UID + ".vcf"
	cond := davsql.Conditions{IfNoneMatchAny: true}
	if from != nil {
		uri, cond = from.URI, davsql.Conditions{}
	}
	var id int64
	err := s.write(func(tx *sql.Tx) ([]string, error) {
		var garbage []string
		if from != nil && from.AddressBookID != bookID {
			old, err := deleteCard(tx, from.AddressBookID, from.URI, davsql.Conditions{}, now.Unix())
			if err != nil {
				return nil, err
			}
			garbage = append(garbage, old)
		}
		st, err := storeCard(tx, s.photos, bookID, uri, c, cond, now.Unix())
		if err != nil {
			return nil, err
		}
		id = st.ID
		if from != nil && from.AddressBookID != bookID {
			if _, err := tx.Exec(`UPDATE cards SET is_starred = ? WHERE id = ?`, from.IsStarred, id); err != nil {
				return nil, fmt.Errorf("move card: %w", err)
			}
		}
		return append(garbage, st.Garbage), nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

// remove deletes a card from DAV and leaves a tombstone.
func (s *Store) remove(collID int64, uri string, cond davsql.Conditions, now time.Time) error {
	return s.write(func(tx *sql.Tx) ([]string, error) {
		photo, err := deleteCard(tx, collID, uri, cond, now.Unix())
		return []string{photo}, err
	})
}

// Delete removes a card and leaves a tombstone for DAV clients.
func (s *Store) Delete(c *Card, now time.Time) error {
	return s.remove(c.AddressBookID, c.URI, davsql.Conditions{}, now)
}

// purge removes every card of a deleted collection, and the photos only
// they used.
func (s *Store) purge(collID int64) error {
	return s.write(func(tx *sql.Tx) ([]string, error) {
		rows, err := tx.Query(`SELECT DISTINCT photo_hash FROM cards WHERE collection_id = ? AND photo_hash != ''`, collID)
		if err != nil {
			return nil, fmt.Errorf("purge cards: %w", err)
		}
		var photos []string
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, fmt.Errorf("purge cards: %w", err)
			}
			photos = append(photos, h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("purge cards: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM cards WHERE collection_id = ?`, collID); err != nil {
			return nil, fmt.Errorf("purge cards: %w", err)
		}
		return photos, davsql.Purge(tx, collID)
	})
}
