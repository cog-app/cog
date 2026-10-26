package contacts

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
)

// Seed creates sample address books and contacts for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	userID, err := p.userID(username)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}

	// Check if already seeded.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cards`).Scan(&count); err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	if count > 0 {
		return nil
	}

	books, err := p.Books(userID)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	ids := map[string]int64{"contacts": books[0].ID}
	work, err := p.BookByName(userID, "work")
	if errors.Is(err, dav.ErrNotFound) {
		work, err = p.CreateBook(userID, "work", "Work", "", "#e64a19")
	}
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	ids["work"] = work.ID

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
		if _, err := SaveCard(db, ids[c.bookUID], nil, vcard(c.uid, c.fn, c.email, c.tel), p.ctx.Now()); err != nil {
			return fmt.Errorf("seed contact %s: %w", c.uid, err)
		}
	}
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
