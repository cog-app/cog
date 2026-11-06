package dav

import (
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/webpush"
	"git.duckfam.us/jj-software/cog/plugin"
)

// Root is the path every DAV URL starts with.
const Root = "/remote.php/dav/"

// User is a Cog user as the DAV core sees one.
type User struct {
	ID          string
	Username    string
	DisplayName string
	Email       string
}

// Users looks users up for principals and paths.
type Users interface {
	ByUsername(name string) (*User, error) // ErrNotFound when unknown
	ByID(id string) (*User, error)         // ErrNotFound when unknown
	// ByEmails finds users by email, ignoring case, keyed by the
	// lower-cased address. Unknown addresses are absent.
	ByEmails(emails []string) (map[string]*User, error)
	// EmailVisible reports whether the user with viewerID may see the
	// email address of the user with targetID.
	EmailVisible(viewerID, targetID string) (bool, error)
}

// DefaultRetention is how long tombstones are kept unless an admin changes it.
const DefaultRetention = 90 * 24 * time.Hour

// Server serves CalDAV and CardDAV under Root.
type Server struct {
	Collections *Store
	Users       Users
	Shares      Shares
	// Backends returns the backends of the plugins enabled now.
	Backends func() []Backend
	// Retention returns how long to keep tombstones; nil means
	// DefaultRetention.
	Retention func() time.Duration
	Clock     clock.Clock
	Log       *slog.Logger
	// Mail returns the sender for iMIP messages to external calendar users,
	// or nil when email is not configured. Mail itself may be nil.
	Mail func() plugin.EmailSender
	// MailLimit bounds the iMIP messages each user's changes send, so no
	// account can use the server to mail strangers in bulk. Nil sends
	// without a limit.
	MailLimit *ratelimit.Limiter
	// BaseURL is the server's external URL, for the absolute URLs of
	// managed attachments.
	BaseURL string
	// Secret signs the attachment URLs sent to external attendees and
	// feed subscribers.
	Secret []byte
	// Links keeps the secret links of ICS feeds; nil serves none.
	Links Links
	// Fetch is the client subscriptions are fetched with; nil means
	// safehttp's, which reaches only public addresses.
	Fetch *http.Client

	// Push keeps WebDAV-Push subscriptions, Pusher delivers to them, and
	// VAPID identifies the server to push services; WebDAV-Push is offered
	// only with all three (EnablePush).
	Push   *push.Store
	Pusher *push.Deliverer
	VAPID  *webpush.VAPID
	// UserListeners hear whose collections changed, at each push round.
	UserListeners []UserListener

	pushQ     pushQueue
	fetchOnce sync.Once
	fetch     *http.Client
	// birthdayStates holds a *birthdayState per owner's username.
	birthdayStates sync.Map
}

func (s *Server) retention() time.Duration {
	if s.Retention == nil {
		return DefaultRetention
	}
	if d := s.Retention(); d > 0 {
		return d
	}
	return DefaultRetention
}

