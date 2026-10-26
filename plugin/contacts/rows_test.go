package contacts_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

func sessionAuth(t testing.TB, env *cogtest.Env, userID string) (cogtest.HTTPOpt, cogtest.HTTPOpt) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(userID)
	require.NoError(t, err)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	return cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), cogtest.Header("X-CSRF-Token", token)
}

func davPath(user, uri string) string {
	return "/remote.php/dav/addressbooks/" + user + "/contacts/" + uri
}

// getCard fetches a card over CardDAV and decodes it.
func getCard(t *testing.T, env *cogtest.Env, user *cogtest.User, uri string) (*vcard.Card, string) {
	t.Helper()
	body := carddavReq(t, env, "GET", davPath(user.Username, uri), "", user.Auth).Status(200).ReadBody()
	comps, err := contentline.Parse(strings.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 1, len(comps))
	return vcard.Decode(comps[0]), body
}

// TestFixturesThroughRows PUTs every card in the corpus and checks that what
// is served back decodes to exactly what was sent: the rows, the photo files
// and the leftovers' JSON keep everything the codec does.
func TestFixturesThroughRows(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	n := 0
	seen := map[string]bool{}
	for _, root := range []string{"../../testdata/dav/vcf", "../../tmp/dav-fixtures-gpl"} {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !strings.EqualFold(filepath.Ext(path), ".vcf") || strings.Contains(path, "/invalid/") {
				return err
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			comps, err := contentline.Parse(bytes.NewReader(data))
			if err != nil {
				return nil
			}
			for _, c := range comps {
				n++
				// A collection holds one card per UID; the corpus repeats some.
				switch p := c.Prop("UID"); {
				case p == nil || p.Value == "":
					c.Props = append(c.Props, contentline.Property{Name: "UID", Value: "fixture-" + strconv.Itoa(n)})
				case seen[p.Value]:
					p.Value = "fixture-" + strconv.Itoa(n)
				default:
					seen[p.Value] = true
				}
				var b bytes.Buffer
				require.NoError(t, contentline.Write(&b, []*contentline.Component{c}))
				uri := "f" + strconv.Itoa(n) + ".vcf"
				t.Run(path+"#"+uri, func(t *testing.T) {
					carddavReq(t, env, "PUT", davPath("alice", uri), b.String(), alice.Auth).Status(201)
					want := vcard.Decode(c)
					want.AssignGroups()
					got, body := getCard(t, env, alice, uri)
					if !reflect.DeepEqual(want, got) {
						t.Fatalf("served card differs from the one sent\n%s", body)
					}
				})
			}
			return nil
		}))
	}
	if n < 100 {
		t.Fatalf("only %d cards in the corpus", n)
	}
}

// appleCard is an iOS-style card: labels in groups, phonetic names, an
// address with Apple's country code beside it, a photo and a property Cog
// does not model.
const appleCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iPhone OS 17.0//EN\r\n" +
	"N:Doe;Jane;;;\r\nFN:Jane Doe\r\nX-PHONETIC-FIRST-NAME:Jayn\r\nORG:Acme;Research\r\n" +
	"item1.EMAIL;type=INTERNET;type=pref:jane@example.com\r\nitem1.X-ABLabel:_$!<Other>!$_\r\n" +
	"TEL;type=CELL;type=VOICE;type=pref:555-0100\r\nTEL;type=HOME:555-0101\r\n" +
	"item2.ADR;type=HOME:;;1 Main St;Springfield;;12345;USA\r\nitem2.X-ABADR:us\r\n" +
	"PHOTO;ENCODING=b;TYPE=JPEG:AQIDBA==\r\nX-APPLE-SOMETHING:keep\r\n" +
	"UID:jane\r\nREV:2020-01-01T00:00:00Z\r\nEND:VCARD\r\n"

