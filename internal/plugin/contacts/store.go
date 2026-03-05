package contacts

import (
	"crypto/md5"
	"database/sql"
	"fmt"
)

// AddressBook is a user's named address book collection.
type AddressBook struct {
	ID          int64
	UID         string
	Name        string
	Description string
	Color       string
	SortOrder   int
	SyncToken   int64
	CTag        int64
	CreatedAt   int64
	UpdatedAt   int64
}

// Card is a single contact card with indexed fields and raw vCard blob.
type Card struct {
	ID            int64
	AddressBookID int64
	UID           string
	URI           string
	ETag          string
	FN            string
	Email         string
	Tel           string
	VCardData     string
	Rev           string
	CreatedAt     int64
	UpdatedAt     int64
}

// CardWithBook extends Card with address book metadata for display.
type CardWithBook struct {
	Card
	BookName  string
	BookColor string
}

// --- Address Book CRUD ---

func ListAddressBooks(db *sql.DB) ([]AddressBook, error) {
	rows, err := db.Query(`SELECT id, uid, name, description, color, sort_order, sync_token, ctag, created_at, updated_at
		FROM address_books ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var books []AddressBook
	for rows.Next() {
		var b AddressBook
		if err := rows.Scan(&b.ID, &b.UID, &b.Name, &b.Description, &b.Color, &b.SortOrder, &b.SyncToken, &b.CTag, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		books = append(books, b)
	}
	return books, rows.Err()
}

func GetAddressBook(db *sql.DB, id int64) (*AddressBook, error) {
	var b AddressBook
	err := db.QueryRow(`SELECT id, uid, name, description, color, sort_order, sync_token, ctag, created_at, updated_at
		FROM address_books WHERE id = ?`, id).
		Scan(&b.ID, &b.UID, &b.Name, &b.Description, &b.Color, &b.SortOrder, &b.SyncToken, &b.CTag, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func GetAddressBookByUID(db *sql.DB, uid string) (*AddressBook, error) {
	var b AddressBook
	err := db.QueryRow(`SELECT id, uid, name, description, color, sort_order, sync_token, ctag, created_at, updated_at
		FROM address_books WHERE uid = ?`, uid).
		Scan(&b.ID, &b.UID, &b.Name, &b.Description, &b.Color, &b.SortOrder, &b.SyncToken, &b.CTag, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func CreateAddressBook(db *sql.DB, uid, name, description, color string) (*AddressBook, error) {
	if color == "" {
		color = "#0082c9"
	}
	res, err := db.Exec(`INSERT INTO address_books (uid, name, description, color) VALUES (?, ?, ?, ?)`, uid, name, description, color)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetAddressBook(db, id)
}

func UpdateAddressBook(db *sql.DB, id int64, name, description, color string) error {
	_, err := db.Exec(`UPDATE address_books SET name = ?, description = ?, color = ?, updated_at = unixepoch() WHERE id = ?`, name, description, color, id)
	return err
}

func DeleteAddressBook(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM address_books WHERE id = ?`, id)
	return err
}

func BumpCTag(db *sql.DB, bookID int64) error {
	_, err := db.Exec(`UPDATE address_books SET ctag = ctag + 1, sync_token = sync_token + 1, updated_at = unixepoch() WHERE id = ?`, bookID)
	return err
}

// EnsureDefaultAddressBook creates a "contacts" address book if none exist.
func EnsureDefaultAddressBook(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM address_books`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := CreateAddressBook(db, "contacts", "Contacts", "", "#0082c9")
		return err
	}
	return nil
}

// --- Card CRUD ---

func ListCards(db *sql.DB, bookID int64) ([]Card, error) {
	rows, err := db.Query(`SELECT id, address_book_id, uid, uri, etag, fn, email, tel, vcard_data, rev, created_at, updated_at
		FROM cards WHERE address_book_id = ? ORDER BY fn`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCards(rows)
}

func GetCardByUID(db *sql.DB, bookID int64, uid string) (*Card, error) {
	var c Card
	err := db.QueryRow(`SELECT id, address_book_id, uid, uri, etag, fn, email, tel, vcard_data, rev, created_at, updated_at
		FROM cards WHERE address_book_id = ? AND uid = ?`, bookID, uid).
		Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.ETag, &c.FN, &c.Email, &c.Tel, &c.VCardData, &c.Rev, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func GetCardByURI(db *sql.DB, bookID int64, uri string) (*Card, error) {
	var c Card
	err := db.QueryRow(`SELECT id, address_book_id, uid, uri, etag, fn, email, tel, vcard_data, rev, created_at, updated_at
		FROM cards WHERE address_book_id = ? AND uri = ?`, bookID, uri).
		Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.ETag, &c.FN, &c.Email, &c.Tel, &c.VCardData, &c.Rev, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func GetCard(db *sql.DB, id int64) (*Card, error) {
	var c Card
	err := db.QueryRow(`SELECT id, address_book_id, uid, uri, etag, fn, email, tel, vcard_data, rev, created_at, updated_at
		FROM cards WHERE id = ?`, id).
		Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.ETag, &c.FN, &c.Email, &c.Tel, &c.VCardData, &c.Rev, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertCard inserts or updates a card, bumps the address book's ctag/sync_token.
func UpsertCard(db *sql.DB, bookID int64, uid, uri, vcardData string, fields VCardFields) (*Card, error) {
	etag := fmt.Sprintf("%x", md5.Sum([]byte(vcardData)))

	_, err := db.Exec(`INSERT INTO cards (address_book_id, uid, uri, etag, fn, email, tel, vcard_data, rev)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(address_book_id, uid) DO UPDATE SET
			uri = excluded.uri,
			etag = excluded.etag,
			fn = excluded.fn,
			email = excluded.email,
			tel = excluded.tel,
			vcard_data = excluded.vcard_data,
			rev = excluded.rev,
			updated_at = unixepoch()`,
		bookID, uid, uri, etag, fields.FN, fields.Email, fields.Tel, vcardData, fields.Rev)
	if err != nil {
		return nil, err
	}

	if err := BumpCTag(db, bookID); err != nil {
		return nil, err
	}

	return GetCardByUID(db, bookID, uid)
}

func DeleteCard(db *sql.DB, bookID int64, uid string) error {
	// Record deletion for sync-token tracking.
	var syncToken int64
	if err := db.QueryRow(`SELECT sync_token FROM address_books WHERE id = ?`, bookID).Scan(&syncToken); err != nil {
		return fmt.Errorf("get sync token: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO deleted_cards (address_book_id, uid, deleted_at_token) VALUES (?, ?, ?)`,
		bookID, uid, syncToken+1); err != nil {
		return fmt.Errorf("record deletion: %w", err)
	}

	_, err := db.Exec(`DELETE FROM cards WHERE address_book_id = ? AND uid = ?`, bookID, uid)
	if err != nil {
		return err
	}
	return BumpCTag(db, bookID)
}

