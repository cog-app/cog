package dav_test

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/webpush"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

// pushService is a Web Push service and the user agents behind it: each
// endpoint has its own keys, and what arrives is decrypted as a client
// would.
type pushService struct {
	*httptest.Server
	t     *testing.T
	vapid string // the server's public key, which every request must carry

	mu      sync.Mutex
	keys    map[string]*ecdh.PrivateKey // by endpoint name
	auth    map[string][]byte
	status  map[string]int      // answer for an endpoint; 201 when unset
	got     map[string][]string // decrypted messages by endpoint
	headers map[string][]http.Header
}

// newPushService starts a push service over TLS and points the server's
// pushes at it. safehttp refuses loopback, so the push client is allowed
// this test server's address and nothing more.
func newPushService(t *testing.T, env *cogtest.Env) *pushService {
	ps := &pushService{t: t, vapid: env.Providers.DAV.VAPID.PublicKey(),
		keys: map[string]*ecdh.PrivateKey{}, auth: map[string][]byte{}, status: map[string]int{},
		got: map[string][]string{}, headers: map[string][]http.Header{}}
	ps.Server = httptest.NewTLSServer(http.HandlerFunc(ps.serve))
	t.Cleanup(ps.Close)
	addr := netip.MustParseAddrPort(strings.TrimPrefix(ps.URL, "https://"))
	env.Providers.Pusher.Sender.Client = safehttp.ClientWith(10*time.Second, safehttp.Options{
		Allow: []netip.Prefix{netip.PrefixFrom(addr.Addr(), addr.Addr().BitLen())},
		TLS:   ps.Client().Transport.(*http.Transport).TLSClientConfig,
	})
	return ps
}

func (ps *pushService) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") || !strings.HasSuffix(r.Header.Get("Authorization"), ", k="+ps.vapid) {
		http.Error(w, "no VAPID", http.StatusUnauthorized)
		return
	}
	if st, ok := ps.status[name]; ok && st != http.StatusCreated {
		http.Error(w, "refused", st)
		return
	}
	ua, ok := ps.keys[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	plain, err := webpush.Decrypt(body, ua, ps.auth[name])
	if err != nil {
		ps.t.Errorf("push to %s: %v", name, err)
		http.Error(w, err.Error(), 400)
		return
	}
	ps.got[name] = append(ps.got[name], string(plain))
	ps.headers[name] = append(ps.headers[name], r.Header.Clone())
	w.WriteHeader(http.StatusCreated)
}

// endpoint makes a subscription at the push service, as a client does
// before registering it.
func (ps *pushService) endpoint(name string) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if _, ok := ps.keys[name]; !ok {
		ua, err := ecdh.P256().GenerateKey(nil)
		require.NoError(ps.t, err)
		ps.keys[name] = ua
		ps.auth[name] = []byte(name + "-auth-secret-16b")[:16]
	}
	return ps.URL + "/" + name
}

func (ps *pushService) setStatus(name string, code int) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.status[name] = code
}

// take returns and forgets what arrived for an endpoint.
func (ps *pushService) take(name string) []string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	out := ps.got[name]
	delete(ps.got, name)
	return out
}

const (
	contentTrigger  = `<P:content-update><D:depth>1</D:depth></P:content-update>`
	propertyTrigger = `<P:property-update><D:depth>0</D:depth></P:property-update>`
)

// registration is a push-register body for an endpoint of ps.
func (ps *pushService) registration(name, trigger, extra string) string {
	endpoint := ps.endpoint(name)
	ps.mu.Lock()
	pub := base64.RawURLEncoding.EncodeToString(ps.keys[name].PublicKey().Bytes())
	secret := base64.RawURLEncoding.EncodeToString(ps.auth[name])
	ps.mu.Unlock()
	return `<?xml version="1.0" encoding="utf-8"?>
<P:push-register xmlns:D="DAV:" xmlns:P="https://bitfire.at/webdav-push">
  <P:subscription><P:web-push-subscription>
    <P:push-resource>` + endpoint + `</P:push-resource>
    <P:content-encoding>aes128gcm</P:content-encoding>
    <P:subscription-public-key type="p256dh">` + pub + `</P:subscription-public-key>
    <P:auth-secret>` + secret + `</P:auth-secret>
  </P:web-push-subscription></P:subscription>
  <P:trigger>` + trigger + `</P:trigger>` + extra + `
</P:push-register>`
}