func (s *Server) username(id string) (string, error) {
	u, err := s.Users.ByID(id)
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func homeComponents(h Home) []Component {
	if h == HomeAddressBooks {
		return []Component{VCARD}
	}
	return []Component{VEVENT, VTODO, VJOURNAL}
}

// --- Hrefs ---

func principalHref(username string) string {
	return Root + "principals/users/" + url.PathEscape(username) + "/"
}

func homeHref(home Home, username string) string {
	return Root + string(home) + "/" + url.PathEscape(username) + "/"
}

func collectionHref(c *Collection, ownerUsername string) string {
	return homeHref(c.Home, ownerUsername) + url.PathEscape(c.Name) + "/"
}

func resourceHref(c *Collection, ownerUsername, uri string) string {
	return collectionHref(c, ownerUsername) + url.PathEscape(uri)
}

// --- Access ---

// access is what the current user may do with a collection.
type access struct {
	owner bool
	perms uint32 // share bitmask; ignored for the owner
	// readOnly is a collection Cog fills itself (Collection.ReadOnly):
	// nobody writes its resources, whatever their share says.
	readOnly bool
}

// accessTo is the access of the owner (or of a sharee with perms) to c.
func accessTo(c *Collection, owner bool, perms uint32) access {
	return access{owner: owner, perms: perms, readOnly: c.ReadOnly()}
}

func (a access) read() bool { return a.owner || a.perms&plugin.PermRead != 0 }
func (a access) write() bool {
	return !a.readOnly && (a.owner || a.perms&plugin.PermWrite != 0)
}
func (a access) freeBusy() bool { return a.read() || a.perms&plugin.PermFreeBusy != 0 }
func (a access) busyOnly() bool { return !a.read() && a.freeBusy() }

// schedule reports whether the user's writes schedule for the owner: a
// sharee needs the Schedule permission to send invitations and replies in
// the owner's name.
func (a access) schedule() bool { return a.owner || a.perms&plugin.PermSchedule != 0 }

// --- Requests ---

// request carries what one HTTP request needs.
type request struct {
	s      *Server
	w      http.ResponseWriter
	r      *http.Request
	user   *User
	shared shareMap                  // shares that reach user, loaded on first use
	owned  map[CollectionID][]sharee // users the user's collections are shared with, loaded on first use
	bes    []Backend
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rest, ok := strings.CutPrefix(r.URL.Path, Root+feedsPath+"/"); ok {
		// A feed's secret is its credential.
		s.serveFeed(w, r, rest)
		return
	}
	au := auth.UserFromContext(r.Context())
	if rest, ok := strings.CutPrefix(r.URL.Path, Root+attachmentsPath+"/"); ok {
		// An external attendee fetches an attachment with a token instead
		// of credentials.
		var u *User
		if au != nil {
			u = &User{ID: au.ID, Username: au.Username, DisplayName: au.DisplayName, Email: au.Email}
		}
		s.serveAttachment(w, r, u, rest)
		return
	}
	if au == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	req := &request{
		s: s, w: w, r: r,
		user: &User{ID: au.ID, Username: au.Username, DisplayName: au.DisplayName, Email: au.Email},
		bes:  s.Backends(),
	}
	req.dispatch()
}

func (q *request) fail(err error) {
	q.s.Log.Error("dav request failed", "method", q.r.Method, "path", q.r.URL.Path, "user", q.user.Username, "err", err)
	http.Error(q.w, "Internal Server Error", http.StatusInternalServerError)
}

func (q *request) dispatch() {
	path := strings.TrimPrefix(q.r.URL.Path, strings.TrimSuffix(Root, "/"))
	path = strings.TrimPrefix(path, "/")
	first, rest, _ := strings.Cut(path, "/")
	switch first {
	case "":
		q.root()
	case "principals":
		q.principals(rest)
	case pushPath:
		q.pushRegistration(rest)
	case string(HomeCalendars), string(HomeAddressBooks):
		home := Home(first)
		if !q.hasHome(home) {
			http.NotFound(q.w, q.r)
			return
		}
		q.home(home, rest)
	default:
		http.NotFound(q.w, q.r)
	}
}

// hasHome reports whether any enabled backend stores components of home.
func (q *request) hasHome(home Home) bool {
	for _, b := range q.bes {
		for _, c := range b.Components() {
			if c.Home() == home {
				return true
			}
		}
	}
	return false
}

// components returns the components of c that an enabled backend stores.
func (q *request) components(c *Collection) []Component {
	var out []Component
	for _, comp := range c.Components {
		if q.backendFor(comp) != nil {
			out = append(out, comp)
		}
	}
	return out
}

func (q *request) backendFor(comp Component) Backend {
	for _, b := range q.bes {
		if slices.Contains(b.Components(), comp) {
			return b
		}
	}
	return nil
}

// backends returns the enabled backends that hold resources of c; for an
// inbox, the store that keeps it.
func (q *request) backends(c *Collection) []Backend {
	if c.ID == InboxID {
		if ib := q.s.inbox(q.bes); ib != nil {
			return []Backend{ib}
		}
		return nil
	}
	var out []Backend
	for _, b := range q.bes {
		if slices.ContainsFunc(b.Components(), c.Has) {
			out = append(out, b)
		}
	}
	return out
}

func (q *request) sharedWith() (shareMap, error) {
	if q.shared != nil {
		return q.shared, nil
	}
	m, err := sharesFor(q.s.Shares, q.user.ID)
	if err != nil {
		return nil, err
	}
	q.shared = m
	return m, nil
}

// access returns the current user's access to c.
func (q *request) access(c *Collection) (access, error) {
	if c.OwnerID == q.user.ID {
		return accessTo(c, true, 0), nil
	}
	shared, err := q.sharedWith()
	if err != nil {
		return access{}, err
	}
	return accessTo(c, false, shared.perms(c)), nil
}

// depth returns the Depth header as 0 or 1; infinity is treated as 1, since
// no collection has collections inside it.
func (q *request) depth() int {
	if strings.TrimSpace(q.r.Header.Get("Depth")) == "0" {
		return 0
	}
	return 1
}

func (q *request) options(allow string) {
	q.w.Header().Set("Allow", allow)
	dav := "1, 3, access-control, calendar-access, calendar-auto-schedule, calendar-no-timezone, calendar-managed-attachments, calendarserver-sharing, addressbook, extended-mkcol"
	if q.s.pushEnabled() {
		dav += ", webdav-push"
	}
	q.w.Header().Set("DAV", dav)
	q.w.WriteHeader(http.StatusOK)
}

const (
	allowRoot       = "OPTIONS, PROPFIND"
	allowHome       = "OPTIONS, PROPFIND, MKCOL, MKCALENDAR, POST"
	allowCollection = "OPTIONS, PROPFIND, PROPPATCH, REPORT, DELETE, POST"
	allowResource   = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, POST"
)

func (q *request) methodNotAllowed(allow string) {
	q.w.Header().Set("Allow", allow)
	http.Error(q.w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// --- Root and principals ---

func (q *request) root() {
	switch q.r.Method {
	case "PROPFIND":
		pf, err := q.propfindBody()
		if err != nil {
			http.Error(q.w, err.Error(), http.StatusBadRequest)
			return
		}
		props := []prop{
			{dav("resourcetype"), el(dav("collection"), "")},
			{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
			{dav("principal-collection-set"), hrefEl(Root + "principals/")},
		}
		ms := multistatus{Responses: []response{{Href: Root, Propstats: pf.evaluate(props, nil)}}}
		ms.write(q.w)
	case http.MethodOptions:
		q.options(allowRoot)
	default:
		q.methodNotAllowed(allowRoot)
	}
}

func (q *request) principals(rest string) {
	rest = strings.TrimSuffix(rest, "/")
	switch {
	case rest == "" || rest == "users":
		q.principalCollection(rest)
	case strings.HasPrefix(rest, "users/") && !strings.Contains(rest[len("users/"):], "/"):
		q.principal(rest[len("users/"):])
	default:
		http.NotFound(q.w, q.r)
	}
}

// principalCollection serves /principals/ and /principals/users/, listing
// only the current user.
func (q *request) principalCollection(rest string) {
	if q.r.Method == http.MethodOptions {
		q.options(allowRoot)
		return
	}
	if q.r.Method != "PROPFIND" {
		q.methodNotAllowed(allowRoot)
		return
	}
	pf, err := q.propfindBody()
	if err != nil {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	href := Root + "principals/"
	if rest != "" {
		href += rest + "/"
	}
	props := []prop{
		{dav("resourcetype"), el(dav("collection"), "")},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
	}
	ms := multistatus{Responses: []response{{Href: href, Propstats: pf.evaluate(props, nil)}}}
	if q.depth() == 1 && rest == "users" {
		ms.Responses = append(ms.Responses, response{
			Href: principalHref(q.user.Username), Propstats: pf.evaluate(q.principalProps(q.user), nil),
		})
	}
	ms.write(q.w)
}

func (q *request) principal(name string) {
	u, err := q.s.Users.ByUsername(name)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(q.w, q.r)
		return
	}
	if err != nil {
		q.fail(err)
		return
	}
	switch q.r.Method {
	case "PROPFIND":
		pf, err := q.propfindBody()
		if err != nil {
			http.Error(q.w, err.Error(), http.StatusBadRequest)
			return
		}
		ms := multistatus{Responses: []response{{Href: principalHref(u.Username), Propstats: pf.evaluate(q.principalProps(u), nil)}}}
		ms.write(q.w)
	case http.MethodOptions:
		q.options(allowRoot)
	default:
		q.methodNotAllowed(allowRoot)
	}
}

func (q *request) principalProps(u *User) []prop {
	email := u.Email
	if email != "" && u.ID != q.user.ID {
		ok, err := q.s.Users.EmailVisible(q.user.ID, u.ID)
		if err != nil {
			q.s.Log.Error("dav: email visibility", "user", u.Username, "err", err)
		}
		if !ok {
			email = ""
		}
	}
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	href := principalHref(u.Username)
	props := []prop{
		{dav("resourcetype"), el(dav("collection"), "") + el(dav("principal"), "")},
		{dav("displayname"), escape(name)},
		{dav("principal-URL"), hrefEl(href)},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
		{dav("principal-collection-set"), hrefEl(Root + "principals/")},
	}
	if q.hasHome(HomeCalendars) {
		addrs := hrefEl(href)
		if email != "" {
			addrs = hrefEl("mailto:"+email) + addrs
		}
		props = append(props,
			prop{caldav("calendar-home-set"), hrefEl(homeHref(HomeCalendars, u.Username))},
			prop{caldav("calendar-user-address-set"), addrs},
			prop{caldav("calendar-user-type"), "INDIVIDUAL"},
		)
		if q.s.inbox(q.bes) != nil {
			props = append(props,
				prop{caldav("schedule-inbox-URL"), hrefEl(homeHref(HomeCalendars, u.Username) + inboxName + "/")},
				prop{caldav("schedule-outbox-URL"), hrefEl(outboxHref(u.Username))},
			)
		}
	}
	if q.hasHome(HomeAddressBooks) {
		props = append(props, prop{carddav("addressbook-home-set"), hrefEl(homeHref(HomeAddressBooks, u.Username))})
	}
	if email != "" {
		props = append(props, prop{cs("email-address-set"), el(cs("email-address"), escape(email))})
	}
	return props
}

// --- Homes ---

// home routes a request under /calendars/ or /addressbooks/.
func (q *request) home(home Home, rest string) {
	ownerName, rest, _ := strings.Cut(rest, "/")
	if ownerName == "" {
		ownerName = q.user.Username
	}
	owner, err := q.s.Users.ByUsername(ownerName)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(q.w, q.r)
		return
	}
	if err != nil {
		q.fail(err)
		return
	}
	name, uri, _ := strings.Cut(rest, "/")
	uri = strings.TrimSuffix(uri, "/")
	switch {
	case name == "":
		q.homeCollection(home, owner)
	case reserved(home, name):
		q.scheduling(owner, name, uri)
	case uri == "":
		q.collection(home, owner, name)
	default:
		q.resource(home, owner, name, uri)
	}
}

// homeCollection serves a user's calendar or address book home. Only its
// owner may use it; shared collections are listed in the sharee's home with
// hrefs under their owner's.
func (q *request) homeCollection(home Home, owner *User) {
	if owner.ID != q.user.ID {
		http.Error(q.w, "Forbidden", http.StatusForbidden)
		return
	}
	switch q.r.Method {
	case "PROPFIND":
	case http.MethodPost:
		q.homePost(home)
		return
	case http.MethodOptions:
		q.options(allowHome)
		return
	default:
		q.methodNotAllowed(allowHome)
		return
	}
	pf, err := q.propfindBody()
	if err != nil {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	homeProps := []prop{
		{dav("resourcetype"), el(dav("collection"), "")},
		{dav("displayname"), escape(owner.Username)},
		{dav("owner"), hrefEl(principalHref(owner.Username))},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
		{dav("current-user-privilege-set"), privileges(access{owner: true}, home)},
	}
	var homeExtra []prop
	if home == HomeCalendars && q.s.attachments(q.bes) != nil {
		// Empty: attachments are on this server, reached however the client
		// reaches its home (RFC 8607 section 6.1).
		homeExtra = append(homeExtra, prop{caldav("managed-attachments-server-URL"), ""})
	}
	homeExtra = append(homeExtra, q.s.pushProps(homeResource(owner.ID, home), false)...)
	ms := multistatus{Responses: []response{{Href: homeHref(home, owner.Username), Propstats: pf.evaluate(homeProps, homeExtra)}}}
	if q.depth() == 1 {
		listed, err := q.listHome(home)
		if err != nil {
			q.fail(err)
			return
		}
		for _, l := range listed {
			props, extra, err := q.collectionProps(&l.Collection, l.access, l.owner, l.token)
			if err != nil {
				q.fail(err)
				return
			}
			ms.Responses = append(ms.Responses, response{
				Href: collectionHref(&l.Collection, l.owner), Propstats: pf.evaluate(props, extra),
			})
		}
		if home == HomeCalendars && q.s.inbox(q.bes) != nil {
			inbox := inboxCollection(owner)
			tok, err := q.token(inbox, owner.Username)
			if err != nil {
				q.fail(err)
				return
			}
			ms.Responses = append(ms.Responses,
				response{Href: collectionHref(inbox, owner.Username), Propstats: pf.evaluate(q.inboxProps(inbox, owner.Username, tok), q.s.pushProps(inboxResource(owner.ID), false))},
				response{Href: outboxHref(owner.Username), Propstats: pf.evaluate(q.outboxProps(owner), nil)},
			)
		}
	}
	ms.write(q.w)
}

// listed is a collection as it appears in the current user's home.
type listed struct {
	Collection
	owner  string // owner's username
	access access
	token  SyncToken
}

// listHome returns the current user's collections in home, creating the
// default one if there are none, followed by those shared with them.
func (q *request) listHome(home Home) ([]listed, error) {
	own, err := q.s.Collections.EnsureDefault(q.user.ID, home)
	if err != nil {
		return nil, err
	}
	var out []listed
	for _, c := range own {
		out = append(out, listed{Collection: c, owner: q.user.Username, access: accessTo(&c, true, 0)})
	}
	shared, err := q.s.Collections.Shared(q.s.Shares, q.s.username, q.user.ID, homeComponents(home)...)
	if err != nil {
		return nil, err
	}
	for _, sc := range shared {
		out = append(out, listed{Collection: sc.Collection, owner: sc.OwnerUsername, access: accessTo(&sc.Collection, false, sc.Permissions)})
	}
	if err := q.fillTokens(out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillTokens sets each collection's sync token with one query per owner and
// backend.
func (q *request) fillTokens(cols []listed) error {
	type key struct {
		owner string
		be    Backend
	}
	ids := map[key][]CollectionID{}
	for i := range cols {
		cols[i].token = SyncToken{}
		for _, b := range q.backends(&cols[i].Collection) {
			k := key{cols[i].owner, b}
			ids[k] = append(ids[k], cols[i].ID)
			cols[i].token[b.Key()] = 0
		}
	}
	for k, list := range ids {
		b := k.be
		states, err := b.States(k.owner, list)
		if err != nil {
			return fmt.Errorf("sync states from %s: %w", b.Key(), err)
		}
		for i := range cols {
			if cols[i].owner != k.owner {
				continue
			}
			if st, ok := states[cols[i].ID]; ok {
				cols[i].token[b.Key()] = st.Token
			}
		}
	}
	return nil
}

// token returns one collection's sync token.
func (q *request) token(c *Collection, ownerName string) (SyncToken, error) {
	l := []listed{{Collection: *c, owner: ownerName}}
	if err := q.fillTokens(l); err != nil {
		return nil, err
	}
	return l[0].token, nil
}

// privilegeSet writes privileges for current-user-privilege-set; the
// free/busy and scheduling ones are CalDAV's.
func privilegeSet(names []string) string {
	var b strings.Builder
	for _, x := range names {
		name := dav(x)
		if x == "read-free-busy" || strings.HasPrefix(x, "schedule-") {
			name = caldav(x)
		}
		b.WriteString(el(dav("privilege"), el(name, "")))
	}
	return b.String()
}

func privileges(a access, home Home) string {
	var names []string
	switch {
	case a.owner && a.readOnly:
		// The owner names, colours and deletes a collection Cog fills, but
		// writes nothing in it.
		names = []string{"read", "write-properties", "read-acl", "read-current-user-privilege-set"}
		if home == HomeCalendars {
			names = append(names, "read-free-busy")
		}
	case a.owner:
		names = []string{"all", "read", "write", "write-properties", "write-content", "bind", "unbind", "read-acl", "read-current-user-privilege-set"}
		if home == HomeCalendars {
			names = append(names, "read-free-busy")
		}
	default:
		names = []string{"read-current-user-privilege-set"}
		if a.read() {
			names = append(names, "read")
		}
		if a.freeBusy() && home == HomeCalendars {
			names = append(names, "read-free-busy")
		}
		if a.write() {
			// write-properties stays with the owner: sharees cannot PROPPATCH.
			names = append(names, "write", "write-content", "bind", "unbind")
		}
		if a.write() && a.perms&plugin.PermSchedule != 0 && home == HomeCalendars {
			// Writes by this sharee send invitations and replies as the owner.
			names = append(names, "schedule-send", "schedule-send-invite", "schedule-send-reply")
		}
	}
	return privilegeSet(names)
}

// collectionProps returns a collection's live properties, and those given
// only when asked for by name.
func (q *request) collectionProps(c *Collection, a access, ownerName string, tok SyncToken) ([]prop, []prop, error) {
	if c.ID == InboxID {
		return q.inboxProps(c, ownerName, tok), q.s.pushProps(inboxResource(c.OwnerID), false), nil
	}
	shared, sharing, err := q.sharingProps(c, a, ownerName)
	if err != nil {
		return nil, nil, err
	}
	kind := el(caldav("calendar"), "")
	reports := []xml.Name{caldav("calendar-multiget"), caldav("calendar-query"), dav("sync-collection")}
	if c.Home == HomeAddressBooks {
		kind = el(carddav("addressbook"), "")
		reports = []xml.Name{carddav("addressbook-multiget"), carddav("addressbook-query"), dav("sync-collection")}
	}
	switch {
	case !a.owner:
		kind += el(cs("shared"), "")
	case shared:
		kind += el(cs("shared-owner"), "")
	}
	var rs strings.Builder
	for _, r := range reports {
		rs.WriteString(el(dav("supported-report"), el(dav("report"), el(r, ""))))
	}
	display := c.Title()
	if !a.owner {
		display += " (" + ownerName + ")"
	}
	props := []prop{
		{dav("resourcetype"), el(dav("collection"), "") + kind},
		{dav("displayname"), escape(display)},
		{dav("owner"), hrefEl(principalHref(ownerName))},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
		{dav("current-user-privilege-set"), privileges(a, c.Home)},
		{dav("supported-report-set"), rs.String()},
		{dav("sync-token"), escape(tok.String())},
		{cs("getctag"), escape(ctag(tok, c.PropsVersion))},
		{dav("getetag"), escape(`"` + ctag(tok, c.PropsVersion) + `"`)},
	}
	maxSize := fmt.Sprint(maxResourceSize)
	if c.Home == HomeCalendars {
		var comps strings.Builder
		for _, comp := range q.components(c) {
			comps.WriteString(`<C:comp name="` + string(comp) + `"/>`)
		}
		props = append(props,
			prop{caldav("supported-calendar-component-set"), comps.String()},
			prop{caldav("supported-calendar-data"), `<C:calendar-data content-type="text/calendar" version="2.0"/>`},
			prop{caldav("max-resource-size"), maxSize},
			prop{apple("calendar-color"), escape(c.Color)},
			prop{apple("calendar-order"), fmt.Sprint(c.SortOrder)},
		)
		if c.Timezone != "" {
			props = append(props, prop{caldav("calendar-timezone-id"), escape(c.Timezone)})
		}
	} else {
		props = append(props,
			prop{carddav("supported-address-data"),
				`<CR:address-data-type content-type="text/vcard" version="3.0"/><CR:address-data-type content-type="text/vcard" version="4.0"/>`},
			prop{carddav("max-resource-size"), maxSize},
			prop{carddav("supported-collation-set"),
				`<CR:supported-collation>i;unicode-casemap</CR:supported-collation><CR:supported-collation>i;ascii-casemap</CR:supported-collation><CR:supported-collation>i;octet</CR:supported-collation>`},
		)
	}
	// A sharee who may only see when the owner is busy learns nothing else
	// about the collection: not its description, nor what the owner noted
	// on it in dead properties.
	if a.read() {
		desc := caldav("calendar-description")
		if c.Home != HomeCalendars {
			desc = carddav("addressbook-description")
		}
		props = append(props, prop{desc, escape(c.Description)})
	}
	if c.Kind == KindSubscription && a.owner {
		// Apple clients show where a subscribed calendar comes from. The
		// resource type stays calendar, not CS:subscribed: Cog holds the
		// events, and a client that saw subscribed would fetch the feed
		// itself instead of syncing them. Only the owner sees it: a feed
		// URL often carries its own secret.
		props = append(props, prop{cs("source"), hrefEl(c.Source)})
	}
	props = append(props, sharing...)
	if a.read() {
		for _, d := range c.DeadProps {
			props = append(props, prop{xml.Name{Space: d.Space, Local: d.Local}, d.Inner})
		}
	}
	var extra []prop
	if c.Home == HomeCalendars && c.Has(VEVENT) && q.s.attachments(q.bes) != nil {
		extra = append(extra,
			prop{caldav("max-attachment-size"), fmt.Sprint(MaxAttachmentSize)},
			prop{caldav("max-attachments-per-resource"), fmt.Sprint(MaxAttachmentsPerResource)},
		)
	}
	if a.read() {
		extra = append(extra, q.s.pushProps(collectionResource(c.ID), true)...)
	}
	return props, extra, nil
}

// Usage returns the bytes owner's calendar objects and cards hold across
// every backend, which count toward their quota (D22).
func (s *Server) Usage(owner string) (int64, error) {
	var total int64
	for _, b := range s.Backends() {
		n, err := b.Usage(owner)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}
