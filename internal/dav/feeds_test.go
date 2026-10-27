package dav_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func davUser(t *testing.T, env *cogtest.Env, u *cogtest.User) *dav.User {
	t.Helper()
	du, err := env.Providers.DAV.Users.ByUsername(u.Username)
	require.NoError(t, err)
	return du
}

// feedPath is the path of a link's feed.
func feedPath(env *cogtest.Env, l *dav.Link) string {
	return strings.TrimPrefix(env.Providers.DAV.FeedURL(l.Token), env.Providers.Config.BaseURL)
}

func anonymousWith(t *testing.T, env *cogtest.Env, method, path string, headers ...string) *cogtest.Response {
	t.Helper()
	rq := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		rq.Header.Set(headers[i], headers[i+1])
	}
	return env.Do(t, rq)
}

func TestICSFeed(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	req(t, env, alice, "PUT", cal+"alice/personal/ev.ics", ics("VEVENT", "ev-1", "Dentist")).Status(201)
	c := personal(t, env, alice)
	srv := env.Providers.DAV

	full, err := srv.RenewFeed(c, false)
	require.NoError(t, err)
	busy, err := srv.RenewFeed(c, true)
	require.NoError(t, err)

	// The feed needs no account; its secret is the credential.
	got := anonymous(t, env, feedPath(env, full)).Status(200)
	body := got.ReadBody()
	assert.Contains(t, got.Header.Get("Content-Type"), "text/calendar")
	assert.Contains(t, body, "X-WR-CALNAME:Personal\r\n")
	assert.Contains(t, body, "SUMMARY:Dentist\r\n")
	etag, modified := got.Header.Get("ETag"), got.Header.Get("Last-Modified")
	require.True(t, etag != "" && modified != "")
	// Generated through the same path as export.
	var exported strings.Builder
	require.NoError(t, srv.Export(&exported, alice.Username, c))
	assert.Equal(t, exported.String(), body)

	// Polling an unchanged calendar costs a 304.
	anonymousWith(t, env, "GET", feedPath(env, full), "If-None-Match", etag).Status(304)
	anonymousWith(t, env, "GET", feedPath(env, full), "If-Modified-Since", modified).Status(304)
	anonymousWith(t, env, "HEAD", feedPath(env, full)).Status(200)
	anonymousWith(t, env, "POST", feedPath(env, full)).Status(405)

	// A change makes a new ETag and a later Last-Modified.
	env.Clock.Advance(time.Hour)
	req(t, env, alice, "PUT", cal+"alice/personal/ev2.ics", ics("VEVENT", "ev-2", "Lunch")).Status(201)
	got = anonymousWith(t, env, "GET", feedPath(env, full), "If-None-Match", etag).Status(200)
	assert.True(t, got.Header.Get("ETag") != etag)
	anonymousWith(t, env, "GET", feedPath(env, full), "If-Modified-Since", modified).Status(200)
	assert.Contains(t, got.ReadBody(), "SUMMARY:Lunch")
	// Renaming the calendar changes the feed too (X-WR-CALNAME).
	etag = got.Header.Get("ETag")
	c.DisplayName = "Mine"
	require.NoError(t, srv.Collections.Update(c))
	got = anonymousWith(t, env, "GET", feedPath(env, full), "If-None-Match", etag).Status(200)
	assert.Contains(t, got.ReadBody(), "X-WR-CALNAME:Mine")

	// The free/busy feed shows when, not what.
	body = anonymous(t, env, feedPath(env, busy)).Status(200).ReadBody()
	assert.Contains(t, body, "DTSTART:20261025T090000Z")
	assert.NotContains(t, body, "Dentist")
	assert.NotContains(t, body, "Lunch")

	// A new address revokes the old one at once.
	renewed, err := srv.RenewFeed(c, false)
	require.NoError(t, err)
	anonymous(t, env, feedPath(env, full)).Status(404)
	anonymous(t, env, feedPath(env, renewed)).Status(200)
	feeds, err := srv.FeedsOf(alice.ID)
	require.NoError(t, err)
	assert.Equal(t, renewed.Token, feeds[c.ID].Full.Token)
	assert.Equal(t, busy.Token, feeds[c.ID].Busy.Token)

	// Revoking one kind leaves the other.
	require.NoError(t, srv.RevokeFeed(c, false))
	anonymous(t, env, feedPath(env, renewed)).Status(404)
	anonymous(t, env, feedPath(env, busy)).Status(200)
	require.NoError(t, srv.RevokeFeed(c, true))
	anonymous(t, env, feedPath(env, busy)).Status(404)

	// A link someone else made to alice's calendar grants nothing, like
	// any share not made by the owner; unknown tokens are not found.
	foreign, err := srv.Links.CreateLink(bob.ID, c.ID, plugin.PermRead)
	require.NoError(t, err)
	anonymous(t, env, feedPath(env, foreign)).Status(404)
	anonymous(t, env, "/remote.php/dav/feeds/nope.ics").Status(404)

	// A public link made in the share panel is the same feed, and deleting
	// it there revokes it.
	_, err = env.Providers.Shares.CreatePublicLink(alice.ID, dav.ShareType, c.ID.String(), plugin.PermRead, nil)
	require.NoError(t, err)
	feeds, err = srv.FeedsOf(alice.ID)
	require.NoError(t, err)
	panel := feeds[c.ID].Full
	require.True(t, panel != nil)
	anonymous(t, env, feedPath(env, panel)).Status(200).Contains("SUMMARY:Dentist")
	require.NoError(t, srv.Links.DeleteLink(panel.ID))
	anonymous(t, env, feedPath(env, panel)).Status(404)

	// A deleted calendar's feed is gone with it.
	again, err := srv.RenewFeed(c, false)
	require.NoError(t, err)
	require.NoError(t, srv.DeleteCollection(alice.Username, c))
	anonymous(t, env, feedPath(env, again)).Status(404)
}

