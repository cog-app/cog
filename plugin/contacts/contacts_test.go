package contacts_test

import (
	"time"
	"fmt"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

func TestContactsPage(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/contacts/", alice.Auth).Status(200)
}

func TestContactsStarredPage(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/contacts/starred", alice.Auth).Status(200)
}

func TestContactsCreateAndView(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	contacts.EnsureDefaultAddressBook(db)
	books, err := contacts.ListAddressBooks(db)
	require.NoError(t, err)
	require.True(t, len(books) > 0)

	vcard := contacts.GenerateVCard("test-uid-1", "Jane Doe", "jane@example.com", "+1234567890", time.Now())
	fields, err := contacts.ParseVCard(vcard)
	require.NoError(t, err)
	card, err := contacts.UpsertCard(db, books[0].ID, "test-uid-1", "test-uid-1.vcf", vcard, fields)
	require.NoError(t, err)
	require.Equal(t, "Jane Doe", card.FN)

	// View contact detail via API.
	resp := env.Get(t, fmt.Sprintf("/apps/contacts/contacts/%d", card.ID), alice.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	require.Contains(t, body, "Jane Doe")
}

func TestContactsCreateAddressBook(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	book, err := contacts.CreateAddressBook(db, "work-contacts", "Work Contacts", "Colleagues", "#0000ff")
	require.NoError(t, err)
	require.Equal(t, "Work Contacts", book.Name)

	books, err := contacts.ListAddressBooks(db)
	require.NoError(t, err)

	found := false
	for _, b := range books {
		if b.Name == "Work Contacts" {
			found = true
		}
	}
	require.True(t, found)
}

func TestContactsStar(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	contacts.EnsureDefaultAddressBook(db)
	books, _ := contacts.ListAddressBooks(db)

	vcard := contacts.GenerateVCard("star-uid", "Bob Star", "bob@example.com", "", time.Now())
	fields, err := contacts.ParseVCard(vcard)
	require.NoError(t, err)
	card, err := contacts.UpsertCard(db, books[0].ID, "star-uid", "star-uid.vcf", vcard, fields)
	require.NoError(t, err)

	err = contacts.SetStarred(db, card.ID, true)
	require.NoError(t, err)

	starred, err := contacts.StarredContacts(db, 10)
	require.NoError(t, err)
	require.Equal(t, 1, len(starred))
	require.Equal(t, "Bob Star", starred[0].FN)
}