// TestWebEditKeepsClientData edits an iOS card in the web form and checks
// that only what the form shows changed: labels, other types, the address,
// the photo and unknown properties stay; the phonetic name goes with the
// name it spelled; REV moves; the card keeps its version.
func TestWebEditKeepsClientData(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	_, s := store(t, env, alice.Username)
	carddavReq(t, env, "PUT", davPath("alice", "jane.vcf"), appleCard, alice.Auth).Status(201)
	before, _ := getCard(t, env, alice, "jane.vcf")

	card, err := s.FindByUID("jane")
	require.NoError(t, err)
	assert.Equal(t, "jane@example.com", card.Email)
	assert.Equal(t, "555-0100", card.Tel)
	assert.Equal(t, "Acme", card.Org)
	require.True(t, card.HasPhoto())

	cookie, csrf := sessionAuth(t, env, alice.ID)
	form := url.Values{
		"given": {"Janet"}, "family": {"Doe"}, "fn": {"Jane Doe"},
		"org": {"Acme"}, "title": {""}, "note": {""},
		"email_index": {"0", "new"}, "email_type": {"INTERNET", "WORK"}, "email_value": {"jane@example.com", "jd@work.example"},
		// The cell phone becomes a work phone; the home phone is removed.
		"phone_index": {"0"}, "phone_type": {"WORK"}, "phone_value": {"555-0100"},
	}
	env.PostForm(t, fmt.Sprintf("/apps/contacts/contacts/%d", card.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)

	after, body := getCard(t, env, alice, "jane.vcf")
	assert.Equal(t, "3.0", after.Version)
	assert.Equal(t, []string{"Janet"}, after.N.Given)
	assert.Equal(t, "Janet Doe", after.FN, "FN followed N, as it did before")
	assert.False(t, after.Leftovers.Has("X-PHONETIC-FIRST-NAME"))
	assert.True(t, after.Leftovers.Has("X-APPLE-SOMETHING"))
	assert.True(t, after.Leftovers.Has("PRODID"))
	assert.Equal(t, []string{"Acme", "Research"}, after.Org)
	require.Equal(t, 2, len(after.Emails))
	assert.Equal(t, before.Emails[0], after.Emails[0], "the unchanged email keeps its group, label and types")
	assert.Equal(t, []string{"WORK"}, after.Emails[1].Types)
	require.Equal(t, 1, len(after.Phones))
	assert.Equal(t, []string{"WORK", "VOICE", "pref"}, after.Phones[0].Types)
	assert.Equal(t, before.Addresses, after.Addresses)
	assert.Contains(t, body, "item2.X-ABADR:us")
	assert.Equal(t, before.Photo, after.Photo)
	assert.Equal(t, "20260314T060000Z", after.Rev)
}

// TestCardVersionKept edits a vCard 4.0 group in the web UI: it stays 4.0,
// with its members.
func TestCardVersionKept(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	_, s := store(t, env, alice.Username)
	group := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:g1\r\nKIND:group\r\nFN:Team\r\n" +
		"MEMBER:urn:uuid:a\r\nMEMBER:urn:uuid:b\r\nPHOTO:data:image/png;base64,AQID\r\nEND:VCARD\r\n"
	carddavReq(t, env, "PUT", davPath("alice", "g1.vcf"), group, alice.Auth).Status(201)

	card, err := s.FindByUID("g1")
	require.NoError(t, err)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	env.PostForm(t, fmt.Sprintf("/apps/contacts/contacts/%d", card.ID), url.Values{"fn": {"The Team"}},
		cookie, csrf, cogtest.StopRedirect).Status(303)

	after, body := getCard(t, env, alice, "g1.vcf")
	assert.Equal(t, "4.0", after.Version)
	assert.True(t, after.IsGroup())
	assert.Equal(t, 2, len(after.Members))
	assert.Equal(t, "The Team", after.FN)
	assert.True(t, after.N == nil, "a group has no N, and the form did not add one")
	assert.Contains(t, body, "PHOTO:data:image/png;base64,AQID\r\n")
}

// TestPhotoUpload uploads a photo in the web UI: it becomes the card's PHOTO
// over CardDAV, and the file of the one it replaced goes.
func TestPhotoUpload(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	carddavReq(t, env, "PUT", davPath("alice", "jane.vcf"), appleCard, alice.Auth).Status(201)
	_, s := store(t, env, alice.Username)
	card, err := s.FindByUID("jane")
	require.NoError(t, err)
	dir := filepath.Join(env.Providers.Config.DataDir, "contacts", "alice", "photos")
	files, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, 1, len(files))
	old := files[0].Name()

	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(1, 1, color.RGBA{R: 255, A: 255})
	var pngData bytes.Buffer
	require.NoError(t, png.Encode(&pngData, img))
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="photo"; filename="face.png"`},
		"Content-Type":        {"image/png"},
	})
	require.NoError(t, err)
	_, err = fw.Write(pngData.Bytes())
	require.NoError(t, err)
	require.NoError(t, mw.Close())

	cookie, csrf := sessionAuth(t, env, alice.ID)
	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+fmt.Sprintf("/apps/contacts/contacts/%d/photo", card.ID), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp := env.Do(t, req, cookie, csrf, cogtest.StopRedirect).Status(303)
	assert.Equal(t, "", resp.Header.Get("X-Flash"))
	for _, c := range resp.Cookies() {
		assert.True(t, c.Name != "cog_flash", "upload failed: %s", c.Value)
	}

	after, text := getCard(t, env, alice, "jane.vcf")
	require.True(t, after.Photo != nil)
	assert.Equal(t, "JPEG", after.Photo.Type)
	assert.Contains(t, text, "PHOTO;ENCODING=b;TYPE=JPEG:")
	env.Get(t, fmt.Sprintf("/apps/contacts/photos/%d", card.ID), cookie).Status(200)

	files, err = os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, 1, len(files))
	assert.True(t, files[0].Name() != old, "the replaced photo's file is removed")

	// Deleting the card removes its photo.
	carddavReq(t, env, "DELETE", davPath("alice", "jane.vcf"), "", alice.Auth).Status(204)
	files, err = os.ReadDir(dir)
	require.NoError(t, err)
	assert.Equal(t, 0, len(files))
}

// TestCreateFromForm creates a contact with only a display name: it gets an
// N, which vCard 3.0 requires.
func TestCreateFromForm(t *testing.T) {
	env := cogtest.New(t, contacts.New())
	alice := env.CreateUser(t, "alice", "pass")
	cp, s := store(t, env, alice.Username)
	books, err := cp.Books(alice.ID)
	require.NoError(t, err)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	env.PostForm(t, "/apps/contacts/contacts", url.Values{
		"address_book_id": {strconv.FormatInt(books[0].ID, 10)},
		"fn":              {"Mary Ann Smith"},
		"email_index":     {"new"}, "email_type": {"WORK"}, "email_value": {"mary@example.com"},
	}, cookie, csrf, cogtest.StopRedirect).Status(303)

	cards, err := s.List(books[0].ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(cards))
	c, err := s.Get(cards[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "3.0", c.VCard.Version)
	assert.Equal(t, []string{"Smith"}, c.VCard.N.Family)
	assert.Equal(t, []string{"Mary Ann"}, c.VCard.N.Given)
	assert.Equal(t, "mary@example.com", c.Email)

	// A form without any name is refused with a message, not stored.
	env.PostForm(t, "/apps/contacts/contacts", url.Values{
		"address_book_id": {strconv.FormatInt(books[0].ID, 10)},
	}, cookie, csrf, cogtest.StopRedirect).Status(303)
	cards, err = s.List(books[0].ID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(cards))
}