// TestICSFeedAttachments checks that a feed's subscribers fetch the owner's
// managed attachments with a token bound to the feed: revoking the feed
// revokes it.
func TestICSFeedAttachments(t *testing.T) {
	env, alice, _ := schedEnv(t)
	ev := cal + "alice/personal/meet-1.ics"
	req(t, env, alice, "PUT", ev, meeting).Status(201)
	add := addAttachment(t, env, alice, ev, "action=attachment-add", agenda).Status(201)
	id := add.Header.Get("Cal-Managed-ID")
	c := personal(t, env, alice)
	srv := env.Providers.DAV

	full, err := srv.RenewFeed(c, false)
	require.NoError(t, err)
	body := unfold(anonymous(t, env, feedPath(env, full)).Status(200).ReadBody())
	assert.NotContains(t, body, "MANAGED-ID")
	m := regexp.MustCompile(`ATTACH;[^:]*:(\S+\?feed=\S+)\r\n`).FindStringSubmatch(body)
	require.True(t, m != nil, body)
	path := strings.TrimPrefix(m[1], env.Providers.Config.BaseURL)
	assert.True(t, strings.HasPrefix(path, "/remote.php/dav/attachments/alice/"+id+"?feed="))
	anonymous(t, env, path).Status(200).Equals(agenda)
	// A token for another attachment, or a made-up one, opens nothing.
	anonymous(t, env, "/remote.php/dav/attachments/alice/"+id+"?feed=forged").Status(403)

	// The free/busy feed carries no attachments.
	busy, err := srv.RenewFeed(c, true)
	require.NoError(t, err)
	assert.NotContains(t, anonymous(t, env, feedPath(env, busy)).Status(200).ReadBody(), "ATTACH")

	require.NoError(t, srv.RevokeFeed(c, false))
	anonymous(t, env, path).Status(403)
}

// feedServer serves an iCalendar feed with an ETag, like most feed hosts,
// counting requests and the ones answered 304.
type feedServer struct {
	*httptest.Server
	mu          sync.Mutex
	body        string
	status      int
	requests    int
	notModified int
}

