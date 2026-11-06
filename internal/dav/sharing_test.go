package dav_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

const csPropfind = `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop>
<D:resourcetype/><CS:invite/><CS:shared-url/><CS:allowed-sharing-modes/></D:prop></D:propfind>`

// collectionShares returns the dav:collection shares of c, by subject.
func collectionShares(t *testing.T, env *cogtest.Env, c *dav.Collection) map[string]uint32 {
	t.Helper()
	out := map[string]uint32{}
	for rec, err := range env.Providers.Shares.ListByResource(dav.ShareType, c.ID.String()) {
		require.NoError(t, err)
		_, dup := out[rec.SubjectID]
		require.False(t, dup, "two shares with "+rec.SubjectID)
		out[rec.SubjectID] = rec.Permissions
	}
	return out
}

// responseFor returns the part of a multistatus about href.
func responseFor(t *testing.T, body, href string) string {
	t.Helper()
	_, part, ok := strings.Cut(body, "<D:href>"+href+"</D:href>")
	require.True(t, ok, body)
	part, _, _ = strings.Cut(part, "</D:response>")
	return part
}

// TestCalendarServerSharing shares a calendar with CS:share and checks that
// it is the share the web panel makes, and the reverse.
func TestCalendarServerSharing(t *testing.T) {
	env, alice, bob := schedEnv(t)
	dave := env.CreateUser(t, "dave", "pass")
	erin := env.CreateUser(t, "erin", "pass")
	c := personal(t, env, alice)
	path := cal + "alice/personal/"

	assert.Contains(t, req(t, env, alice, "OPTIONS", path, "").Header.Get("DAV"), "calendarserver-sharing")
	// Not shared yet: shareable, but not shared-owner, and nobody invited.
	body := req(t, env, alice, "PROPFIND", path, csPropfind, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<CS:can-be-shared/>")
	assert.NotContains(t, body, "shared-owner")
	assert.Contains(t, body, "<CS:invite/>")

	// Alice shares by email with bob (read-write) and by principal with
	// dave (read only). Bob's address differs in case from his email.
	req(t, env, alice, "POST", path, `<?xml version="1.0" encoding="utf-8"?>
<CS:share xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
  <CS:set><D:href>mailto:b@example.com</D:href><CS:summary>Planning</CS:summary><CS:read-write/></CS:set>
  <CS:set><D:href>/remote.php/dav/principals/users/dave/</D:href><CS:read/></CS:set>
</CS:share>`, "Content-Type", "application/xml").Status(200)
	assert.Equal(t, map[string]uint32{bob.ID: plugin.PermRead | plugin.PermWrite, dave.ID: plugin.PermRead}, collectionShares(t, env, c))

	// The web panel's share of erin shows up beside them.
	share(t, env, alice, erin, c.ID, plugin.PermRead)
	body = req(t, env, alice, "PROPFIND", path, csPropfind, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<CS:shared-owner/>")
	assert.Contains(t, body, "<CS:user><D:href>mailto:B@Example.com</D:href><CS:common-name>bob</CS:common-name><CS:invite-accepted/><CS:access><CS:read-write/></CS:access></CS:user>")
	assert.Contains(t, body, "<CS:user><D:href>/remote.php/dav/principals/users/dave/</D:href><CS:common-name>dave</CS:common-name><CS:invite-accepted/><CS:access><CS:read/></CS:access></CS:user>")
	assert.Contains(t, body, "<D:href>/remote.php/dav/principals/users/erin/</D:href>")

	// Bob sees it in his home: shared, the owner's URL, alice as organizer.
	body = req(t, env, bob, "PROPFIND", cal+"bob/", csPropfind, "Depth", "1").Status(207).ReadBody()
	part := responseFor(t, body, path)
	assert.Contains(t, part, "<CS:shared/>")
	assert.Contains(t, part, "<CS:shared-url><D:href>"+path+"</D:href></CS:shared-url>")
	assert.Contains(t, part, "<CS:organizer><D:href>mailto:a@example.com</D:href>")
	assert.Contains(t, part, "<CS:access><CS:read-write/></CS:access>")
	assert.NotContains(t, part, "dave") // other sharees are not his to know
	req(t, env, bob, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Bob writes")).Status(201)

	// Downgrading to read leaves one share, without Write.
	share(t, env, alice, bob, c.ID, 0) // a duplicate, as an old client might leave
	req(t, env, alice, "POST", path, `<CS:share xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><CS:set><D:href>mailto:b@example.com</D:href><CS:read/></CS:set></CS:share>`).Status(200)
	assert.Equal(t, plugin.PermRead, collectionShares(t, env, c)[bob.ID])
	req(t, env, bob, "PUT", path+"ev2.ics", ics("VEVENT", "ev2", "No")).Status(403)

	// Dave declines: his share goes. Erin accepts: nothing changes, and the
	// answer says where the calendar is.
	reply := func(u *cogtest.User, answer string) *cogtest.Response {
		return req(t, env, u, "POST", cal+u.Username+"/", `<CS:invite-reply xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
<D:href>mailto:x@example.com</D:href><CS:`+answer+`/><CS:hosturl><D:href>`+path+`</D:href></CS:hosturl><CS:in-reply-to>x</CS:in-reply-to></CS:invite-reply>`)
	}
	reply(dave, "invite-declined").Status(204)
	req(t, env, dave, "GET", path+"ev.ics", "").Status(403)
	reply(erin, "invite-accepted").Status(200).Contains("<D:href>" + path + "</D:href></CS:shared-as>")
	reply(dave, "invite-accepted").Status(403) // no share left to answer

	// Remove bob; only the owner may change shares, and only Cog users can
	// be sharees.
	req(t, env, alice, "POST", path, `<CS:share xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><CS:remove><D:href>mailto:b@example.com</D:href></CS:remove></CS:share>`).Status(200)
	assert.Equal(t, map[string]uint32{erin.ID: plugin.PermRead}, collectionShares(t, env, c))
	req(t, env, erin, "POST", path, `<CS:share xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><CS:set><D:href>mailto:b@example.com</D:href><CS:read/></CS:set></CS:share>`).Status(403)
	req(t, env, alice, "POST", path, `<CS:share xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><CS:set><D:href>mailto:nobody@example.com</D:href><CS:read/></CS:set></CS:share>`).Status(403)
	assert.Equal(t, map[string]uint32{erin.ID: plugin.PermRead}, collectionShares(t, env, c))
}

// TestDeclineGroupShare: a member declines a calendar shared with their
// group; it stops reaching them and still reaches the others.
func TestDeclineGroupShare(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")
	c := personal(t, env, alice)
	req(t, env, alice, "PUT", cal+"alice/personal/ev.ics", ics("VEVENT", "ev", "Team")).Status(201)
	g, err := env.Providers.Groups.Create("team", nil, &alice.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, bob.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, carol.ID, group.User))
	_, err = env.Providers.Shares.Create(alice.ID, dav.ShareType, c.ID.String(), "GROUP", g.ID, plugin.PermRead)
	require.NoError(t, err)
	path := cal + "alice/personal/"
	req(t, env, bob, "GET", path+"ev.ics", "").Status(200)

	req(t, env, bob, "POST", cal+"bob/", `<CS:invite-reply xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/">
<D:href>mailto:x@example.com</D:href><CS:invite-declined/><CS:hosturl><D:href>`+path+`</D:href></CS:hosturl><CS:in-reply-to>x</CS:in-reply-to></CS:invite-reply>`).Status(204)
	req(t, env, bob, "GET", path+"ev.ics", "").Status(403)
	req(t, env, carol, "GET", path+"ev.ics", "").Status(200)
}
