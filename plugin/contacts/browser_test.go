package contacts_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

func TestBrowser(t *testing.T) {

	t.Run("EmptyState", func(t *testing.T) {
		eng := browserEnv(t, "empty")
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Create", func(t *testing.T) {
		eng := browserEnv(t, "create")
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		b.Eval(t, `document.getElementById('create-contact-modal').style.display='flex'`)
		b.Type(t, "#cc-fn", "Alice Smith")
		b.Click(t, `#create-contact-modal button[type="submit"]`)
		b.WaitText(t, "Alice Smith")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Delete", func(t *testing.T) {
		eng := browserEnv(t, "delete")
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		b.Eval(t, `document.getElementById('create-contact-modal').style.display='flex'`)
		b.Type(t, "#cc-fn", "Bob Jones")
		b.Click(t, `#create-contact-modal button[type="submit"]`)
		b.WaitText(t, "Bob Jones")
		// Click the contact row to open detail modal via showContactDetail().
		b.ClickText(t, "Bob Jones")
		b.WaitVisible(t, `#contact-modal [hx-delete]`)
		b.AcceptConfirms(t)
		b.Click(t, `#contact-modal [hx-delete]`)
		b.WaitNotVisible(t, `#contact-modal [hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		eng := browserEnv(t, "settings")
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/contacts/settings")
		b.WaitText(t, "Contacts")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditContact", func(t *testing.T) {
		eng := browserEnv(t, "edit")
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		// Create a contact first.
		b.Eval(t, `document.getElementById('create-contact-modal').style.display='flex'`)
		b.Type(t, "#cc-fn", "Edit Target")
		b.Click(t, `#create-contact-modal button[type="submit"]`)
		b.WaitText(t, "Edit Target")
		// Open detail modal.
		b.ClickText(t, "Edit Target")
		b.WaitVisible(t, `#contact-modal`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("StarredPage", func(t *testing.T) {
		eng := browserEnv(t, "starred")
		b := eng.Start(t)
		b.Login(t, "starred", "pass")
		b.Navigate(t, "/apps/contacts/starred")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateAddressBook", func(t *testing.T) {
		eng := browserEnv(t, "newbook")
		b := eng.Start(t)
		b.Login(t, "newbook", "pass")
		b.Navigate(t, "/apps/contacts/settings")
		b.WaitVisible(t, `input[name="name"]`)
		b.Type(t, `input[name="name"]`, "Work Contacts")
		b.Click(t, `form[action="/apps/contacts/books"] button[type="submit"]`)
		b.WaitText(t, "Work Contacts")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// browserEnv starts a server and browser of its own for one subtest, with one
// user. Collection IDs are global, so a shared server would give a subtest
// different IDs, and a different golden, depending on which others ran.
func browserEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, contacts.New())
	// The first user is made an admin; the one under test is not.
	env.CreateUser(t, "admin", "pass")
	env.CreateUser(t, username, "pass")
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
}
