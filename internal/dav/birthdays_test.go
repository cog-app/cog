package dav_test

import (
	"html"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
)

// birthdayEvents returns the birthday calendar's events as
// "SUMMARY|DTSTART|RRULE" lines, sorted.
func birthdayEvents(t *testing.T, env *cogtest.Env, u *cogtest.User) []string {
	t.Helper()
	body := req(t, env, u, "PROPFIND", cal+u.Username+"/"+dav.BirthdaysName+"/",
		`<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop></D:propfind>`, "Depth", "1").
		Status(207).ReadBody()
	field := func(ev, name string) string {
		m := regexp.MustCompile(`(?m)^` + name + `[;:](.*?)\r?$`).FindStringSubmatch(ev)
		if m == nil {
			return ""
		}
		return m[1]
	}
	var out []string
	body = unfold(html.UnescapeString(body))
	for _, part := range strings.Split(body, "<C:calendar-data>")[1:] {
		ev, _, _ := strings.Cut(part, "</C:calendar-data>")
		out = append(out, field(ev, "SUMMARY")+"|"+field(ev, "DTSTART")+"|"+field(ev, "RRULE"))
	}
	slices.Sort(out)
	return out
}

// birthdayCards are cards from the corpus with a date in every syntax the
// versions allow, and what the birthday calendar makes of them.
var birthdayCards = []string{
	"ez-vcard/rfc6350-example.vcf",                                   // 4.0: --0203, a date-time ANNIVERSARY
	"aosp-vcard/v21_winmo_65.vcf",                                    // 2.1: ANNIVERSARY;VALUE=DATE
	"ez-vcard/John_Doe_MS_OUTLOOK.vcf",                               // 2.1: X-MS-ANNIVERSARY
	"ez-vcard/thunderbird-MoreFunctionsForAddressBook-extension.vcf", // 3.0: X-ANNIVERSARY
	"folkerkinzel-vcards/v4.vcf",                                     // 4.0: 19723101 is no date
	"folkerkinzel-vcards/v2_1.vcf",                                   // 2.1: X-WAB-WEDDING_ANNIVERSARY
	"ical.js/vcard3.vcf",                                             // 3.0: a second and third BDAY, kept as leftovers
	"ical.js/escape_semicolon.vcf",                                   // 3.0: date-time without zone, X-ANNIVERSARY
	"ez-vcard/John_Doe_EVOLUTION.vcf",                                // 3.0: X-EVOLUTION-ANNIVERSARY
	"aosp-vcard/v21_x_android_custom_after_photo.vcf",                // 2.1: extended date, no FN
	"ccs-caldavtester/vcardput-10.vcf",                               // Apple: item3.X-ABDATE labelled anniversary
	"ez-vcard/gmail-single.vcf",                                      // Google's Apple-style anniversary
	"ez-vcard/gmail-single2.vcf",                                     // a labelled anniversary among other dates
	"ez-vcard/John_Doe_GMAIL.vcf",                                    // the same
}

var birthdaysWanted = []string{
	"🎂 All Custom Fields Here (1999)|VALUE=DATE:19990318|FREQ=YEARLY",
	"🎂 Greg Dartmouth (1960)|VALUE=DATE:19600910|FREQ=YEARLY",
	"🎂 Mr. John Richter\\, James Doe Sr. (1980)|VALUE=DATE:19800322|FREQ=YEARLY",
	"🎂 VCard Test (1912)|VALUE=DATE:19120623|FREQ=YEARLY",
	"💍 All Custom Fields Here (1995)|VALUE=DATE:19950521|FREQ=YEARLY",
	"💍 Greg Dartmouth (1970)|VALUE=DATE:19700602|FREQ=YEARLY",
	"💍 Mr. John Richter\\, James Doe Sr. (1975)|VALUE=DATE:19750301|FREQ=YEARLY",
	"💍 VCard Test (1930)|VALUE=DATE:19300320|FREQ=YEARLY",
	"🎂 First Last NextCloud (2019)|VALUE=DATE:20190210|FREQ=YEARLY",
	"🎂 John Doe (1970)|VALUE=DATE:19700921|FREQ=YEARLY",
	"🎂 Maxl (1972)|VALUE=DATE:19720131|FREQ=YEARLY",
	"🎂 Mr. John Q. Public\\, Esq. (1996)|VALUE=DATE:19960415|FREQ=YEARLY",
	"🎂 Mr. John Richter James Doe Sr. (1980)|VALUE=DATE:19800322|FREQ=YEARLY",
	"🎂 Mr. John Richter\\, James Doe Sr. (1980)|VALUE=DATE:19800322|FREQ=YEARLY",
	"🎂 Roid Ando (1975)|VALUE=DATE:19750820|FREQ=YEARLY",
	"🎂 Simon Perreault|VALUE=DATE:19700203|FREQ=YEARLY",
	"💍 Example (2009)|VALUE=DATE:20091010|FREQ=YEARLY",
	"💍 First Last NextCloud (2019)|VALUE=DATE:20190220|FREQ=YEARLY",
	"💍 John Doe (1990)|VALUE=DATE:19900430|FREQ=YEARLY",
	"💍 Maxl (2000)|VALUE=DATE:20001224|FREQ=YEARLY",
	"💍 Mr. John Richter James Doe Sr. (2011)|VALUE=DATE:20110113|FREQ=YEARLY",
	"💍 Mr. John Richter\\, James Doe Sr. (1980)|VALUE=DATE:19800322|FREQ=YEARLY",
	"💍 Simon Perreault (2009)|VALUE=DATE:20090808|FREQ=YEARLY",
}

