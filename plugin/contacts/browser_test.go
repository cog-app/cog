package contacts_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "delete", "pass")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Create", func(t *testing.T) {
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
}