// register registers endpoint name on path and returns its registration
// URL.
func (ps *pushService) register(t *testing.T, env *cogtest.Env, u *cogtest.User, path, name, trigger string) string {
	t.Helper()
	got := req(t, env, u, "POST", path, ps.registration(name, trigger, ""), "Content-Type", "application/xml").Status(204)
	loc := got.Header.Get("Location")
	require.True(t, strings.HasPrefix(loc, env.Providers.Config.BaseURL+"/remote.php/dav/push/"), loc)
	return loc
}

// round delivers what is owed now, as the deliverer's loop does after its
// debounce.
func round(t *testing.T, env *cogtest.Env) {
	t.Helper()
	require.NoError(t, env.Providers.Pusher.Round(context.Background()))
}

var topicRe = regexp.MustCompile(`<topic xmlns="https://bitfire.at/webdav-push">([^<]+)</topic>`)

const pushPropfind = `<D:propfind xmlns:D="DAV:" xmlns:P="https://bitfire.at/webdav-push"><D:prop><P:transports/><P:topic/><P:supported-triggers/></D:prop></D:propfind>`

// topicOf reads a resource's push topic.
func topicOf(t *testing.T, env *cogtest.Env, u *cogtest.User, path string) string {
	t.Helper()
	m := topicRe.FindStringSubmatch(req(t, env, u, "PROPFIND", path, pushPropfind, "Depth", "0").Status(207).ReadBody())
	require.True(t, m != nil, "no topic on "+path)
	return m[1]
}

// contentPush is the message a content update sends.
func contentPush(topic string) string {
	return `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<P:push-message xmlns:D="DAV:" xmlns:P="https://bitfire.at/webdav-push"><P:topic>` + topic + `</P:topic><P:content-update/></P:push-message>`
}

func TestPushDiscovery(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	srv := env.Providers.DAV
	c := personal(t, env, alice)

	assert.Contains(t, req(t, env, alice, "OPTIONS", cal+"alice/personal/", "").Status(200).Header.Get("DAV"), "webdav-push")

	body := req(t, env, alice, "PROPFIND", cal+"alice/personal/", pushPropfind, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, `<transports xmlns="https://bitfire.at/webdav-push"><web-push xmlns="https://bitfire.at/webdav-push">`+
		`<vapid-public-key xmlns="https://bitfire.at/webdav-push" type="p256ecdsa">`+srv.VAPID.PublicKey()+`</vapid-public-key>`)
	assert.Contains(t, body, `<content-update xmlns="https://bitfire.at/webdav-push"><D:depth>1</D:depth></content-update>`)
	assert.Contains(t, body, `<property-update xmlns="https://bitfire.at/webdav-push"><D:depth>0</D:depth></property-update>`)
	topic := topicOf(t, env, alice, cal+"alice/personal/")
	assert.Equal(t, 22, len(topic))
	// Stable, and different for each resource.
	assert.Equal(t, topic, topicOf(t, env, alice, cal+"alice/personal/"))
	book := topicOf(t, env, alice, "/remote.php/dav/addressbooks/alice/contacts/")
	inbox := topicOf(t, env, alice, cal+"alice/inbox/")
	home := topicOf(t, env, alice, cal+"alice/")
	assert.Equal(t, 4, len(map[string]bool{topic: true, book: true, inbox: true, home: true}))

	// Inboxes and homes have content updates only.
	body = req(t, env, alice, "PROPFIND", cal+"alice/inbox/", pushPropfind, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "content-update")
	assert.NotContains(t, body, "property-update")
	body = req(t, env, alice, "PROPFIND", cal+"alice/", pushPropfind, "Depth", "0").Status(207).ReadBody()
	assert.NotContains(t, body, "property-update")

	// Only when asked for by name.
	assert.NotContains(t, req(t, env, alice, "PROPFIND", cal+"alice/personal/", "", "Depth", "0").Status(207).ReadBody(), "webdav-push")

	// A free/busy-only sharee cannot subscribe, so is not offered it; a
	// reader sees the owner's topic.
	rec, err := env.Providers.Shares.Create(alice.ID, dav.ShareType, c.ID.String(), "USER", bob.ID, plugin.PermFreeBusy)
	require.NoError(t, err)
	body = req(t, env, bob, "PROPFIND", cal+"alice/personal/", pushPropfind, "Depth", "0").Status(207).ReadBody()
	assert.False(t, topicRe.MatchString(body), body)
	require.NoError(t, env.Providers.Shares.Update(rec.ID, new(uint32(plugin.PermRead)), nil))
	assert.Equal(t, topic, topicOf(t, env, bob, cal+"alice/personal/"))
}

