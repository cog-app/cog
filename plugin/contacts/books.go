package contacts

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
)

// userID returns the ID of a user by username. Collections are keyed by user
// ID; plugin databases by username.
func (p *Plugin) userID(username string) (string, error) {
	users := p.ctx.UserLookup()
	if users == nil {
		return "", errors.New("contacts: no user lookup service")
	}
	u, err := users.GetByUsername(username)
	if err != nil {
		return "", fmt.Errorf("look up user %s: %w", username, err)
	}
	return u.ID, nil
}

func (p *Plugin) username(userID string) (string, error) {
	users := p.ctx.UserLookup()
	if users == nil {
		return "", errors.New("contacts: no user lookup service")
	}
	u, err := users.GetByID(userID)
	if err != nil {
		return "", fmt.Errorf("look up user %s: %w", userID, err)
	}
	return u.Username, nil
}

// Books returns a user's address books, creating the default one when there
// are none.
func (p *Plugin) Books(userID string) ([]AddressBook, error) {
	cols, err := p.dav.Collections.EnsureDefault(userID, dav.HomeAddressBooks)
	if err != nil {
		return nil, err
	}
	books := make([]AddressBook, len(cols))
	for i := range cols {
		books[i] = bookFrom(&cols[i])
	}
	return books, nil
}

func (p *Plugin) bookMap(userID string) (map[int64]AddressBook, error) {
	books, err := p.Books(userID)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]AddressBook, len(books))
	for _, b := range books {
		m[b.ID] = b
	}
	return m, nil
}

// Book returns one of a user's own address books, or dav.ErrNotFound.
func (p *Plugin) Book(userID string, id int64) (*AddressBook, error) {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return nil, err
	}
	if c.OwnerID != userID || c.Home != dav.HomeAddressBooks {
		return nil, dav.ErrNotFound
	}
	b := bookFrom(c)
	return &b, nil
}

// BookByName returns a user's address book by its path name.
func (p *Plugin) BookByName(userID, name string) (*AddressBook, error) {
	c, err := p.dav.Collections.Get(userID, dav.HomeAddressBooks, name)
	if err != nil {
		return nil, err
	}
	b := bookFrom(c)
	return &b, nil
}

// CreateBook creates an address book. An empty name is made from the
// display name, unique among the user's address books.
func (p *Plugin) CreateBook(userID, name, displayName, description, color string) (*AddressBook, error) {
	create := p.dav.Collections.Create
	if name == "" {
		name = dav.Slug(displayName, "contacts")
		create = p.dav.Collections.CreateNamed
	}
	if color == "" {
		color = "#0082c9"
	}
	c := &dav.Collection{
		OwnerID: userID, Home: dav.HomeAddressBooks, Name: name, Components: []dav.Component{dav.VCARD},
		DisplayName: displayName, Description: description, Color: color,
	}
	if err := create(c); err != nil {
		return nil, err
	}
	b := bookFrom(c)
	return &b, nil
}

// UpdateBook renames or recolors one of a user's address books.
func (p *Plugin) UpdateBook(userID string, id int64, displayName, color string) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID || c.Home != dav.HomeAddressBooks {
		return dav.ErrNotFound
	}
	c.DisplayName, c.Color = displayName, color
	return p.dav.Collections.Update(c)
}

// DeleteBook deletes one of a user's address books with its cards.
func (p *Plugin) DeleteBook(username, userID string, id int64) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID || c.Home != dav.HomeAddressBooks {
		return dav.ErrNotFound
	}
	return p.dav.DeleteCollection(username, c)
}

// sharedAddressBook describes an address book shared to the current user.
type sharedAddressBook struct {
	OwnerUsername string
	Book          AddressBook
	Perms         uint32
}

// sharedBooks returns the address books shared with a user.
func (p *Plugin) sharedBooks(userID string) ([]sharedAddressBook, error) {
	shared, err := p.dav.Collections.Shared(p.ctx.ShareStore(), p.username, userID, dav.VCARD)
	if err != nil {
		return nil, err
	}
	out := make([]sharedAddressBook, len(shared))
	for i, sc := range shared {
		out[i] = sharedAddressBook{OwnerUsername: sc.OwnerUsername, Book: bookFrom(&sc.Collection), Perms: sc.Permissions}
	}
	return out, nil
}
