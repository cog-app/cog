package jmap_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/jmap"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

type obj = map[string]any

func newEnv(t *testing.T) *cogtest.Env {
	t.Helper()
	env := cogtest.New(t, calendar.New(), tasks.New(), contacts.New())
	env.Providers.JMAP.Calendars = true
	return env
}

var allCaps = []string{jmap.CapCore, jmap.CapContacts, jmap.CapCalendars}

// invocation is one method response.
type invocation struct {
	Name string
	Args obj
	ID   string
}

// call posts method calls and returns the responses.
func call(t *testing.T, env *cogtest.Env, u *cogtest.User, calls ...[]any) []invocation {
	t.Helper()
	body := env.PostJSON(t, jmap.APIPath, obj{"using": allCaps, "methodCalls": calls}, u.Auth).Status(200).ReadBody()
	var resp struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
		SessionState    string              `json:"sessionState"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp))
	require.True(t, resp.SessionState != "", body)
	out := make([]invocation, len(resp.MethodResponses))
	for i, r := range resp.MethodResponses {
		require.NoError(t, json.Unmarshal(r[0], &out[i].Name))
		require.NoError(t, json.Unmarshal(r[1], &out[i].Args))
		require.NoError(t, json.Unmarshal(r[2], &out[i].ID))
	}
	return out
}

// one makes one call and returns its response's arguments, failing on a
// method error.
func one(t *testing.T, env *cogtest.Env, u *cogtest.User, name string, args obj) obj {
	t.Helper()
	got := call(t, env, u, []any{name, args, "0"})
	require.Equal(t, 1, len(got))
	if got[0].Name == "error" {
		t.Fatalf("%s: %v", name, got[0].Args)
	}
	require.Equal(t, name, got[0].Name)
	return got[0].Args
}

// failing makes one call and returns its error type.
func failing(t *testing.T, env *cogtest.Env, u *cogtest.User, name string, args obj) string {
	t.Helper()
	got := call(t, env, u, []any{name, args, "0"})
	require.Equal(t, "error", got[0].Name, "%v", got[0].Args)
	return got[0].Args["type"].(string)
}

func acct(u *cogtest.User) string { return "u" + u.ID }

func session(t *testing.T, env *cogtest.Env, u *cogtest.User) obj {
	t.Helper()
	var s obj
	env.Get(t, jmap.SessionPath, u.Auth).Status(200).JSON(&s)
	return s
}

func davReq(t *testing.T, env *cogtest.Env, u *cogtest.User, method, path, body string, headers ...string) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rq := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	for i := 0; i+1 < len(headers); i += 2 {
		rq.Header.Set(headers[i], headers[i+1])
	}
	return env.Do(t, rq, u.Auth)
}

func book(t *testing.T, env *cogtest.Env, u *cogtest.User) *dav.Collection {
	t.Helper()
	_, err := env.Providers.DAV.Collections.EnsureDefault(u.ID, dav.HomeAddressBooks)
	require.NoError(t, err)
	c, err := env.Providers.DAV.Collections.Get(u.ID, dav.HomeAddressBooks, "contacts")
	require.NoError(t, err)
	return c
}

func personal(t *testing.T, env *cogtest.Env, u *cogtest.User) *dav.Collection {
	t.Helper()
	_, err := env.Providers.DAV.Collections.EnsureDefault(u.ID, dav.HomeCalendars)
	require.NoError(t, err)
	c, err := env.Providers.DAV.Collections.Get(u.ID, dav.HomeCalendars, "personal")
	require.NoError(t, err)
	return c
}

func TestSession(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")

	rq := cogtest.NewRequest(t, "GET", env.Providers.Config.BaseURL+jmap.WellKnown, nil)
	got := env.Do(t, rq, cogtest.StopRedirect)
	got.Status(http.StatusTemporaryRedirect)
	assert.Equal[any](t, jmap.SessionPath, got.Header.Get("Location"))

	env.Get(t, jmap.SessionPath).Status(401)

	s := session(t, env, alice)
	caps := s["capabilities"].(obj)
	assert.NotNil(t, caps[jmap.CapCore])
	assert.NotNil(t, caps[jmap.CapContacts])
	assert.NotNil(t, caps[jmap.CapCalendars])
	assert.Equal[any](t, "alice", s["username"])
	assert.Equal[any](t, env.Providers.Config.BaseURL+jmap.APIPath, s["apiUrl"])
	accounts := s["accounts"].(obj)
	require.Equal(t, 1, len(accounts))
	a := accounts[acct(alice)].(obj)
	assert.Equal[any](t, true, a["isPersonal"])
	assert.Equal[any](t, false, a["isReadOnly"])
	assert.Equal[any](t, acct(alice), s["primaryAccounts"].(obj)[jmap.CapContacts])

	// Without the flag, calendars are not offered.
	env.Providers.JMAP.Calendars = false
	s = session(t, env, alice)
	assert.Nil(t, s["capabilities"].(obj)[jmap.CapCalendars])
	got2 := env.PostJSON(t, jmap.APIPath, obj{"using": []string{jmap.CapCore, jmap.CapCalendars}, "methodCalls": []any{}}, alice.Auth)
	got2.Status(400).Contains("unknownCapability")
}

func TestRequestErrors(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Not JSON, by content type and by content.
	rq := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+jmap.APIPath, strings.NewReader(`{"using":[],"methodCalls":[]}`))
	rq.Header.Set("Content-Type", "text/plain")
	env.Do(t, rq, alice.Auth).Status(400).Contains("urn:ietf:params:jmap:error:notJSON")
	rq = cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+jmap.APIPath, strings.NewReader(`{`))
	rq.Header.Set("Content-Type", "application/json")
	env.Do(t, rq, alice.Auth).Status(400).Contains("urn:ietf:params:jmap:error:notJSON")
	env.PostJSON(t, jmap.APIPath, obj{"using": allCaps}, alice.Auth).Status(400).Contains("urn:ietf:params:jmap:error:notRequest")
	env.PostJSON(t, jmap.APIPath, obj{"using": []string{"urn:x"}, "methodCalls": []any{}}, alice.Auth).Status(400).Contains("unknownCapability")
	env.PostJSON(t, jmap.APIPath, obj{"using": allCaps, "methodCalls": []any{}}).Status(401)

	got := call(t, env, alice,
		[]any{"Core/echo", obj{"hello": true}, "a"},
		[]any{"Nope/get", obj{}, "b"},
		[]any{"ContactCard/get", obj{"accountId": "u-nobody"}, "c"},
		[]any{"ContactCard/get", obj{"accountId": acct(alice), "bogus": 1}, "d"},
	)
	require.Equal(t, 4, len(got))
	assert.Equal[any](t, "Core/echo", got[0].Name)
	assert.Equal[any](t, obj{"hello": true}, got[0].Args)
	assert.Equal[any](t, "unknownMethod", got[1].Args["type"])
	assert.Equal[any](t, "accountNotFound", got[2].Args["type"])
	assert.Equal[any](t, "invalidArguments", got[3].Args["type"])

	// A method of a capability the request does not use is unknown.
	body := env.PostJSON(t, jmap.APIPath, obj{"using": []string{jmap.CapCore},
		"methodCalls": []any{[]any{"ContactCard/get", obj{"accountId": acct(alice)}, "x"}}}, alice.Auth).Status(200).ReadBody()
	assert.Contains(t, body, "unknownMethod")
}

func TestBackReferences(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	b := book(t, env, alice)
	for _, uid := range []string{"a", "b"} {
		davReq(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/"+uid+".vcf",
			"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+uid+"\r\nFN:Person "+uid+"\r\nEND:VCARD\r\n").Status(201)
	}
	got := call(t, env, alice,
		[]any{"ContactCard/query", obj{"accountId": acct(alice), "sort": []any{obj{"property": "name/given"}}}, "q"},
		[]any{"ContactCard/get", obj{"accountId": acct(alice), "#ids": obj{"resultOf": "q", "name": "ContactCard/query", "path": "/ids"},
			"properties": []string{"name", "addressBookIds"}}, "g"},
		[]any{"Core/echo", obj{"#names": obj{"resultOf": "g", "name": "ContactCard/get", "path": "/list/*/name/full"}}, "e"},
		[]any{"Core/echo", obj{"#x": obj{"resultOf": "nope", "name": "ContactCard/get", "path": "/list"}}, "bad"},
	)
	require.Equal(t, 4, len(got))
	assert.Equal[any](t, 2, len(got[0].Args["ids"].([]any)))
	list := got[1].Args["list"].([]any)
	require.Equal(t, 2, len(list))
	assert.Equal[any](t, obj{"c" + b.ID.String(): true}, list[0].(obj)["addressBookIds"])
	assert.Equal[any](t, []any{"Person a", "Person b"}, got[2].Args["names"])
	assert.Equal[any](t, "invalidResultReference", got[3].Args["type"])
}

// TestContacts creates, reads, changes, queries and deletes cards over
// JMAP, and checks DAV sees each step.
func TestContacts(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	b := book(t, env, alice)
	bookID := "c" + b.ID.String()
	path := "/remote.php/dav/addressbooks/alice/contacts/"

	books := one(t, env, alice, "AddressBook/get", obj{"accountId": acct(alice)})
	require.Equal(t, 1, len(books["list"].([]any)))
	ab := books["list"].([]any)[0].(obj)
	assert.Equal[any](t, bookID, ab["id"])
	assert.Equal[any](t, true, ab["isDefault"])
	assert.Equal[any](t, obj{"mayRead": true, "mayWrite": true, "mayShare": true, "mayDelete": true}, ab["myRights"])

	start := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{}})["state"].(string)

	set := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "create": obj{
		"k1": obj{
			"@type": "Card", "version": "1.0", "addressBookIds": obj{bookID: true},
			"name":   obj{"components": []any{obj{"kind": "given", "value": "Jane"}, obj{"kind": "surname", "value": "Doe"}}, "full": "Jane Doe"},
			"emails": obj{"e1": obj{"address": "jane@example.com", "contexts": obj{"work": true}}},
			"phones": obj{"p1": obj{"number": "+1 555 0100", "features": obj{"mobile": true}}},
			"notes":  obj{"n1": obj{"note": "met at the conference", "created": "2026-01-01T00:00:00Z"}},
		},
		"bad": obj{"@type": "Card", "addressBookIds": obj{bookID: true, "c999": true}},
	}})
	created := set["created"].(obj)["k1"].(obj)
	id := created["id"].(string)
	uid := created["uid"].(string)
	require.True(t, uid != "")
	assert.Equal[any](t, "invalidProperties", set["notCreated"].(obj)["bad"].(obj)["type"])
	assert.Equal[any](t, start, set["oldState"])
	afterCreate := set["newState"].(string)

	// DAV sees the card, with what JMAP has no vCard property for kept.
	vcf := davReq(t, env, alice, "GET", path+uid+".vcf", "").Status(200).ReadBody()
	assert.Contains(t, vcf, "FN:Jane Doe")
	// N keeps the order the client gave its components in.
	assert.Contains(t, vcf, ":Doe;Jane;;;")
	assert.Contains(t, vcf, "N;X-COG-JSCOMPS=")
	assert.Contains(t, vcf, "EMAIL;TYPE=WORK;JSID=e1:jane@example.com")
	assert.Contains(t, vcf, "TEL;TYPE=CELL;JSID=p1:+1 555 0100")
	assert.Contains(t, vcf, "JSPROP;JSPTR=/notes/n1/created:")
	assert.Contains(t, vcf, "REV:")

	got := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{id, "rnope"}})
	card := got["list"].([]any)[0].(obj)
	assert.Equal[any](t, []any{"rnope"}, got["notFound"])
	assert.Equal[any](t, "Jane Doe", card["name"].(obj)["full"])
	assert.Equal[any](t, "2026-01-01T00:00:00Z", card["notes"].(obj)["n1"].(obj)["created"])
	assert.Equal[any](t, obj{"work": true}, card["emails"].(obj)["e1"].(obj)["contexts"])

	ch := one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": start})
	assert.Equal[any](t, []any{id}, ch["created"])
	assert.Equal[any](t, afterCreate, ch["newState"])

	// A patch changes one value; REV moves, which the response says.
	env.Clock.Advance(time.Hour)
	upd := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "ifInState": afterCreate, "update": obj{
		id: obj{"emails/e1/address": "jane.doe@example.com", "notes/n1/note": "met in Berlin"},
	}})
	assert.NotNil(t, upd["updated"].(obj)[id].(obj)["updated"])
	vcf = davReq(t, env, alice, "GET", path+uid+".vcf", "").Status(200).ReadBody()
	assert.Contains(t, vcf, "jane.doe@example.com")
	assert.Contains(t, vcf, "NOTE;JSID=n1:met in Berlin")
	assert.Contains(t, vcf, "JSPROP;JSPTR=/notes/n1/created:")

	// A stale state is refused.
	assert.Equal[any](t, "stateMismatch", failing(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "ifInState": afterCreate,
		"update": obj{id: obj{"notes/n1/note": "x"}}}))

	ch = one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": afterCreate})
	assert.Equal[any](t, []any{}, ch["created"])
	assert.Equal[any](t, []any{id}, ch["updated"])

	// Query: filters and sort.
	davReq(t, env, alice, "PUT", path+"zed.vcf", "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:zed\r\nFN:Zed Alpha\r\nN:Alpha;Zed;;;\r\nORG:Acme\r\nEND:VCARD\r\n").Status(201)
	q := one(t, env, alice, "ContactCard/query", obj{"accountId": acct(alice), "filter": obj{"text": "acme"}})
	assert.Equal[any](t, 1, len(q["ids"].([]any)))
	q = one(t, env, alice, "ContactCard/query", obj{"accountId": acct(alice),
		"filter": obj{"operator": "OR", "conditions": []any{obj{"email": "jane"}, obj{"organization": "acme"}}},
		"sort":   []any{obj{"property": "name/surname"}}, "calculateTotal": true, "limit": 1})
	assert.Equal[any](t, float64(2), q["total"])
	assert.Equal[any](t, 1, len(q["ids"].([]any)))
	assert.Equal[any](t, "unsupportedFilter", failing(t, env, alice, "ContactCard/query", obj{"accountId": acct(alice), "filter": obj{"createdAfter": "2026-01-01T00:00:00Z"}}))
	assert.Equal[any](t, "cannotCalculateChanges", failing(t, env, alice, "ContactCard/queryChanges", obj{"accountId": acct(alice)}))

	// Destroy: gone over DAV too, and reported.
	before := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{}})["state"].(string)
	del := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "destroy": []any{id, "rgone"}})
	assert.Equal[any](t, []any{id}, del["destroyed"])
	assert.Equal[any](t, "notFound", del["notDestroyed"].(obj)["rgone"].(obj)["type"])
	davReq(t, env, alice, "GET", path+uid+".vcf", "").Status(404)
	ch = one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": before})
	assert.Equal[any](t, []any{id}, ch["destroyed"])

	// An unknown state cannot be calculated from.
	assert.Equal[any](t, "cannotCalculateChanges", failing(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": "garbage"}))
}

// TestChangesLimit checks maxChanges: intermediate states, never splitting
// one version.
func TestChangesLimit(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	book(t, env, alice)
	start := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{}})["state"].(string)
	for _, uid := range []string{"a", "b", "c"} {
		davReq(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/"+uid+".vcf",
			"BEGIN:VCARD\r\nVERSION:3.0\r\nUID:"+uid+"\r\nFN:"+uid+"\r\nEND:VCARD\r\n").Status(201)
	}
	state := start
	var seen []any
	for range 3 {
		ch := one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": state, "maxChanges": 2})
		seen = append(seen, ch["created"].([]any)...)
		state = ch["newState"].(string)
		if ch["hasMoreChanges"] == false {
			break
		}
	}
	assert.Equal[any](t, 3, len(seen))
	ch := one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": state})
	assert.Equal[any](t, 0, len(ch["created"].([]any)))
}

// TestCrossProtocol edits over JMAP a card a DAV client wrote, and checks
// that what Cog does not model survives and the vendor copies of a changed
// name go, as a web edit does.
func TestCrossProtocol(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	b := book(t, env, alice)
	path := "/remote.php/dav/addressbooks/alice/contacts/apple.vcf"
	davReq(t, env, alice, "PUT", path, "BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iOS 17//EN\r\nUID:apple-1\r\n"+
		"N:Appleseed;John;;;\r\nFN:John Appleseed\r\nX-PHONETIC-FIRST-NAME:Jon\r\n"+
		"item1.EMAIL;type=INTERNET;type=pref:john@example.com\r\nitem1.X-ABLabel:_$!<Other>!$_\r\n"+
		"X-SOCIALPROFILE;type=twitter:x-apple:johnny\r\nEND:VCARD\r\n").Status(201)
	id := "r" + b.ID.String() + "-" + b64("apple.vcf")
	got := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{id}})
	card := got["list"].([]any)[0].(obj)
	email := card["emails"].(obj)["1"].(obj)
	assert.Equal[any](t, "_$!<Other>!$_", email["label"])

	// A patch may not point inside an array (RFC 8620 section 5.3).
	bad := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "update": obj{
		id: obj{"name/components/1/value": "Johnny"},
	}})
	assert.Equal[any](t, "invalidPatch", bad["notUpdated"].(obj)[id].(obj)["type"])
	upd := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(alice), "update": obj{
		id: obj{"name/full": "Johnny Appleseed", "name/components": []any{
			obj{"kind": "surname", "value": "Appleseed"}, obj{"kind": "given", "value": "Johnny"}}},
	}})
	assert.Nil(t, upd["notUpdated"])
	vcf := davReq(t, env, alice, "GET", path, "").Status(200).ReadBody()
	assert.Contains(t, vcf, "FN:Johnny Appleseed")
	assert.Contains(t, vcf, "N:Appleseed;Johnny;;;")
	assert.Contains(t, vcf, "item1.EMAIL;TYPE=INTERNET,pref:john@example.com")
	assert.Contains(t, vcf, "item1.X-ABLabel:_$!<Other>!$_")
	assert.Contains(t, vcf, "X-SOCIALPROFILE;type=twitter:x-apple:johnny")
	assert.Contains(t, vcf, "PRODID:-//Apple Inc.//iOS 17//EN")
	assert.NotContains(t, vcf, "X-PHONETIC-FIRST-NAME")

	// A DAV change is a JMAP change.
	state := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{}})["state"].(string)
	davReq(t, env, alice, "DELETE", path, "").Status(204)
	ch := one(t, env, alice, "ContactCard/changes", obj{"accountId": acct(alice), "sinceState": state})
	assert.Equal[any](t, []any{id}, ch["destroyed"])
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestAddressBookSet(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	book(t, env, alice)
	start := one(t, env, alice, "AddressBook/get", obj{"accountId": acct(alice)})["state"].(string)

	// A book and a card in it, in one request.
	got := call(t, env, alice,
		[]any{"AddressBook/set", obj{"accountId": acct(alice), "create": obj{"b1": obj{"name": "Work", "description": "colleagues"}}}, "0"},
		[]any{"ContactCard/set", obj{"accountId": acct(alice), "create": obj{"k": obj{"@type": "Card",
			"addressBookIds": obj{"#b1": true}, "name": obj{"full": "Colleague"}}}}, "1"},
	)
	bid := got[0].Args["created"].(obj)["b1"].(obj)["id"].(string)
	require.NotNil(t, got[1].Args["created"], "%v", got[1].Args)

	ch := one(t, env, alice, "AddressBook/changes", obj{"accountId": acct(alice), "sinceState": start})
	assert.Equal[any](t, []any{bid}, ch["created"])

	upd := one(t, env, alice, "AddressBook/set", obj{"accountId": acct(alice), "update": obj{bid: obj{"name": "Office"}}})
	assert.Equal[any](t, obj{bid: nil}, upd["updated"])
	ch2 := one(t, env, alice, "AddressBook/changes", obj{"accountId": acct(alice), "sinceState": ch["newState"]})
	assert.Equal[any](t, []any{bid}, ch2["updated"])
	assert.Equal[any](t, "invalidProperties", one(t, env, alice, "AddressBook/set", obj{"accountId": acct(alice),
		"update": obj{bid: obj{"myRights/mayRead": false}}})["notUpdated"].(obj)[bid].(obj)["type"])

	// Not empty: refused, unless asked.
	des := one(t, env, alice, "AddressBook/set", obj{"accountId": acct(alice), "destroy": []any{bid}})
	assert.Equal[any](t, "addressBookHasContents", des["notDestroyed"].(obj)[bid].(obj)["type"])
	des = one(t, env, alice, "AddressBook/set", obj{"accountId": acct(alice), "destroy": []any{bid}, "onDestroyRemoveContents": true})
	assert.Equal[any](t, []any{bid}, des["destroyed"])
	ch3 := one(t, env, alice, "AddressBook/changes", obj{"accountId": acct(alice), "sinceState": ch2["newState"]})
	assert.Equal[any](t, []any{bid}, ch3["destroyed"])
	// The card's state names a collection that is gone.
	assert.Equal[any](t, "cannotCalculateChanges", failing(t, env, alice, "ContactCard/changes",
		obj{"accountId": acct(alice), "sinceState": got[1].Args["newState"]}))
}

// TestSharing checks that a shared address book is an account of its
// owner's, with the sharee's rights.
func TestSharing(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	b := book(t, env, bob)
	davReq(t, env, bob, "PUT", "/remote.php/dav/addressbooks/bob/contacts/x.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:x\r\nFN:Bob's friend\r\nEND:VCARD\r\n").Status(201)
	rec, err := env.Providers.Shares.Create(bob.ID, dav.ShareType, b.ID.String(), "USER", alice.ID, 1)
	require.NoError(t, err)

	s := session(t, env, alice)
	shared := s["accounts"].(obj)[acct(bob)].(obj)
	assert.Equal[any](t, false, shared["isPersonal"])
	assert.Equal[any](t, true, shared["isReadOnly"])

	got := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(bob)})
	require.Equal(t, 1, len(got["list"].([]any)))
	state := got["state"].(string)
	ab := one(t, env, alice, "AddressBook/get", obj{"accountId": acct(bob)})["list"].([]any)[0].(obj)
	assert.Equal[any](t, obj{"mayRead": true, "mayWrite": false, "mayShare": false, "mayDelete": false}, ab["myRights"])

	set := one(t, env, alice, "ContactCard/set", obj{"accountId": acct(bob), "create": obj{"k": obj{"@type": "Card",
		"addressBookIds": obj{"c" + b.ID.String(): true}, "name": obj{"full": "x"}}}})
	assert.Equal[any](t, "forbidden", set["notCreated"].(obj)["k"].(obj)["type"])
	assert.Equal[any](t, "forbidden", one(t, env, alice, "AddressBook/set", obj{"accountId": acct(bob),
		"destroy": []any{"c" + b.ID.String()}})["notDestroyed"].(obj)["c"+b.ID.String()].(obj)["type"])

	// Losing the share loses the account.
	require.NoError(t, env.Providers.Shares.Delete(rec.ID))
	assert.Equal[any](t, "accountNotFound", failing(t, env, alice, "ContactCard/changes", obj{"accountId": acct(bob), "sinceState": state}))
}