func TestPushRegistration(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	ps := newPushService(t, env)
	c := personal(t, env, alice)
	path := cal + "alice/personal/"
	now := env.Clock.Now()

	// Without a requested expiry, a week.
	got := req(t, env, alice, "POST", path, ps.registration("a", contentTrigger, ""), "Content-Type", "application/xml").Status(204)
	loc := got.Header.Get("Location")
	assert.Equal(t, now.Add(7*24*time.Hour).Format(http.TimeFormat), got.Header.Get("Expires"))
	// The same endpoint again updates that registration: its URL stays.
	in2 := now.Add(48 * time.Hour).Format(http.TimeFormat)
	got = req(t, env, alice, "POST", path, ps.registration("a", contentTrigger, "<P:expires>"+in2+"</P:expires>")).Status(204)
	assert.Equal(t, loc, got.Header.Get("Location"))
	assert.Equal(t, in2, got.Header.Get("Expires"))
	// Too far is cut to a week, too near raised to an hour.
	got = req(t, env, alice, "POST", path, ps.registration("a", contentTrigger, "<P:expires>"+now.Add(90*24*time.Hour).Format(http.TimeFormat)+"</P:expires>")).Status(204)
	assert.Equal(t, now.Add(7*24*time.Hour).Format(http.TimeFormat), got.Header.Get("Expires"))
	got = req(t, env, alice, "POST", path, ps.registration("a", contentTrigger, "<P:expires>"+now.Add(-time.Hour).Format(http.TimeFormat)+"</P:expires>")).Status(204)
	assert.Equal(t, now.Add(time.Hour).Format(http.TimeFormat), got.Header.Get("Expires"))
	req(t, env, alice, "POST", path, ps.registration("a", contentTrigger, "<P:expires>tomorrow</P:expires>")).Status(400)
	subs, err := env.Providers.Push.OfUser(push.WebDAV, alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(subs))
	assert.Equal(t, "collection/"+c.ID.String(), subs[0].Resource)

	// Refused subscriptions.
	plain := strings.Replace(ps.registration("b", contentTrigger, ""), "<P:push-resource>https://", "<P:push-resource>http://", 1)
	req(t, env, alice, "POST", path, plain).Status(403).Contains("invalid-subscription")
	noKeys := regexp.MustCompile(`(?s)<P:subscription-public-key.*</P:auth-secret>`).ReplaceAllString(ps.registration("b", contentTrigger, ""), "")
	req(t, env, alice, "POST", path, noKeys).Status(403).Contains("invalid-subscription")
	badKey := regexp.MustCompile(`type="p256dh">[^<]+<`).ReplaceAllString(ps.registration("b", contentTrigger, ""), `type="p256dh">AAAA<`)
	req(t, env, alice, "POST", path, badKey).Status(403).Contains("invalid-subscription")
	req(t, env, alice, "POST", path, strings.Replace(ps.registration("b", contentTrigger, ""), "aes128gcm", "aesgcm", 1)).Status(403).Contains("invalid-subscription")
	req(t, env, alice, "POST", path, ps.registration("b", "", "")).Status(403).Contains("no-supported-trigger")
	// An inbox has no property updates: that trigger alone is ignored,
	// which leaves none.
	req(t, env, alice, "POST", cal+"alice/inbox/", ps.registration("b", propertyTrigger, "")).Status(403).Contains("no-supported-trigger")
	req(t, env, alice, "POST", path, "<x/>").Status(400)

	// Bob cannot subscribe to what he cannot read, nor take over Alice's
	// endpoint once he can.
	req(t, env, bob, "POST", path, ps.registration("bob", contentTrigger, "")).Status(403)
	_, err = env.Providers.Shares.Create(alice.ID, dav.ShareType, c.ID.String(), "USER", bob.ID, plugin.PermRead)
	require.NoError(t, err)
	req(t, env, bob, "POST", path, ps.registration("a", contentTrigger, "")).Status(403).Contains("invalid-subscription")
	bobLoc := ps.register(t, env, bob, path, "bob", contentTrigger)
	// Nor touch her registration.
	req(t, env, bob, "DELETE", strings.TrimPrefix(loc, env.Providers.Config.BaseURL), "").Status(404)

	// Unregistering.
	req(t, env, alice, "DELETE", strings.TrimPrefix(loc, env.Providers.Config.BaseURL), "").Status(204)
	req(t, env, alice, "DELETE", strings.TrimPrefix(loc, env.Providers.Config.BaseURL), "").Status(404)
	req(t, env, alice, "GET", strings.TrimPrefix(loc, env.Providers.Config.BaseURL), "").Status(405)

	// Expiry: an expired registration gets nothing, cannot be removed, and
	// is pruned.
	req(t, env, bob, "POST", path, ps.registration("bob", contentTrigger, "<P:expires>"+now.Add(time.Hour).Format(http.TimeFormat)+"</P:expires>")).Status(204)
	env.Clock.Advance(2 * time.Hour)
	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Lunch")).Status(201)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("bob")))
	req(t, env, bob, "DELETE", strings.TrimPrefix(bobLoc, env.Providers.Config.BaseURL), "").Status(404)
	require.NoError(t, env.Providers.Pusher.Prune(context.Background()))
	var n int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT count(*) FROM push_subscriptions`).Scan(&n))
	assert.Equal(t, 0, n)
}

// TestPushDelivery follows every kind of write that bumps a collection's
// sync token to its subscribers.
func TestPushDelivery(t *testing.T) {
	env, alice, bob := schedEnv(t)
	ps := newPushService(t, env)
	srv := env.Providers.DAV
	ctx := context.Background()
	path := cal + "alice/personal/"
	topic := topicOf(t, env, alice, path)
	round(t, env) // what setting up changed is nobody's business yet

	ps.register(t, env, alice, path, "content", contentTrigger)
	ps.register(t, env, alice, path, "props", propertyTrigger)
	ps.register(t, env, alice, "/remote.php/dav/addressbooks/alice/contacts/", "book", contentTrigger)
	ps.register(t, env, alice, cal+"alice/inbox/", "inbox", contentTrigger)
	ps.register(t, env, alice, cal+"alice/", "home", contentTrigger)
	round(t, env)
	for _, ep := range []string{"content", "props", "book", "inbox", "home"} {
		assert.Equal(t, 0, len(ps.take(ep)), ep)
	}

	// A PUT: one encrypted, content-free push to the collection's
	// subscribers with content triggers, and to nobody else.
	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Lunch")).Status(201)
	round(t, env)
	assert.Equal(t, []string{contentPush(topic)}, ps.take("content"))
	assert.Equal(t, 0, len(ps.take("props")))
	assert.Equal(t, 0, len(ps.take("book")))
	ps.mu.Lock()
	h := ps.headers["content"][len(ps.headers["content"])-1]
	ps.mu.Unlock()
	assert.Equal(t, topic, h.Get("Topic"))
	assert.Equal(t, "86400", h.Get("TTL"))
	assert.Equal(t, "aes128gcm", h.Get("Content-Encoding"))

	// Many writes before a round are one push; nothing more is owed after.
	for _, uid := range []string{"a", "b", "c"} {
		req(t, env, alice, "PUT", path+uid+".ics", ics("VEVENT", uid, "E "+uid)).Status(201)
	}
	req(t, env, alice, "DELETE", path+"a.ics", "").Status(204)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("content")))
	round(t, env)
	assert.Equal(t, 0, len(ps.take("content")))

	// A DELETE alone.
	req(t, env, alice, "DELETE", path+"b.ics", "").Status(204)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("content")))

	// An import of several events, each its own write: one push.
	c := personal(t, env, alice)
	res, err := srv.Import(alice.Username, c, strings.NewReader(
		strings.Replace(ics("VEVENT", "i1", "One"), "END:VCALENDAR\r\n", "", 1)+
			strings.TrimPrefix(ics("VEVENT", "i2", "Two"), "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n")))
	require.NoError(t, err)
	assert.Equal(t, 2, res.Imported)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("content")))

	// A web edit: toggling a task in the tasks app.
	req(t, env, alice, "PUT", path+"todo.ics", ics("VTODO", "todo", "Chores")).Status(201)
	round(t, env)
	ps.take("content")
	tp, ok := env.Providers.Plugins.Plugin("tasks")
	require.True(t, ok)
	tdb, err := tp.(*tasks.Plugin).DB(alice.Username)
	require.NoError(t, err)
	task, err := tasks.GetTaskByUID(tdb, int64(c.ID), "todo")
	require.NoError(t, err)
	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	env.Post(t, "/apps/tasks/task/"+strconv.FormatInt(task.ID, 10)+"/toggle",
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID),
		cogtest.Header("X-CSRF-Token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))).Status(200)
	round(t, env)
	assert.Equal(t, []string{contentPush(topic)}, ps.take("content"))

	// Properties: a PROPPATCH is a property update, for the subscriber
	// that asked for those only.
	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Mine</D:displayname></D:prop></D:set></D:propertyupdate>`).Status(207)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("content")))
	got := ps.take("props")
	require.Equal(t, 1, len(got))
	assert.Contains(t, got[0], "<P:topic>"+topic+"</P:topic><P:property-update/></P:push-message>")

	// Scheduling: Bob invites Alice, which lands in her inbox and her
	// calendar.
	invite := strings.NewReplacer("ORGANIZER;CN=Alice:mailto:a@example.com", "ORGANIZER;CN=Bob:mailto:b@example.com",
		"ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com", "ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:b@example.com",
		"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:b@example.com", "ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:a@example.com").Replace(meeting)
	req(t, env, bob, "PUT", cal+"bob/personal/meet.ics", invite).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("inbox")))
	assert.Equal(t, 1, len(ps.take("content")))

	// Collections made and deleted change the home.
	req(t, env, alice, "MKCALENDAR", cal+"alice/work/", "").Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("home")))
	ps.register(t, env, alice, cal+"alice/work/", "work", contentTrigger)
	req(t, env, alice, "DELETE", cal+"alice/work/", "").Status(204)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("home")))
	assert.Equal(t, 1, len(ps.take("work")), "a deleted collection's subscribers are told")
	// Then they are dropped.
	require.NoError(t, env.Providers.Pusher.Prune(ctx))
	subs, err := env.Providers.Push.OfUser(push.WebDAV, alice.ID)
	require.NoError(t, err)
	assert.Equal(t, 5, len(subs))

	// A subscription calendar's poll.
	feed := newFeedServer(t, feedV1)
	srv.Fetch = feed.Client() // the feed is on loopback, which safehttp refuses
	sc, _, err := srv.Subscribe(ctx, davUser(t, env, alice), feed.URL+"/school.ics", "School", "")
	require.NoError(t, err)
	spath := cal + "alice/" + sc.Name + "/"
	round(t, env)
	assert.Equal(t, 1, len(ps.take("home")))
	ps.register(t, env, alice, spath, "feed", contentTrigger)
	feed.set(strings.Replace(feedV1, "SUMMARY:Half term", "SUMMARY:Half term (moved)", 1), http.StatusOK)
	_, err = srv.Refresh(ctx, alice.Username, sc)
	require.NoError(t, err)
	round(t, env)
	assert.Equal(t, []string{contentPush(topicOf(t, env, alice, spath))}, ps.take("feed"))
	// An unchanged feed writes nothing and pushes nothing.
	_, err = srv.Refresh(ctx, alice.Username, sc)
	require.NoError(t, err)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("feed")))

	// The birthday calendar, following a card.
	bc, err := srv.EnableBirthdays(davUser(t, env, alice))
	require.NoError(t, err)
	bpath := cal + "alice/" + bc.Name + "/"
	round(t, env)
	assert.Equal(t, 1, len(ps.take("home")))
	ps.register(t, env, alice, bpath, "birthdays", contentTrigger)
	card := "BEGIN:VCARD\r\nVERSION:4.0\r\nUID:kid\r\nFN:Kid\r\nBDAY:20100415\r\nEND:VCARD\r\n"
	req(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/kid.vcf", card).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("book")))
	assert.Equal(t, []string{contentPush(topicOf(t, env, alice, bpath))}, ps.take("birthdays"))
	// A card edit that leaves the date alone writes no event.
	req(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/kid.vcf", strings.Replace(card, "FN:Kid", "FN:Kid\r\nNOTE:x", 1)).Status(204)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("book")))
	assert.Equal(t, 0, len(ps.take("birthdays")))
}

// TestPushSharing: a sharee's subscription follows the owner's collection
// while the share lasts.
func TestPushSharing(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	ps := newPushService(t, env)
	c := personal(t, env, alice)
	path := cal + "alice/personal/"

	ps.register(t, env, bob, cal+"bob/", "bobhome", contentTrigger)
	rec, err := env.Providers.Shares.Create(alice.ID, dav.ShareType, c.ID.String(), "USER", bob.ID, plugin.PermRead|plugin.PermWrite)
	require.NoError(t, err)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("bobhome")), "the share appears in Bob's home")

	ps.register(t, env, bob, path, "bob", contentTrigger+propertyTrigger)
	ps.register(t, env, alice, path, "alice", contentTrigger)
	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Lunch")).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("bob")))
	assert.Equal(t, 1, len(ps.take("alice")))
	// Bob's own write reaches Alice.
	req(t, env, bob, "PUT", path+"ev2.ics", ics("VEVENT", "ev2", "Dinner")).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("alice")))
	ps.take("bob")

	// Unshared: Bob's home changes, and his subscription to the calendar
	// is dropped rather than told of what he can no longer read.
	require.NoError(t, env.Providers.Shares.Delete(rec.ID))
	round(t, env)
	assert.Equal(t, 1, len(ps.take("bobhome")))
	assert.Equal(t, 0, len(ps.take("bob")))
	req(t, env, alice, "PUT", path+"ev3.ics", ics("VEVENT", "ev3", "Tea")).Status(201)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("bob")))
	assert.Equal(t, 1, len(ps.take("alice")))
	subs, err := env.Providers.Push.OfUser(push.WebDAV, bob.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, len(subs)) // his home's
}

