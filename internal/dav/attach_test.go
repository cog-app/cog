package dav_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

const agenda = "<html>\n  <body>\n    <h1>Agenda</h1>\n  </body>\n</html>\n"

// addAttachment POSTs data to an event as RFC 8607's example does.
func addAttachment(t *testing.T, env *cogtest.Env, u *cogtest.User, path, query, data string) *cogtest.Response {
	t.Helper()
	return req(t, env, u, "POST", path+"?"+query, data,
		"Content-Type", `text/html; charset="utf-8"`,
		"Content-Disposition", "attachment;filename=agenda.html",
		"Prefer", "return=representation")
}

var attachLine = regexp.MustCompile(`ATTACH;FMTTYPE=([^;]+);MANAGED-ID=([^;]+);SIZE=(\d+);FILENAME=([^:]+):(\S+)\r\n`)

// anonymous fetches path without credentials.
func anonymous(t *testing.T, env *cogtest.Env, path string) *cogtest.Response {
	t.Helper()
	return env.Do(t, cogtest.NewRequest(t, "GET", env.Providers.Config.BaseURL+path, nil))
}

// mailCalendar returns the text/calendar part of an email.
func mailCalendar(t *testing.T, raw []byte) string {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	require.NoError(t, err)
	var walk func(r *multipart.Reader) string
	walk = func(r *multipart.Reader) string {
		for {
			p, err := r.NextPart()
			if err == io.EOF {
				return ""
			}
			require.NoError(t, err)
			ct, ps, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if strings.HasPrefix(ct, "multipart/") {
				if s := walk(multipart.NewReader(p, ps["boundary"])); s != "" {
					return s
				}
				continue
			}
			if ct != "text/calendar" {
				continue
			}
			var body io.Reader = p
			if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
				body = base64.NewDecoder(base64.StdEncoding, p)
			}
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			return string(data)
		}
	}
	return unfold(walk(multipart.NewReader(msg.Body, params["boundary"])))
}

