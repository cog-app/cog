package contacts_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
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
		b.Type(t, "#cc-given", "Alice")
		b.Type(t, "#cc-family", "Smith")
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

	t.Run("PopulatedList", func(t *testing.T) {
		eng := populatedEnv(t, "list")
		b := eng.Start(t)
		b.Login(t, "list", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "Maximilian")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("BookFilter", func(t *testing.T) {
		eng := populatedEnv(t, "book")
		b := eng.Start(t)
		b.Login(t, "book", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "Maximilian")
		href := b.Eval(t, `() => Array.from(document.querySelectorAll('.sidebar a[href*="?book="]')).pop().getAttribute('href')`)
		b.Navigate(t, href.(string))
		b.WaitText(t, "Emma Wilson")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DetailManyFields", func(t *testing.T) {
		eng := populatedEnv(t, "detail")
		b := eng.Start(t)
		b.Login(t, "detail", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "Maximilian")
		b.ClickText(t, "Maximilian")
		b.WaitVisible(t, `#contact-modal [hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditFormMultipleValues", func(t *testing.T) {
		eng := populatedEnv(t, "editmulti")
		b := eng.Start(t)
		b.Login(t, "editmulti", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "Maximilian")
		b.ClickText(t, "Maximilian")
		b.WaitVisible(t, `#contact-modal [hx-delete]`)
		b.Click(t, `#contact-modal button[onclick*="em-phones"]`)
		// The image shows the email and phone rows rather than the header.
		b.Eval(t, `() => document.getElementById('em-emails').scrollIntoView()`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateFormExtraRows", func(t *testing.T) {
		eng := populatedEnv(t, "newform")
		b := eng.Start(t)
		b.Login(t, "newform", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "Maximilian")
		b.Click(t, `button[onclick*="create-contact-modal"]`)
		b.WaitVisible(t, "#cc-given")
		b.Click(t, `#create-contact-modal button[onclick*="cc-emails"]`)
		b.Click(t, `#create-contact-modal button[onclick*="cc-phones"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("StarredWithContact", func(t *testing.T) {
		eng := populatedEnv(t, "star")
		b := eng.Start(t)
		b.Login(t, "star", "pass")
		b.Navigate(t, "/apps/contacts/starred")
		b.WaitText(t, "Maximilian")
		// The detail modal opened from the starred page, not the index.
		b.ClickText(t, "Maximilian")
		b.WaitVisible(t, `#contact-modal [hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// populatedEnv is browserEnv with the seeded contacts in two address books
// and one starred contact whose every field is long.
func populatedEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, contacts.New())
	env.CreateUser(t, "admin", "pass")
	u := env.CreateUser(t, username, "pass").Seed(t)
	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	s, err := cp.Store(username)
	require.NoError(t, err)
	books, err := cp.Books(u.ID)
	require.NoError(t, err)
	c := contacts.NewCard(env.Now())
	c.UID = "maximilian"
	c.N = &vcard.Name{Given: []string{"Maximilian Alexander"}, Family: []string{"Featherstonehaugh-Wolfeschlegelsteinhausen"}}
	c.FN = c.N.Formatted()
	c.Org = []string{"International Consolidated Widgets and Sprockets Corporation"}
	c.Title = "Senior Principal Vice President of Strategic Partnerships"
	c.Birthday = "1984-02-29"
	c.Note = "Met at the conference in Lisbon.\nPrefers email over phone; never call before 10am."
	c.Emails = []vcard.Value{
		{Meta: vcard.Meta{Types: []string{"INTERNET"}}, Value: "maximilian.featherstonehaugh-wolfeschlegelsteinhausen@international-widgets.example.com"},
		{Meta: vcard.Meta{Types: []string{"WORK"}}, Value: "max@widgets.example.com"},
		{Meta: vcard.Meta{Types: []string{"HOME"}}, Value: "max@home.example.org"},
	}
	c.Phones = []vcard.Value{
		{Meta: vcard.Meta{Types: []string{"CELL"}}, Value: "+44 20 7946 0958"},
		{Meta: vcard.Meta{Types: []string{"WORK"}}, Value: "+1 (555) 010-9999 ext. 12345"},
		{Meta: vcard.Meta{Types: []string{"FAX"}}, Value: "+1 555 010 0000"},
	}
	c.Addresses = []vcard.Address{{
		Meta:     vcard.Meta{Types: []string{"WORK"}},
		Street:   []string{"1600 Amphitheatre Parkway, Building 42, Floor 7"},
		Locality: []string{"Mountain View"}, Region: []string{"CA"}, Code: []string{"94043"},
		Country: []string{"United States of America"},
	}}
	c.URLs = []vcard.Value{{Value: "https://www.international-consolidated-widgets.example.com/people/maximilian"}}
	card, err := s.Save(books[0].ID, nil, c, env.Now())
	require.NoError(t, err)
	require.NoError(t, s.SetStarred(card.ID, true))

	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
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
