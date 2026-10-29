package contacts_test

import (
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

func carddavReq(t testing.TB, env *cogtest.Env, method, path, body string, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	return env.Do(t, req, opts...)
}

func TestCardDAV_AddressBookHome(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Ensure DB is migrated and default address book exists.
	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	// PROPFIND on address book home with Depth: 1 should list address books.
	resp := carddavReq(t, env, "PROPFIND",
		"/remote.php/dav/addressbooks/"+alice.Username+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "D:multistatus")
	require.Contains(t, body, "addressbook")
	require.Contains(t, body, "Contacts") // default address book name

	// Depth: 0 should return only the home collection.
	resp = carddavReq(t, env, "PROPFIND",
		"/remote.php/dav/addressbooks/"+alice.Username+"/", "",
		alice.Auth, cogtest.Header("Depth", "0"))
	resp.Status(207)
	body = resp.ReadBody()
	require.Contains(t, body, "D:multistatus")
	require.Contains(t, body, alice.Username)
}

func TestCardDAV_PutGetDeleteCard(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	bookUID := "contacts"
	cardUID := "test-card-1"
	cardURI := cardUID + ".vcf"
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + cardUID + "\r\nFN:Alice Wonderland\r\nEMAIL;TYPE=INTERNET:alice@example.com\r\nTEL;TYPE=CELL:+15551234567\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/" + bookUID + "/"

	// PUT a new contact.
	resp := carddavReq(t, env, "PUT", basePath+cardURI, vcf,
		alice.Auth, cogtest.Header("Content-Type", "text/vcard; charset=utf-8"))
	resp.Status(201)
	etag := resp.Header.Get("ETag")
	require.True(t, etag != "")

	// GET the contact back.
	resp = carddavReq(t, env, "GET", basePath+cardURI, "",
		alice.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	require.Contains(t, body, "Alice Wonderland")
	require.Contains(t, body, "alice@example.com")
	require.Contains(t, body, "+15551234567")
	require.Equal(t, "text/vcard; charset=utf-8", resp.Header.Get("Content-Type"))

	// PROPFIND on the card.
	resp = carddavReq(t, env, "PROPFIND", basePath+cardURI, "",
		alice.Auth, cogtest.Header("Depth", "0"))
	resp.Status(207)
	body = resp.ReadBody()
	require.Contains(t, body, "D:multistatus")
	require.Contains(t, body, cardURI)

	// DELETE the contact.
	resp = carddavReq(t, env, "DELETE", basePath+cardURI, "",
		alice.Auth)
	resp.Status(204)

	// GET after delete should return 404.
	resp = carddavReq(t, env, "GET", basePath+cardURI, "",
		alice.Auth)
	resp.Status(404)
}

func TestCardDAV_PutIfNoneMatch(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"
	cardUID := "ifnone-card"
	cardURI := cardUID + ".vcf"
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + cardUID + "\r\nFN:Test Person\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"

	// First PUT should succeed.
	resp := carddavReq(t, env, "PUT", basePath+cardURI, vcf,
		alice.Auth, cogtest.Header("If-None-Match", "*"))
	resp.Status(201)

	// Second PUT with If-None-Match: * should fail (card already exists).
	resp = carddavReq(t, env, "PUT", basePath+cardURI, vcf,
		alice.Auth, cogtest.Header("If-None-Match", "*"))
	resp.Status(412)
}

func TestCardDAV_ReportMultiget(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	// Create two contacts.
	for _, name := range []string{"card-a", "card-b"} {
		vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + name + "\r\nFN:" + name + "\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"
		carddavReq(t, env, "PUT", basePath+name+".vcf", vcf,
			alice.Auth).Status(201)
	}

	// REPORT addressbook-multiget requesting both cards.
	reportXML := `<?xml version="1.0" encoding="UTF-8"?>
<CR:addressbook-multiget xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <D:getetag/>
    <CR:address-data/>
  </D:prop>
  <D:href>` + basePath + `card-a.vcf</D:href>
  <D:href>` + basePath + `card-b.vcf</D:href>
</CR:addressbook-multiget>`

	resp := carddavReq(t, env, "REPORT", basePath, reportXML,
		alice.Auth, cogtest.Header("Content-Type", "application/xml"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "card-a")
	require.Contains(t, body, "card-b")
	require.Contains(t, body, "D:multistatus")

	// Request a non-existent card in multiget — should get 404 status for that href.
	reportXML2 := `<?xml version="1.0" encoding="UTF-8"?>
<CR:addressbook-multiget xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <D:getetag/>
    <CR:address-data/>
  </D:prop>
  <D:href>` + basePath + `nonexistent.vcf</D:href>
</CR:addressbook-multiget>`

	resp = carddavReq(t, env, "REPORT", basePath, reportXML2,
		alice.Auth, cogtest.Header("Content-Type", "application/xml"))
	resp.Status(207)
	body = resp.ReadBody()
	require.Contains(t, body, "404")
}

func TestCardDAV_ReportQuery(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:query-card\r\nFN:Query Person\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", basePath+"query-card.vcf", vcf, alice.Auth).Status(201)

	reportXML := `<?xml version="1.0" encoding="UTF-8"?>
<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop>
    <D:getetag/>
    <CR:address-data/>
  </D:prop>
</CR:addressbook-query>`

	resp := carddavReq(t, env, "REPORT", basePath, reportXML,
		alice.Auth, cogtest.Header("Content-Type", "application/xml"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "Query Person")
}

// syncReport sends a sync-collection REPORT with token and returns the body
// and the new token.
func syncReport(t *testing.T, env *cogtest.Env, u *cogtest.User, path, token string) (string, string) {
	t.Helper()
	body := carddavReq(t, env, "REPORT", path, `<?xml version="1.0" encoding="UTF-8"?>
<D:sync-collection xmlns:D="DAV:">
  <D:sync-token>`+token+`</D:sync-token>
  <D:prop><D:getetag/></D:prop>
</D:sync-collection>`, u.Auth, cogtest.Header("Content-Type", "application/xml")).Status(207).ReadBody()
	_, rest, ok := strings.Cut(body, "<D:sync-token>")
	require.True(t, ok, body)
	next, _, _ := strings.Cut(rest, "</D:sync-token>")
	return body, next
}

func TestCardDAV_SyncCollection(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	first := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:first-card\r\nFN:First\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", basePath+"first-card.vcf", first, alice.Auth).Status(201)

	// Initial sync (empty token) returns every card.
	body, token := syncReport(t, env, alice, basePath, "")
	require.Contains(t, body, "first-card")

	// Add a contact and sync again with the token: only the change comes back.
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:sync-card\r\nFN:Sync Person\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", basePath+"sync-card.vcf", vcf, alice.Auth).Status(201)
	body, next := syncReport(t, env, alice, basePath, token)
	require.Contains(t, body, "sync-card")
	require.NotContains(t, body, "first-card")

	// Nothing changed since: an empty delta.
	body, _ = syncReport(t, env, alice, basePath, next)
	require.NotContains(t, body, "<D:response>")
}

func TestCardDAV_SyncCollectionDeletedCards(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:del-sync-card\r\nFN:Delete Me\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", basePath+"del-sync-card.vcf", vcf, alice.Auth).Status(201)
	_, token := syncReport(t, env, alice, basePath, "")

	carddavReq(t, env, "DELETE", basePath+"del-sync-card.vcf", "", alice.Auth).Status(204)

	// The deletion shows as a 404 response.
	body, _ := syncReport(t, env, alice, basePath, token)
	require.Contains(t, body, "del-sync-card")
	require.Contains(t, body, "404")
}

func TestCardDAV_SyncCollectionOldTokenRefused(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"
	carddavReq(t, env, "PROPFIND", basePath, "", alice.Auth).Status(207)

	// A token from before the composite format makes the client resync.
	carddavReq(t, env, "REPORT", basePath, `<?xml version="1.0" encoding="UTF-8"?>
<D:sync-collection xmlns:D="DAV:"><D:sync-token>http://cog/ns/sync/1</D:sync-token><D:prop><D:getetag/></D:prop></D:sync-collection>`,
		alice.Auth).Status(403).Contains("valid-sync-token")
}

func TestCardDAV_MKCOL(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	// Create a new address book via MKCOL.
	mkcolXML := `<?xml version="1.0" encoding="UTF-8"?>
<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:set>
    <D:prop>
      <D:displayname>Work Contacts</D:displayname>
    </D:prop>
  </D:set>
</D:mkcol>`

	resp := carddavReq(t, env, "MKCOL",
		"/remote.php/dav/addressbooks/"+alice.Username+"/work-contacts/", mkcolXML,
		alice.Auth, cogtest.Header("Content-Type", "application/xml"))
	resp.Status(201)

	// PROPFIND home should now show the new book.
	resp = carddavReq(t, env, "PROPFIND",
		"/remote.php/dav/addressbooks/"+alice.Username+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "work-contacts")
}

func TestCardDAV_PROPPATCH(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	// PROPPATCH to rename the default address book.
	proppatchXML := `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:ICAL="http://apple.com/ns/ical/">
  <D:set>
    <D:prop>
      <D:displayname>My Contacts</D:displayname>
      <ICAL:addressbook-color>#ff0000</ICAL:addressbook-color>
    </D:prop>
  </D:set>
</D:propertyupdate>`

	resp := carddavReq(t, env, "PROPPATCH", basePath, proppatchXML,
		alice.Auth, cogtest.Header("Content-Type", "application/xml"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "D:displayname")
	require.Contains(t, body, "200 OK")
}

func TestCardDAV_DeleteAddressBook(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.Books(alice.ID)
	require.NoError(t, err)

	// Create a second address book to delete.
	_, err = cp.CreateBook(alice.ID, "deleteme", "Delete Me", "", "#000")
	require.NoError(t, err)

	resp := carddavReq(t, env, "DELETE",
		"/remote.php/dav/addressbooks/"+alice.Username+"/deleteme/", "",
		alice.Auth)
	resp.Status(204)

	// PROPFIND should no longer list it.
	resp = carddavReq(t, env, "PROPFIND",
		"/remote.php/dav/addressbooks/"+alice.Username+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.NotContains(t, body, "deleteme")
}

func TestCardDAV_Options(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	// OPTIONS on address book home.
	resp := carddavReq(t, env, "OPTIONS",
		"/remote.php/dav/addressbooks/"+alice.Username+"/", "",
		alice.Auth)
	resp.Status(200)
	require.Contains(t, resp.Header.Get("DAV"), "addressbook")

	// OPTIONS on a specific address book.
	resp = carddavReq(t, env, "OPTIONS",
		"/remote.php/dav/addressbooks/"+alice.Username+"/contacts/", "",
		alice.Auth)
	resp.Status(200)
}

func TestCardDAV_CollectionPROPFINDWithCards(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	basePath := "/remote.php/dav/addressbooks/" + alice.Username + "/contacts/"

	// Add a contact.
	vcf := "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:pfcard\r\nFN:Propfind Card\r\nREV:20260314T060000Z\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", basePath+"pfcard.vcf", vcf, alice.Auth).Status(201)

	// PROPFIND Depth:1 on the book should list the book and the card.
	resp := carddavReq(t, env, "PROPFIND", basePath, "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "pfcard.vcf")
	require.Contains(t, body, "D:getetag")
}

func TestCardDAV_Unauthorized(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	env.CreateUser(t, "alice", "pass")

	// Request without auth should get 401.
	resp := carddavReq(t, env, "PROPFIND",
		"/remote.php/dav/addressbooks/alice/", "")
	resp.Status(401)
}

// TestCardDAV_ReportQueryFilter sends the query a client sends to search
// for an email address: only matching cards come back, and a limit is
// reported with a 507 on the collection (RFC 6352 §8.6.1).
func TestCardDAV_ReportQueryFilter(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	base := "/remote.php/dav/addressbooks/alice/contacts/"
	for _, c := range []struct{ uid, fn, email string }{
		{"a", "Ann Example", "ann@example.org"},
		{"b", "Bob Example", "bob@example.org"},
		{"c", "Cy Other", "cy@other.net"},
	} {
		carddavReq(t, env, "PUT", base+c.uid+".vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+c.uid+"\r\nFN:"+c.fn+
			"\r\nEMAIL:"+c.email+"\r\nPHOTO;ENCODING=b;TYPE=JPEG:AQID\r\nEND:VCARD\r\n", alice.Auth).Status(201)
	}
	query := func(filter, limit string) string {
		return carddavReq(t, env, "REPORT", base, `<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/><CR:address-data/></D:prop>`+filter+limit+`</CR:addressbook-query>`,
			alice.Auth, cogtest.Header("Depth", "1")).Status(207).ReadBody()
	}
	byEmail := `<CR:filter><CR:prop-filter name="EMAIL"><CR:text-match collation="i;unicode-casemap" match-type="ends-with">EXAMPLE.ORG</CR:text-match></CR:prop-filter></CR:filter>`
	body := query(byEmail, "")
	require.Contains(t, body, "a.vcf")
	require.Contains(t, body, "b.vcf")
	require.NotContains(t, body, "c.vcf")
	require.Contains(t, body, "PHOTO;ENCODING=b;TYPE=JPEG:AQID", "matches are served with their photos")

	body = query(byEmail, `<CR:limit><CR:nresults>1</CR:nresults></CR:limit>`)
	require.Contains(t, body, "a.vcf")
	require.NotContains(t, body, "b.vcf")
	require.Contains(t, body, "507")
	require.Contains(t, body, "number-of-matches-within-limits")

	carddavReq(t, env, "REPORT", base, `<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
  <D:prop><D:getetag/></D:prop><CR:filter><CR:prop-filter name="FN"><CR:text-match collation="i;nope">x</CR:text-match></CR:prop-filter></CR:filter></CR:addressbook-query>`,
		alice.Auth).Status(403).Contains("supported-collation")
}
