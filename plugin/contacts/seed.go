package contacts

import (
	"crypto/md5"
	"fmt"
)

// Seed creates sample address books and contacts for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}

	// Check if already seeded.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM address_books`).Scan(&count)
	if count > 0 {
		return nil
	}

	db.Exec(`INSERT INTO address_books (uid, name, color) VALUES ('contacts', 'Contacts', '#0082c9')`)
	db.Exec(`INSERT INTO address_books (uid, name, color) VALUES ('work', 'Work', '#e64a19')`)

	type seedCard struct {
		bookUID string
		uid     string
		fn      string
		email   string
		tel     string
	}

	cards := []seedCard{
		{"contacts", "alice-smith", "Alice Smith", "alice@example.com", "+1-555-0101"},
		{"contacts", "bob-jones", "Bob Jones", "bob.jones@example.com", "+1-555-0102"},
		{"contacts", "carol-white", "Carol White", "carol@example.com", ""},
		{"contacts", "david-brown", "David Brown", "", "+1-555-0104"},
		{"work", "emma-wilson", "Emma Wilson", "emma.wilson@company.com", "+1-555-0201"},
		{"work", "frank-taylor", "Frank Taylor", "frank.t@company.com", "+1-555-0202"},
		{"work", "grace-lee", "Grace Lee", "grace.lee@company.com", "+1-555-0203"},
	}

	for _, c := range cards {
		vc := vcard(c.uid, c.fn, c.email, c.tel)
		etag := fmt.Sprintf("%x", md5.Sum([]byte(vc)))
		uri := c.uid + ".vcf"
		var bookID int64
		db.QueryRow(`SELECT id FROM address_books WHERE uid = ?`, c.bookUID).Scan(&bookID)
		_, err := db.Exec(`INSERT INTO cards (address_book_id, uid, uri, etag, fn, email, tel, vcard_data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			bookID, c.uid, uri, etag, c.fn, c.email, c.tel, vc)
		if err != nil {
			p.log.Error("seed contact", "uid", c.uid, "err", err)
		} else {
			p.log.Info("seeded contact", "name", c.fn)
		}
	}

	db.Exec(`UPDATE address_books SET ctag = (SELECT COUNT(*) FROM cards WHERE address_book_id = address_books.id)`)
	return nil
}

func vcard(uid, fn, email, tel string) string {
	s := "BEGIN:VCARD\r\nVERSION:3.0\r\n"
	s += "UID:" + uid + "\r\n"
	s += "FN:" + fn + "\r\n"
	if email != "" {
		s += "EMAIL;TYPE=INTERNET:" + email + "\r\n"
	}
	if tel != "" {
		s += "TEL;TYPE=CELL:" + tel + "\r\n"
	}
	s += "REV:20260305T120000Z\r\n"
	s += "END:VCARD\r\n"
	return s
}