// TestPushFailures: a subscription the push service has forgotten is
// removed; other failures are retried with backoff.
func TestPushFailures(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	ps := newPushService(t, env)
	path := cal + "alice/personal/"
	personal(t, env, alice)
	loc := ps.register(t, env, alice, path, "flaky", contentTrigger)
	ps.register(t, env, alice, path, "gone", contentTrigger)
	ps.register(t, env, alice, path, "lost", contentTrigger)
	id := loc[strings.LastIndex(loc, "/")+1:]
	ps.setStatus("gone", http.StatusGone)
	ps.setStatus("lost", http.StatusNotFound)
	ps.setStatus("flaky", http.StatusServiceUnavailable)

	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Lunch")).Status(201)
	round(t, env)
	subs, err := env.Providers.Push.OfUser(push.WebDAV, alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(subs))
	assert.Equal(t, id, subs[0].ID)
	assert.Equal(t, 1, subs[0].Failures)
	assert.Equal(t, "push service answered 503", subs[0].LastError)
	assert.Equal(t, env.Clock.Now().Add(push.Backoff(1)), subs[0].NextAttemptAt)

	// Not before its time.
	ps.setStatus("flaky", http.StatusCreated)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("flaky")))
	// Then once, carrying what happened meanwhile.
	req(t, env, alice, "PUT", path+"ev2.ics", ics("VEVENT", "ev2", "Dinner")).Status(201)
	env.Clock.Advance(push.Backoff(1))
	round(t, env)
	assert.Equal(t, 1, len(ps.take("flaky")))
	sub, err := env.Providers.Push.Get(id)
	require.NoError(t, err)
	assert.Equal(t, 0, sub.Failures)
	assert.Equal(t, uint(0), sub.Pending)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("flaky")))

	// Backoff doubles, within bounds.
	assert.Equal(t, 30*time.Second, push.Backoff(1))
	assert.Equal(t, time.Minute, push.Backoff(2))
	assert.Equal(t, 6*time.Hour, push.Backoff(40))
}