// apple is a card as Apple Contacts writes a birthday without a year (3.0
// has no form for one), born on a leap day.
const apple = "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:leap\r\nN:Kid;Leap;;;\r\nFN:Leap Kid\r\n" +
	"BDAY;X-APPLE-OMIT-YEAR=1604:1604-02-29\r\nEND:VCARD\r\n"

func TestBirthdayCalendar(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	srv := env.Providers.DAV
	me := davUser(t, env, alice)
	books := "/remote.php/dav/addressbooks/alice/contacts/"

	bc, err := srv.EnableBirthdays(me)
	require.NoError(t, err)
	assert.Equal(t, dav.KindBirthdays, bc.Kind)
	assert.Equal(t, 0, len(birthdayEvents(t, env, alice)))
	again, err := srv.EnableBirthdays(me)
	require.NoError(t, err)
	assert.Equal(t, bc.ID, again.ID)

	// Every syntax, from an import.
	book, err := srv.Collections.GetOrDefault(alice.ID, dav.HomeAddressBooks, "contacts")
	require.NoError(t, err)
	for _, f := range birthdayCards {
		data, err := os.ReadFile("../../testdata/dav/vcf/" + f)
		require.NoError(t, err)
		res, err := srv.Import(alice.Username, book, strings.NewReader(string(data)))
		require.NoError(t, err, f)
		assert.Equal(t, 0, res.Failed, f)
	}
	want := slices.Sorted(slices.Values(birthdaysWanted))
	assert.Equal(t, want, birthdayEvents(t, env, alice))

	// Read-only to everyone.
	bpath := cal + "alice/" + dav.BirthdaysName + "/"
	req(t, env, alice, "PUT", bpath+"x.ics", ics("VEVENT", "x", "Mine")).Status(403)
	props := req(t, env, alice, "PROPFIND", bpath, "", "Depth", "0").Status(207).ReadBody()
	assert.NotContains(t, props, "<D:write-content/>")
	assert.NotContains(t, props, "CS:source")

	// A card from CardDAV: Apple's yearless leap day falls on the last day
	// of February.
	_, token := syncReport(t, env, alice, bpath, "")
	req(t, env, alice, "PUT", books+"leap.vcf", apple).Status(201)
	body, token := syncReport(t, env, alice, bpath, token)
	hrefs := regexp.MustCompile(`<D:href>[^<]*/([^/<]+\.ics)</D:href>`).FindAllStringSubmatch(body, -1)
	require.Equal(t, 1, len(hrefs), body)
	leap := hrefs[0][1]
	ev := req(t, env, alice, "GET", bpath+leap, "").Status(200).ReadBody()
	assert.Contains(t, ev, "SUMMARY:🎂 Leap Kid\r\n")
	assert.Contains(t, ev, "DTSTART;VALUE=DATE:19720229\r\n")
	assert.Contains(t, ev, "RRULE:FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=-1\r\n")
	assert.Contains(t, ev, "TRANSP:TRANSPARENT\r\n")
	etag := req(t, env, alice, "GET", bpath+leap, "").Header.Get("ETag")

	// Editing something else rewrites nothing: same event, same ETag, no
	// new sync token.
	req(t, env, alice, "PUT", books+"leap.vcf", strings.Replace(apple, "END:VCARD", "NOTE:hi\r\nEND:VCARD", 1)).Status(204)
	_, next := syncReport(t, env, alice, bpath, token)
	assert.Equal(t, token, next)
	assert.Equal(t, etag, req(t, env, alice, "GET", bpath+leap, "").Header.Get("ETag"))

	// Changing the date replaces the event in place: same URI and UID.
	req(t, env, alice, "PUT", books+"leap.vcf", strings.Replace(apple, "1604-02-29", "2001-07-04", 1)).Status(204)
	body, token = syncReport(t, env, alice, bpath, token)
	assert.Contains(t, body, leap)
	ev = req(t, env, alice, "GET", bpath+leap, "").Status(200).ReadBody()
	assert.Contains(t, ev, "SUMMARY:🎂 Leap Kid (2001)\r\n")
	assert.Contains(t, ev, "UID:"+strings.TrimSuffix(leap, ".ics")+"\r\n")

	// From the web UI too.
	plug, _ := env.Providers.Plugins.Plugin("contacts")
	cp := plug.(*contacts.Plugin)
	store, err := cp.Store(alice.Username)
	require.NoError(t, err)
	card, err := store.FindByUID("leap")
	require.NoError(t, err)
	require.NoError(t, store.Load(card))
	card.VCard.Birthday = "1850-12-31"
	_, err = store.Save(card.AddressBookID, card, card.VCard, env.Now())
	require.NoError(t, err)
	ev = req(t, env, alice, "GET", bpath+leap, "").Status(200).ReadBody()
	assert.Contains(t, ev, "SUMMARY:🎂 Leap Kid (1850)\r\n")
	assert.Contains(t, ev, "DTSTART;VALUE=DATE:19001231\r\n") // series start from 1900
	assert.Contains(t, ev, "DTEND;VALUE=DATE:19010101\r\n")

	// Removing the date, then the card, removes the event, with a
	// tombstone for clients.
	_, token = syncReport(t, env, alice, bpath, "")
	req(t, env, alice, "PUT", books+"leap.vcf", strings.Replace(apple, "BDAY;X-APPLE-OMIT-YEAR=1604:1604-02-29\r\n", "", 1)).Status(204)
	body, token = syncReport(t, env, alice, bpath, token)
	assert.Contains(t, body, leap+"</D:href><D:status>HTTP/1.1 404 Not Found")
	req(t, env, alice, "PUT", books+"leap.vcf", apple).Status(204)
	req(t, env, alice, "DELETE", books+"leap.vcf", "").Status(204)
	body, _ = syncReport(t, env, alice, bpath, token)
	assert.Contains(t, body, leap+"</D:href><D:status>HTTP/1.1 404 Not Found")
	assert.Equal(t, want, birthdayEvents(t, env, alice))

	// A second address book adds its own; deleting a book takes its
	// events with it.
	req(t, env, alice, "MKCOL", "/remote.php/dav/addressbooks/alice/family/", "").Status(201)
	req(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/family/leap.vcf", apple).Status(201)
	assert.Equal(t, len(birthdaysWanted)+1, len(birthdayEvents(t, env, alice)))
	req(t, env, alice, "DELETE", "/remote.php/dav/addressbooks/alice/contacts/", "").Status(204)
	assert.Equal(t, []string{"🎂 Leap Kid|VALUE=DATE:19720229|FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=-1"}, birthdayEvents(t, env, alice))

	// Turning it off deletes the calendar; on again, it is rebuilt.
	require.NoError(t, srv.DisableBirthdays(me))
	req(t, env, alice, "PROPFIND", bpath, "", "Depth", "0").Status(404)
	_, err = srv.EnableBirthdays(me)
	require.NoError(t, err)
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))

	// Another user's cards reach alice's calendar only through a share
	// that lets her read them, and follow it.
	bob := env.CreateUser(t, "bob", "pass")
	bobCard := strings.NewReplacer("UID:leap", "UID:bobs", "Leap Kid", "Bob's Mum", "1604-02-29", "1950-05-01").Replace(apple)
	req(t, env, bob, "PUT", "/remote.php/dav/addressbooks/bob/contacts/mum.vcf", bobCard).Status(201)
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))
	bobBook, err := srv.Collections.GetOrDefault(bob.ID, dav.HomeAddressBooks, "contacts")
	require.NoError(t, err)
	share(t, env, bob, alice, bobBook.ID, plugin.PermFreeBusy) // reads nothing
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))
	rec, err := env.Providers.Shares.Create(bob.ID, dav.ShareType, bobBook.ID.String(), "USER", alice.ID, plugin.PermRead)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(birthdayEvents(t, env, alice), "\n"), "🎂 Bob's Mum (1950)|VALUE=DATE:19500501")
	// Bob's edits reach her calendar, though she wrote nothing.
	req(t, env, bob, "PUT", "/remote.php/dav/addressbooks/bob/contacts/mum.vcf", strings.Replace(bobCard, "1950-05-01", "1950-05-02", 1)).Status(204)
	assert.Contains(t, strings.Join(birthdayEvents(t, env, alice), "\n"), "🎂 Bob's Mum (1950)|VALUE=DATE:19500502")
	// Revoking the share takes the events away at once.
	require.NoError(t, env.Providers.Shares.Delete(rec.ID))
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))

	// Through a group: sharing with it reaches its members; one who
	// leaves loses the events at the next sweep.
	grp, err := env.Providers.Groups.Create("family", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.User))
	_, err = env.Providers.Shares.Create(bob.ID, dav.ShareType, bobBook.ID.String(), "GROUP", grp.ID, plugin.PermRead)
	require.NoError(t, err)
	assert.Equal(t, 2, len(birthdayEvents(t, env, alice)))
	require.NoError(t, env.Providers.Groups.RemoveMember(grp.ID, alice.ID))
	require.NoError(t, srv.SweepBirthdays())
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))

	// Deleting a shared book removes its events from the sharee's
	// calendar.
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.User))
	require.NoError(t, srv.SweepBirthdays())
	assert.Equal(t, 2, len(birthdayEvents(t, env, alice)))
	req(t, env, bob, "DELETE", "/remote.php/dav/addressbooks/bob/contacts/", "").Status(204)
	assert.Equal(t, 1, len(birthdayEvents(t, env, alice)))
}