func newFeedServer(t *testing.T, body string) *feedServer {
	f := &feedServer{body: body, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests++
		if f.status != http.StatusOK {
			http.Error(w, "nope", f.status)
			return
		}
		etag := `"` + strings.Repeat("x", len(f.body)%7) + string(rune('a'+len(f.body)%26)) + `"`
		if r.Header.Get("If-None-Match") == etag {
			f.notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/calendar")
		w.Write([]byte(f.body))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *feedServer) set(body string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body, f.status = body, status
}

func (f *feedServer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.notModified
}

const feedV1 = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//School//EN\r\nX-WR-CALNAME:School\r\nREFRESH-INTERVAL;VALUE=DURATION:PT6H\r\n" +
	"BEGIN:VEVENT\r\nUID:term-1@school\r\nDTSTAMP:20260101T000000Z\r\nDTSTART;VALUE=DATE:20261019\r\nDTEND;VALUE=DATE:20261024\r\nSUMMARY:Half term\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:sports day/2026\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261105T090000Z\r\nDTEND:20261105T150000Z\r\nSUMMARY:Sports day\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261201T180000Z\r\nSUMMARY:Concert (no UID)\r\nEND:VEVENT\r\n" +
	"BEGIN:VTODO\r\nUID:todo@school\r\nSUMMARY:Not an event\r\nEND:VTODO\r\n" +
	"END:VCALENDAR\r\n"

// TestSubscription subscribes to a feed served by a test server and follows
// it through polls: conditional fetches, changes and removals reaching DAV
// sync, read-only enforcement, and failures recorded.
func TestSubscription(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	srv := env.Providers.DAV
	feed := newFeedServer(t, feedV1)
	srv.Fetch = feed.Client() // the test server is on loopback, which safehttp refuses
	ctx := context.Background()

	c, res, err := srv.Subscribe(ctx, davUser(t, env, alice), strings.Replace(feed.URL, "http://", "webcal://", 1), "School", "#00ff00")
	// webcal:// is https://, which the test server does not speak.
	require.True(t, err != nil)
	assert.True(t, c == nil && res == nil)
	cols, err := srv.Collections.OfKind(alice.ID, dav.KindSubscription)
	require.NoError(t, err)
	assert.Equal(t, 0, len(cols))

	c, res, err = srv.Subscribe(ctx, davUser(t, env, alice), feed.URL+"/school.ics", "School", "#00ff00")
	require.NoError(t, err)
	assert.Equal(t, dav.PollResult{Added: 3, Skipped: 1}, *res)
	assert.Equal(t, dav.KindSubscription, c.Kind)
	path := cal + "alice/" + c.Name + "/"

	// The collection is a calendar to clients, read-only to everyone, and
	// says where it comes from.
	props := req(t, env, alice, "PROPFIND", path, "", "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, props, "<CS:source><D:href>"+feed.URL+"/school.ics</D:href></CS:source>")
	assert.Contains(t, props, "<C:calendar/>")
	assert.NotContains(t, props, "subscribed")
	privs := props[strings.Index(props, "<D:current-user-privilege-set>"):]
	privs = privs[:strings.Index(privs, "</D:current-user-privilege-set>")]
	assert.Contains(t, privs, "<D:read/>")
	assert.Contains(t, privs, "<D:write-properties/>")
	for _, p := range []string{"<D:all/>", "<D:write/>", "<D:write-content/>", "<D:bind/>", "<D:unbind/>"} {
		assert.NotContains(t, privs, p)
	}

	body, token := syncReport(t, env, alice, path, "")
	assert.Contains(t, body, "term-1@school.ics")
	// A UID unsafe in a path is named by its hash; an event without one
	// gets a UID from its text. Both are the same on every fetch.
	uriSports := regexp.MustCompile(`/([0-9a-f]{32}\.ics)<`).FindAllStringSubmatch(body, -1)
	require.Equal(t, 1, len(uriSports))
	assert.True(t, regexp.MustCompile(`/cog-feed-[0-9a-f]{32}\.ics<`).MatchString(body), body)
	ev := req(t, env, alice, "GET", path+"term-1@school.ics", "").Status(200).ReadBody()
	assert.Contains(t, ev, "SUMMARY:Half term")

	// Nobody writes to it, the owner included.
	req(t, env, alice, "PUT", path+"term-1@school.ics", ics("VEVENT", "term-1@school", "Mine now")).Status(403)
	req(t, env, alice, "PUT", path+"new.ics", ics("VEVENT", "new", "New")).Status(403)
	req(t, env, alice, "DELETE", path+"term-1@school.ics", "").Status(403)
	req(t, env, alice, "POST", path+"term-1@school.ics?action=attachment-add", "x").Status(403)
	_, err = srv.Import(alice.Username, c, strings.NewReader(ics("VEVENT", "imp", "Imported")))
	assert.True(t, errors.Is(err, dav.ErrReadOnly), err)
	// It may be renamed.
	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Kids</D:displayname></D:prop></D:set></D:propertyupdate>`).Status(207).Contains("200 OK")
	// The feed asked to be fetched every six hours.
	sub, err := srv.Collections.Subscription(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 6*time.Hour, sub.Refresh)
	assert.Equal(t, env.Now().Add(6*time.Hour).Unix(), sub.NextPoll.Unix())

	// Not due yet: nothing is fetched.
	require.NoError(t, srv.PollDue(ctx))
	n, _ := feed.counts()
	assert.Equal(t, 1, n) // the https attempt never reached the handler
	// Due, unchanged: a conditional fetch answered 304, and nothing moves.
	env.Clock.Advance(7 * time.Hour)
	require.NoError(t, srv.PollDue(ctx))
	n, nm := feed.counts()
	assert.Equal(t, 2, n)
	assert.Equal(t, 1, nm)
	body, next := syncReport(t, env, alice, path, token)
	assert.Equal(t, token, next)
	// A full fetch of the same text rewrites nothing either.
	feed.set(feedV1+"\r\n", http.StatusOK)
	res, err = srv.Refresh(ctx, alice.Username, c)
	require.NoError(t, err)
	assert.Equal(t, dav.PollResult{Unchanged: 3, Skipped: 1}, *res)
	_, next = syncReport(t, env, alice, path, token)
	assert.Equal(t, token, next)

	// The feed changes: one event edited, one gone, one new. Clients see
	// exactly that, the removal as a tombstone.
	v2 := strings.Replace(feedV1, "SUMMARY:Half term", "SUMMARY:Half term (moved)", 1)
	v2 = strings.Replace(v2, "BEGIN:VEVENT\r\nUID:sports day/2026\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261105T090000Z\r\nDTEND:20261105T150000Z\r\nSUMMARY:Sports day\r\nEND:VEVENT\r\n",
		"BEGIN:VEVENT\r\nUID:fair@school\r\nDTSTAMP:20260101T000000Z\r\nDTSTART:20261110T090000Z\r\nSUMMARY:Fair\r\nEND:VEVENT\r\n", 1)
	feed.set(v2, http.StatusOK)
	res, err = srv.Refresh(ctx, alice.Username, c)
	require.NoError(t, err)
	assert.Equal(t, dav.PollResult{Added: 1, Updated: 1, Removed: 1, Unchanged: 1, Skipped: 1}, *res)
	body, _ = syncReport(t, env, alice, path, token)
	assert.Contains(t, body, "term-1@school.ics")
	assert.Contains(t, body, "fair@school.ics")
	assert.Contains(t, body, uriSports[0][1]+"</D:href><D:status>HTTP/1.1 404 Not Found")
	req(t, env, alice, "GET", path+"term-1@school.ics", "").Status(200).Contains("Half term (moved)")

	// A failing feed is recorded and retried later; its events stay.
	feed.set(v2, http.StatusInternalServerError)
	_, err = srv.Refresh(ctx, alice.Username, c)
	var fe *dav.FeedError
	require.True(t, errors.As(err, &fe), err)
	sub, err = srv.Collections.Subscription(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, sub.Failures)
	assert.Contains(t, sub.LastError, "500")
	assert.Equal(t, env.Now().Add(6*time.Hour).Unix(), sub.NextPoll.Unix())
	req(t, env, alice, "GET", path+"term-1@school.ics", "").Status(200)
	// PollDue records it too, and reports no error of Cog's own.
	env.Clock.Advance(7 * time.Hour)
	require.NoError(t, srv.PollDue(ctx))
	sub, err = srv.Collections.Subscription(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, sub.Failures)
	assert.Equal(t, env.Now().Add(12*time.Hour).Unix(), sub.NextPoll.Unix())
	// Not a calendar at all: refused, nothing deleted.
	feed.set("<html>moved</html>", http.StatusOK)
	_, err = srv.Refresh(ctx, alice.Username, c)
	require.True(t, errors.As(err, &fe), err)
	req(t, env, alice, "GET", path+"term-1@school.ics", "").Status(200)
	// It recovers.
	feed.set(v2, http.StatusOK)
	_, err = srv.Refresh(ctx, alice.Username, c)
	require.NoError(t, err)
	sub, err = srv.Collections.Subscription(c.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, sub.Failures)
	assert.Equal(t, "", sub.LastError)

	// Invitations never land in it.
	assert.NotContains(t, req(t, env, alice, "PROPFIND", cal+"alice/inbox/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:schedule-default-calendar-URL/></D:prop></D:propfind>`, "Depth", "0").ReadBody(), c.Name)

	// Unsubscribing deletes the calendar and its state.
	req(t, env, alice, "DELETE", path, "").Status(204)
	_, err = srv.Collections.Subscription(c.ID)
	assert.True(t, errors.Is(err, dav.ErrNotFound), err)
}

// TestSubscriptionLimits checks what Cog refuses to fetch: private
// addresses (through safehttp), URLs that are not feeds', and feeds too
// large.
func TestSubscriptionLimits(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	srv := env.Providers.DAV
	feed := newFeedServer(t, feedV1)
	ctx := context.Background()
	u := davUser(t, env, alice)

	// The default client dials only public addresses: a feed on loopback
	// (or one redirecting there) is refused, and no calendar is made.
	_, _, err := srv.Subscribe(ctx, u, feed.URL+"/school.ics", "Internal", "")
	var fe *dav.FeedError
	require.True(t, errors.As(err, &fe), err)
	assert.Contains(t, err.Error(), "blocked")
	assert.NotContains(t, err.Error(), feed.URL) // feed URLs may hold secrets
	n, _ := feed.counts()
	assert.Equal(t, 0, n)
	for _, bad := range []string{"ftp://example.com/x.ics", "file:///etc/passwd", "example.com/x.ics", "https://user:pw@example.com/x.ics"} {
		_, _, err := srv.Subscribe(ctx, u, bad, "Bad", "")
		require.True(t, errors.As(err, &fe), bad, err)
	}
	cols, err := srv.Collections.OfKind(alice.ID, dav.KindSubscription)
	require.NoError(t, err)
	assert.Equal(t, 0, len(cols))

	// Too large.
	srv.Fetch = feed.Client()
	feed.set("BEGIN:VCALENDAR\r\nX-PAD:"+strings.Repeat("x", 10<<20)+"\r\nEND:VCALENDAR\r\n", http.StatusOK)
	_, _, err = srv.Subscribe(ctx, u, feed.URL, "Huge", "")
	require.True(t, errors.As(err, &fe), err)
	assert.Contains(t, err.Error(), "larger than")
	// Nothing but tasks: storing it would delete every event, so it fails.
	feed.set("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VTODO\r\nUID:t\r\nEND:VTODO\r\nEND:VCALENDAR\r\n", http.StatusOK)
	_, _, err = srv.Subscribe(ctx, u, feed.URL, "Tasks", "")
	require.True(t, errors.As(err, &fe), err)
	// An empty calendar is a feed with no events.
	feed.set("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n", http.StatusOK)
	_, res, err := srv.Subscribe(ctx, u, feed.URL, "Empty", "")
	require.NoError(t, err)
	assert.Equal(t, dav.PollResult{}, *res)
}

// TestSubscriptionFixtures subscribes to real feeds from the corpus and
// checks every event arrives, and that fetching them again changes nothing.
func TestSubscriptionFixtures(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	srv := env.Providers.DAV
	for _, file := range []string{
		"ical4j/google_aus_holidays.ics", // Google Calendar's public holidays
		"ical4j/lastfm.ics",              // Last.fm's event feed
		"ical4j/Australian_TV_Melbourne.ics",
		"icaljs/google_birthday.ics", // Google's contacts' birthdays
	} {
		data, err := os.ReadFile("../../testdata/dav/ics/" + file)
		require.NoError(t, err)
		feed := newFeedServer(t, string(data))
		srv.Fetch = feed.Client()
		uids := map[string]bool{}
		noUID := 0
		for _, block := range strings.Split(string(data), "BEGIN:VEVENT")[1:] {
			if m := regexp.MustCompile(`(?m)^UID:(.*?)\r?$`).FindStringSubmatch(block); m != nil {
				uids[m[1]] = true
			} else {
				noUID++
			}
		}
		c, res, err := srv.Subscribe(context.Background(), davUser(t, env, alice), feed.URL, file, "")
		require.NoError(t, err, file)
		assert.Equal(t, len(uids)+noUID, res.Added, file)
		assert.Equal(t, 0, res.Skipped, file)
		if file == "ical4j/google_aus_holidays.ics" {
			// Floating times read in the zone the feed names.
			assert.Equal(t, "US/Pacific", c.Timezone)
		}
		_, token := syncReport(t, env, alice, cal+"alice/"+c.Name+"/", "")
		feed.set(string(data)+"\r\n", http.StatusOK)
		res, err = srv.Refresh(context.Background(), alice.Username, c)
		require.NoError(t, err, file)
		assert.Equal(t, len(uids)+noUID, res.Unchanged, file)
		_, next := syncReport(t, env, alice, cal+"alice/"+c.Name+"/", token)
		assert.Equal(t, token, next, file)
	}
}