// TestPushDontNotify: a client's own change is not pushed back to it.
func TestPushDontNotify(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	ps := newPushService(t, env)
	c := personal(t, env, alice)
	path := cal + "alice/personal/"
	mine := ps.register(t, env, alice, path, "mine", contentTrigger)
	ps.register(t, env, alice, path, "other", contentTrigger)
	_, err := env.Providers.Shares.Create(alice.ID, dav.ShareType, c.ID.String(), "USER", bob.ID, plugin.PermRead|plugin.PermWrite)
	require.NoError(t, err)
	bobs := ps.register(t, env, bob, path, "bob", contentTrigger)
	round(t, env)

	req(t, env, alice, "PUT", path+"a.ics", ics("VEVENT", "a", "A"), "Push-Dont-Notify", `"`+mine+`"`).Status(201)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("mine")))
	assert.Equal(t, 1, len(ps.take("other")))
	assert.Equal(t, 1, len(ps.take("bob")))

	// Everyone.
	req(t, env, alice, "DELETE", path+"a.ics", "", "Push-Dont-Notify", "*").Status(204)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("mine"))+len(ps.take("other"))+len(ps.take("bob")))

	// Someone else's registration is not hers to silence.
	req(t, env, alice, "PUT", path+"b.ics", ics("VEVENT", "b", "B"), "Push-Dont-Notify", `"`+bobs+`", "junk"`).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("bob")))
	assert.Equal(t, 1, len(ps.take("mine")))

	// A change by someone else in the same round still reaches her.
	req(t, env, alice, "PUT", path+"c.ics", ics("VEVENT", "c", "C"), "Push-Dont-Notify", `"`+mine+`"`).Status(201)
	req(t, env, bob, "PUT", path+"d.ics", ics("VEVENT", "d", "D")).Status(201)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("mine")))

	// A request that changed nothing silences nothing.
	req(t, env, alice, "PUT", path+"e.ics", ics("VEVENT", "e", "E")).Status(201)
	req(t, env, alice, "PUT", path+"c.ics", ics("VEVENT", "c", "C"), "If-None-Match", "*", "Push-Dont-Notify", `"`+mine+`"`).Status(412)
	round(t, env)
	assert.Equal(t, 1, len(ps.take("mine")))
}