func DeleteCardByID(db *sql.DB, id int64) error {
	var bookID int64
	var uid string
	if err := db.QueryRow(`SELECT address_book_id, uid FROM cards WHERE id = ?`, id).Scan(&bookID, &uid); err != nil {
		return err
	}

	var syncToken int64
	if err := db.QueryRow(`SELECT sync_token FROM address_books WHERE id = ?`, bookID).Scan(&syncToken); err != nil {
		return fmt.Errorf("get sync token: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO deleted_cards (address_book_id, uid, deleted_at_token) VALUES (?, ?, ?)`,
		bookID, uid, syncToken+1); err != nil {
		return fmt.Errorf("record deletion: %w", err)
	}

	if _, err := db.Exec(`DELETE FROM cards WHERE id = ?`, id); err != nil {
		return err
	}
	return BumpCTag(db, bookID)
}

// AllCards returns all cards joined with address book info, ordered by name.
func AllCards(db *sql.DB, limit int) ([]CardWithBook, error) {
	rows, err := db.Query(`SELECT c.id, c.address_book_id, c.uid, c.uri, c.etag, c.fn, c.email, c.tel,
		c.vcard_data, c.rev, c.created_at, c.updated_at,
		b.name, b.color
		FROM cards c JOIN address_books b ON c.address_book_id = b.id
		ORDER BY c.fn LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []CardWithBook
	for rows.Next() {
		var cw CardWithBook
		if err := rows.Scan(&cw.ID, &cw.AddressBookID, &cw.UID, &cw.URI, &cw.ETag, &cw.FN, &cw.Email, &cw.Tel,
			&cw.VCardData, &cw.Rev, &cw.CreatedAt, &cw.UpdatedAt,
			&cw.BookName, &cw.BookColor); err != nil {
			return nil, err
		}
		cards = append(cards, cw)
	}
	return cards, rows.Err()
}

// CardsByAddressBook returns cards for a specific address book, joined with book info.
func CardsByAddressBook(db *sql.DB, bookID int64, limit int) ([]CardWithBook, error) {
	rows, err := db.Query(`SELECT c.id, c.address_book_id, c.uid, c.uri, c.etag, c.fn, c.email, c.tel,
		c.vcard_data, c.rev, c.created_at, c.updated_at,
		b.name, b.color
		FROM cards c JOIN address_books b ON c.address_book_id = b.id
		WHERE c.address_book_id = ?
		ORDER BY c.fn LIMIT ?`, bookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []CardWithBook
	for rows.Next() {
		var cw CardWithBook
		if err := rows.Scan(&cw.ID, &cw.AddressBookID, &cw.UID, &cw.URI, &cw.ETag, &cw.FN, &cw.Email, &cw.Tel,
			&cw.VCardData, &cw.Rev, &cw.CreatedAt, &cw.UpdatedAt,
			&cw.BookName, &cw.BookColor); err != nil {
			return nil, err
		}
		cards = append(cards, cw)
	}
	return cards, rows.Err()
}

// DeletedCardsSince returns UIDs of cards deleted after the given sync token.
func DeletedCardsSince(db *sql.DB, bookID, sinceToken int64) ([]string, error) {
	rows, err := db.Query(`SELECT uid FROM deleted_cards WHERE address_book_id = ? AND deleted_at_token > ?`,
		bookID, sinceToken)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

func scanCards(rows *sql.Rows) ([]Card, error) {
	var cards []Card
	for rows.Next() {
		var c Card
		if err := rows.Scan(&c.ID, &c.AddressBookID, &c.UID, &c.URI, &c.ETag, &c.FN, &c.Email, &c.Tel,
			&c.VCardData, &c.Rev, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}
