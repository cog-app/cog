package contacts_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
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

func TestContactsSettingsPage(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Ensure DB is migrated.
	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	resp := env.Get(t, "/apps/contacts/settings", alice.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	require.Contains(t, body, "Contacts") // default address book
}

func TestContactsUpdateContact(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	contacts.EnsureDefaultAddressBook(db)
	books, _ := contacts.ListAddressBooks(db)

	vcard := contacts.GenerateVCard("update-uid", "Old Name", "old@example.com", "", time.Now())
	fields, err := contacts.ParseVCard(vcard)
	require.NoError(t, err)
	card, err := contacts.UpsertCard(db, books[0].ID, "update-uid", "update-uid.vcf", vcard, fields)
	require.NoError(t, err)

	// Create session and CSRF token for the POST action.
	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", token)

	form := url.Values{
		"fn":    {"New Name"},
		"org":   {"Acme Corp"},
		"title": {"Engineer"},
	}
	resp := env.PostForm(t, fmt.Sprintf("/apps/contacts/contacts/%d", card.ID), form,
		cookie, csrf, cogtest.StopRedirect)
	require.True(t, resp.StatusCode == 303 || resp.StatusCode == 200)

	// Verify the card was updated.
	updated, err := contacts.GetCard(db, card.ID)
	require.NoError(t, err)
	require.Equal(t, "New Name", updated.FN)
	require.Equal(t, "Acme Corp", updated.Org)
}

func TestContactsCreateDeleteAddressBookHandler(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", token)

	// Create an address book via POST handler.
	form := url.Values{
		"uid":   {"test-book"},
		"name":  {"Test Book"},
		"color": {"#ff0000"},
	}
	resp := env.PostForm(t, "/apps/contacts/books", form, cookie, csrf, cogtest.StopRedirect)
	require.True(t, resp.StatusCode == 303 || resp.StatusCode == 200)

	// Verify the book was created.
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)
	books, err := contacts.ListAddressBooks(db)
	require.NoError(t, err)
	var bookID int64
	found := false
	for _, b := range books {
		if b.UID == "test-book" {
			found = true
			bookID = b.ID
		}
	}
	require.True(t, found)

	// Delete the address book via DELETE handler.
	resp = env.Delete(t, fmt.Sprintf("/apps/contacts/books/%d", bookID), cookie, csrf)
	resp.Status(200)

	// Verify it was deleted.
	books, err = contacts.ListAddressBooks(db)
	require.NoError(t, err)
	for _, b := range books {
		require.True(t, b.UID != "test-book")
	}
}

func TestContactsToggleStar(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	contacts.EnsureDefaultAddressBook(db)
	books, _ := contacts.ListAddressBooks(db)

	vcard := contacts.GenerateVCard("toggle-uid", "Toggle Star", "toggle@example.com", "", time.Now())
	fields, err := contacts.ParseVCard(vcard)
	require.NoError(t, err)
	card, err := contacts.UpsertCard(db, books[0].ID, "toggle-uid", "toggle-uid.vcf", vcard, fields)
	require.NoError(t, err)
	require.Equal(t, false, card.IsStarred)

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", token)

	// Toggle star on.
	resp := env.Post(t, fmt.Sprintf("/apps/contacts/contacts/%d/star", card.ID), cookie, csrf)
	resp.Status(200)

	updated, err := contacts.GetCard(db, card.ID)
	require.NoError(t, err)
	require.Equal(t, true, updated.IsStarred)

	// Toggle star off.
	resp = env.Post(t, fmt.Sprintf("/apps/contacts/contacts/%d/star", card.ID), cookie, csrf)
	resp.Status(200)

	updated, err = contacts.GetCard(db, card.ID)
	require.NoError(t, err)
	require.Equal(t, false, updated.IsStarred)
}