// TestManagedAttachments follows an attachment through RFC 8607's add,
// update and remove, and checks who may fetch it.
func TestManagedAttachments(t *testing.T) {
	env, alice, bob := schedEnv(t)
	dave := env.CreateUser(t, "dave", "pass")
	erin := env.CreateUser(t, "erin", "pass")
	share(t, env, alice, erin, personal(t, env, alice).ID, plugin.PermRead)
	ev := cal + "alice/personal/meet-1.ics"
	req(t, env, alice, "PUT", ev, meeting).Status(201)
	before := req(t, env, alice, "GET", ev, "").Status(200).Header.Get("ETag")

	// Discovery: the home's OPTIONS and the limits, given only by name.
	assert.Contains(t, req(t, env, alice, "OPTIONS", cal+"alice/", "").Status(200).Header.Get("DAV"), "calendar-managed-attachments")
	props := req(t, env, alice, "PROPFIND", cal+"alice/personal/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:max-attachment-size/><C:max-attachments-per-resource/></D:prop></D:propfind>`, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, props, "<C:max-attachment-size>26214400</C:max-attachment-size>")
	assert.Contains(t, props, "<C:max-attachments-per-resource>20</C:max-attachments-per-resource>")
	req(t, env, alice, "PROPFIND", cal+"alice/personal/", "", "Depth", "0").NotContains("max-attachment-size")
	req(t, env, alice, "PROPFIND", cal+"alice/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:managed-attachments-server-URL/></D:prop></D:propfind>`, "Depth", "0").
		Contains("<C:managed-attachments-server-URL/>")

	// Add: 201, the new MANAGED-ID, and the event as stored, whose ETag is
	// the one served.
	add := addAttachment(t, env, alice, ev, "action=attachment-add", agenda).Status(201)
	id := add.Header.Get("Cal-Managed-ID")
	require.True(t, id != "")
	body := unfold(add.ReadBody())
	m := attachLine.FindStringSubmatch(body)
	require.True(t, m != nil, body)
	assert.Equal(t, "text/html", m[1])
	assert.Equal(t, id, m[2])
	assert.Equal(t, strconv.Itoa(len(agenda)), m[3])
	assert.Equal(t, "agenda.html", m[4])
	url := m[5]
	assert.Equal(t, env.Providers.Config.BaseURL+"/remote.php/dav/attachments/alice/"+id, url)
	etag := add.Header.Get("ETag")
	assert.True(t, etag != before)
	assert.Equal(t, etag, req(t, env, alice, "GET", ev, "").Status(200).Header.Get("ETag"))
	assert.Contains(t, body, "SEQUENCE:1\r\n")
	path := strings.TrimPrefix(url, env.Providers.Config.BaseURL)

	// The owner fetches it, as a download that cannot run on Cog's origin.
	got := req(t, env, alice, "GET", path, "").Status(200)
	assert.Equal(t, agenda, got.ReadBody())
	assert.Contains(t, got.Header.Get("Content-Disposition"), `filename=agenda.html`)
	assert.Equal(t, "sandbox", got.Header.Get("Content-Security-Policy"))
	assert.Equal(t, "nosniff", got.Header.Get("X-Content-Type-Options"))

	// Bob, an attendee, got the change with the attachment by reference,
	// and may fetch it; so may erin, whom the calendar is shared with.
	// Dave may not, and nobody without credentials.
	bobs := unfold(req(t, env, bob, "GET", cal+"bob/personal/meet-1.ics", "").Status(200).ReadBody())
	assert.Contains(t, bobs, "MANAGED-ID="+id+";")
	assert.Contains(t, bobs, ":"+url+"\r\n")
	req(t, env, bob, "GET", path, "").Status(200).Equals(agenda)
	req(t, env, erin, "GET", path, "").Status(200).Equals(agenda)
	req(t, env, dave, "GET", path, "").Status(403)
	anonymous(t, env, path).Status(401)
	// Attachments change only through their event.
	req(t, env, alice, "DELETE", path, "").Status(405)
	req(t, env, alice, "PUT", path, "x").Status(405)

	// Carol, outside Cog, gets the URL with a token that works only for her.
	msgs := env.Mailbox.ForRecipient("carol@elsewhere.example")
	sent := ""
	for _, msg := range msgs {
		if c := mailCalendar(t, msg.Raw); strings.Contains(c, "ATTACH") {
			sent = c
		}
	}
	tm := regexp.MustCompile(`ATTACH;[^:]*:(\S+\?token=\S+)\r\n`).FindStringSubmatch(sent)
	require.True(t, tm != nil, sent)
	assert.NotContains(t, sent, "MANAGED-ID")
	tokenURL := strings.TrimPrefix(tm[1], env.Providers.Config.BaseURL)
	anonymous(t, env, tokenURL).Status(200).Equals(agenda)
	anonymous(t, env, path+"?token=forged").Status(403)

	// Bob is an attendee, not the organizer: he may not attach files to
	// the meeting, even in his own copy.
	addAttachment(t, env, bob, cal+"bob/personal/meet-1.ics", "action=attachment-add", "x").Status(403).Contains("need-privileges")

	// Update: a new MANAGED-ID and URL; the old one is gone.
	const agenda2 = "<html><body><h1>Agenda</h1><p>Discuss attachment draft</p></body></html>\n"
	upd := addAttachment(t, env, alice, ev, "action=attachment-update&managed-id="+id, agenda2).Status(200)
	id2 := upd.Header.Get("Cal-Managed-ID")
	require.True(t, id2 != "" && id2 != id)
	assert.NotContains(t, upd.ReadBody(), id)
	req(t, env, alice, "GET", path, "").Status(410)
	path2 := "/remote.php/dav/attachments/alice/" + id2
	req(t, env, bob, "GET", path2, "").Status(200).Equals(agenda2)

	// Errors name their precondition.
	req(t, env, alice, "POST", ev+"?action=attachment-frob", "").Status(403).Contains("valid-action")
	req(t, env, alice, "POST", ev+"?action=attachment-update&managed-id="+id2+"&rid=M", "x").Status(409).Contains("valid-rid")
	req(t, env, alice, "POST", ev+"?action=attachment-remove&managed-id=nope", "").Status(409).Contains("valid-managed-id")
	req(t, env, alice, "POST", ev+"?action=attachment-add&rid=20991231T000000Z", "x").Status(409).Contains("valid-rid")
	req(t, env, alice, "POST", ev+"?action=attachment-add", "x", "If-Match", before).Status(412)

	// One instance: an override is created for it, carrying its own
	// attachment as well as the series'.
	inst := addAttachment(t, env, alice, ev, "action=attachment-add&rid=20261109T090000Z", "<p>this week</p>").Status(201)
	id3 := inst.Header.Get("Cal-Managed-ID")
	stored := unfold(req(t, env, alice, "GET", ev, "").Status(200).ReadBody())
	_, override, ok := strings.Cut(stored, "RECURRENCE-ID:20261109T090000Z\r\n")
	require.True(t, ok, stored)
	assert.Contains(t, override, "MANAGED-ID="+id3+";")
	assert.Contains(t, override, "MANAGED-ID="+id2+";")
	master, _, _ := strings.Cut(stored, "RECURRENCE-ID")
	assert.NotContains(t, master, id3)

	// Remove the series' attachment everywhere: 204, a new ETag, and the
	// file is gone once nothing names it.
	etag = req(t, env, alice, "GET", ev, "").Header.Get("ETag")
	rm := req(t, env, alice, "POST", ev+"?action=attachment-remove&managed-id="+id2, "").Status(204)
	assert.Equal(t, "", rm.Header.Get("Cal-Managed-ID"))
	assert.True(t, rm.Header.Get("ETag") != etag)
	req(t, env, alice, "GET", ev, "").NotContains(id2)
	req(t, env, alice, "GET", path2, "").Status(410)
	req(t, env, alice, "GET", "/remote.php/dav/attachments/alice/"+id3, "").Status(200)
	req(t, env, bob, "GET", cal+"bob/personal/meet-1.ics", "").NotContains(id2)
}

// TestManagedAttachmentsInPUT checks RFC 8607 section 3.7: a PUT may name
// the owner's own managed attachments, whose size Cog corrects, and an
// invitation's as received, but no one else's.
func TestManagedAttachmentsInPUT(t *testing.T) {
	env, alice, bob := schedEnv(t)
	ev := cal + "alice/personal/meet-1.ics"
	req(t, env, alice, "PUT", ev, meeting).Status(201)
	id := addAttachment(t, env, alice, ev, "action=attachment-add", agenda).Status(201).Header.Get("Cal-Managed-ID")
	url := env.Providers.Config.BaseURL + "/remote.php/dav/attachments/alice/" + id

	// Alice reuses it in another event, with a wrong size.
	other := strings.Replace(ics("VEVENT", "other", "Other"), "END:VEVENT", "ATTACH;MANAGED-ID="+id+";SIZE=1:"+url+"\r\nEND:VEVENT", 1)
	req(t, env, alice, "PUT", cal+"alice/personal/other.ics", other).Status(201)
	got := unfold(req(t, env, alice, "GET", cal+"alice/personal/other.ics", "").Status(200).ReadBody())
	assert.Contains(t, got, "MANAGED-ID="+id+";SIZE="+strconv.Itoa(len(agenda))+":"+url+"\r\n")

	// Bob's client writes back his copy, which carries alice's attachment.
	bobEv := cal + "bob/personal/meet-1.ics"
	copyOf := req(t, env, bob, "GET", bobEv, "").Status(200).ReadBody()
	req(t, env, bob, "PUT", bobEv, copyOf).Status(204)
	// But bob cannot claim alice's attachment for an event of his own.
	theft := strings.Replace(ics("VEVENT", "theft", "Mine"), "END:VEVENT", "ATTACH;MANAGED-ID="+id+":"+url+"\r\nEND:VEVENT", 1)
	req(t, env, bob, "PUT", cal+"bob/personal/theft.ics", theft).Status(403).Contains("valid-managed-id-parameter")

	// Removing the ATTACH from both of alice's events frees the file.
	req(t, env, alice, "PUT", cal+"alice/personal/other.ics", ics("VEVENT", "other", "Other")).Status(204)
	req(t, env, alice, "GET", strings.TrimPrefix(url, env.Providers.Config.BaseURL), "").Status(200)
	req(t, env, alice, "POST", ev+"?action=attachment-remove&managed-id="+id, "").Status(204)
	req(t, env, alice, "GET", strings.TrimPrefix(url, env.Providers.Config.BaseURL), "").Status(410)
}

// TestUnmanagedAttachmentsSurvive checks that a URI and inline data sent by
// a client come back as sent, and that export and import drop MANAGED-ID.
func TestUnmanagedAttachmentsSurvive(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)
	data := strings.Replace(ics("VEVENT", "att", "Att"), "END:VEVENT",
		"ATTACH;FMTTYPE=text/plain;VALUE=BINARY;ENCODING=BASE64:SGVsbG8=\r\n"+
			"ATTACH;VALUE=URI;X-ORIGIN=x:https://example.com/doc.pdf\r\n"+
			"ATTACH;FMTTYPE=text/plain;SIZE=abc:https://example.com/odd\r\nEND:VEVENT", 1)
	req(t, env, alice, "PUT", cal+"alice/personal/att.ics", data).Status(201)
	got := unfold(req(t, env, alice, "GET", cal+"alice/personal/att.ics", "").Status(200).ReadBody())
	assert.Contains(t, got, "ATTACH;VALUE=BINARY;ENCODING=BASE64;FMTTYPE=text/plain:SGVsbG8=\r\n")
	assert.Contains(t, got, "ATTACH;VALUE=URI;X-ORIGIN=x:https://example.com/doc.pdf\r\n")
	assert.Contains(t, got, "ATTACH;FMTTYPE=text/plain;SIZE=abc:https://example.com/odd\r\n")

	id := addAttachment(t, env, alice, cal+"alice/personal/att.ics", "action=attachment-add", agenda).Status(201).Header.Get("Cal-Managed-ID")
	c := personal(t, env, alice)
	var out bytes.Buffer
	require.NoError(t, env.Providers.DAV.Export(&out, "alice", c))
	assert.NotContains(t, out.String(), "MANAGED-ID")
	assert.Contains(t, unfold(out.String()), "/remote.php/dav/attachments/alice/"+id)
	_, err := env.Providers.DAV.Import("alice", c, strings.NewReader(strings.Replace(meeting, "ORGANIZER", "ATTACH;MANAGED-ID=foreign:https://elsewhere.example/a\r\nORGANIZER", 1)))
	require.NoError(t, err)
	req(t, env, alice, "GET", cal+"alice/personal/meet-1.ics", "").Status(200).NotContains("foreign")
}

// TestAttachmentQuota checks that managed attachments count toward the
// user's quota with their files, and that a POST over it fails as RFC 8607
// says.
func TestAttachmentQuota(t *testing.T) {
	env := cogtest.New(t, files.New(), calendar.New())
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	_, err = env.Providers.Mounts.Create(alice.ID, "/", vol.ID, "alice/files", false)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "USER", alice.ID, 100))
	ev := cal + "alice/personal/ev.ics"
	req(t, env, alice, "PUT", ev, ics("VEVENT", "ev", "Ev")).Status(201)

	sixty := strings.Repeat("x", 60)
	addAttachment(t, env, alice, ev, "action=attachment-add", sixty).Status(201)
	addAttachment(t, env, alice, ev, "action=attachment-add", sixty+"y").Status(403).Contains("quota-not-exceeded")
	req(t, env, alice, "GET", ev, "").Status(200).NotContains("SIZE=61")
}
