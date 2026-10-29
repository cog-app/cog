package contacts

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

// Seed creates sample address books and contacts for a user.
func (p *Plugin) Seed(username string) error {
	s, err := p.Store(username)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}
	userID, err := p.userID(username)
	if err != nil {
		return fmt.Errorf("seed contacts: %w", err)
	}

	// Check if already seeded.
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM cards`).Scan(&count); err != nil {
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
		bookUID       string
		uid           string
		given, family string
		email         string
		tel           string
	}

	cards := []seedCard{
		{"contacts", "alice-smith", "Alice", "Smith", "alice@example.com", "+1-555-0101"},
		{"contacts", "bob-jones", "Bob", "Jones", "bob.jones@example.com", "+1-555-0102"},
		{"contacts", "carol-white", "Carol", "White", "carol@example.com", ""},
		{"contacts", "david-brown", "David", "Brown", "", "+1-555-0104"},
		{"work", "emma-wilson", "Emma", "Wilson", "emma.wilson@company.com", "+1-555-0201"},
		{"work", "frank-taylor", "Frank", "Taylor", "frank.t@company.com", "+1-555-0202"},
		{"work", "grace-lee", "Grace", "Lee", "grace.lee@company.com", "+1-555-0203"},
	}

	now := p.ctx.Now()
	for _, sc := range cards {
		c := NewCard(now)
		c.UID = sc.uid
		c.N = &vcard.Name{Given: []string{sc.given}, Family: []string{sc.family}}
		c.FN = c.N.Formatted()
		if sc.email != "" {
			c.Emails = []vcard.Value{{Meta: vcard.Meta{Types: []string{"INTERNET"}}, Value: sc.email}}
		}
		if sc.tel != "" {
			c.Phones = []vcard.Value{{Meta: vcard.Meta{Types: []string{"CELL"}}, Value: sc.tel}}
		}
		if _, err := s.Save(ids[sc.bookUID], nil, c, now); err != nil {
			return fmt.Errorf("seed contact %s: %w", sc.uid, err)
		}
	}
	return nil
}
