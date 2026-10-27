package dav

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/itip"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
	"git.duckfam.us/jj-software/cog/plugin"
	"github.com/google/uuid"
)

// Scheduling (RFC 6638): every calendar user has a schedule inbox, where the
// iTIP messages delivered to them are kept, and an outbox, which answers
// free/busy requests. Messages between Cog users are delivered by the
// server and applied to the recipient's calendar at once; external
// recipients get them by email (iMIP). DESIGN/dav-storage.md section 9.

// InboxID is the collection ID of every user's schedule inbox. Inboxes are
// not rows in dav_collections (no calendar UI lists them), and IDs there
// start at 1, so 0 can never name a calendar. The inbox's resources live in
// the plugin database of the backend that provides it, keyed by this ID.
const InboxID CollectionID = 0

// Names of the scheduling collections in a calendar home. They are reserved:
// no calendar may take them.
const (
	inboxName  = "inbox"
	outboxName = "outbox"
)

func reserved(home Home, name string) bool {
	return home == HomeCalendars && (name == inboxName || name == outboxName)
}

// InboxStore keeps a user's schedule inbox: the read side of a Backend over
// the InboxID collection, plus delivery. Clients delete messages once they
// have seen them; Deliver drops those older than cutoff.
type InboxStore interface {
	Backend
	Deliver(owner, uid string, data []byte, cutoff time.Time) error
}

// InboxProvider is implemented by the backend whose plugin keeps inboxes.
type InboxProvider interface {
	Inbox() InboxStore
}

// UIDLocator is implemented by backends that can find a UID among several
// collections in one query. It returns ErrNotFound when none holds it.
type UIDLocator interface {
	LocateUID(owner string, ids []CollectionID, uid string) (CollectionID, string, error)
}

// FreeBusyQuerier is implemented by backends whose resources make their
// owner busy: the periods of c's resources overlapping [start, end). self
// tells the owner's addresses apart, for their PARTSTAT.
type FreeBusyQuerier interface {
	FreeBusy(owner string, c *Collection, self func(string) bool, start, end time.Time) ([]itip.Period, error)
}

// Addresses returns a user's calendar user addresses, normalised
// (itip.Normalize): their principal URL, and their email as a mailto
// address when they have one. calendar-user-address-set lists the same.
func Addresses(u *User) []string {
	out := []string{principalHref(u.Username)}
	if u.Email != "" {
		out = append(out, itip.Normalize("mailto:"+u.Email))
	}
	return out
}

func selfOf(u *User) func(string) bool {
	addrs := Addresses(u)
	return func(a string) bool { return slices.Contains(addrs, a) }
}

// zone returns the collection's zone, for floating times.
func (c *Collection) zone() *time.Location {
	if loc := tz.Load(c.Timezone); loc != nil {
		return loc
	}
	return time.UTC
}

// transparent reports whether the owner marked the calendar as not making
// them busy (RFC 6638 §9.1, kept as a dead property). A calendar Cog fills
// (a subscription's school holidays, the birthdays) is transparent unless
// marked opaque.
func (c *Collection) transparent() bool {
	for _, d := range c.DeadProps {
		if d.Space == nsCalDAV && d.Local == "schedule-calendar-transp" {
			return strings.Contains(d.Inner, "transparent")
		}
	}
	return c.ReadOnly()
}

func (s *Server) mailer() plugin.EmailSender {
	if s.Mail == nil {
		return nil
	}
	return s.Mail()
}

func (s *Server) inbox(bes []Backend) InboxStore {
	for _, b := range bes {
		if p, ok := b.(InboxProvider); ok {
			return p.Inbox()
		}
	}
	return nil
}

// --- Implicit scheduling ---

// ScheduleChange is a change to an event in Owner's calendar, made by them
// or by a sharee writing for them.
type ScheduleChange struct {
	Owner    *User
	Old, New *ical.Object // Old nil when created, New nil when deleted
	// MaySend is false when a sharee without the Schedule permission made
	// the change: nothing is sent, and SCHEDULE-STATUS says so.
	MaySend bool
	// NoReply is Schedule-Reply: F on a DELETE (RFC 6638 §8.1): an
	// attendee deleting the event does not decline it.
	NoReply bool
}

// Delivery is the messages a change sends, worked out before it is stored.
type Delivery struct {
	s     *Server
	from  *User
	sends []send
}

type send struct {
	msg   itip.Message
	to    *User  // a Cog user, or nil
	email string // an external recipient's address
}

// Plan works out what a change sends (itip.Organize for an organizer,
// itip.Respond for an attendee), and records the outcome on New before it
// is stored: SCHEDULE-STATUS on each recipient, and the scheduling
// parameters a client may not store removed. Nothing is sent until Send,
// which the caller runs once the change is stored.
func (s *Server) Plan(ch ScheduleChange) (*Delivery, error) {
	d := &Delivery{s: s, from: ch.Owner}
	self := selfOf(ch.Owner)
	now := s.Clock()
	msgs := itip.Organize(ch.Old, ch.New, self, now)
	reply := false
	if len(msgs) == 0 && !(ch.New == nil && ch.NoReply) {
		if r := itip.Respond(ch.Old, ch.New, self, now); r != nil {
			msgs, reply = []itip.Message{*r}, true
		}
	}
	if ch.New != nil {
		itip.Clean(ch.Old, ch.New)
	}
	if len(msgs) == 0 {
		return d, nil
	}
	addrs := make([]string, len(msgs))
	for i, m := range msgs {
		addrs[i] = m.Recipient
	}
	users, err := s.resolve(addrs)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		u := users[m.Recipient]
		if u != nil && u.ID == ch.Owner.ID {
			continue
		}
		var status string
		switch {
		case !ch.MaySend:
			status = itip.StatusNoAuthority
		case u != nil:
			status = itip.StatusDelivered
			d.sends = append(d.sends, send{msg: m, to: u})
		case ical.Email(m.Recipient) == "":
			status = itip.StatusInvalidUser
		case s.mailer() == nil:
			status = itip.StatusUnavailable
		default:
			status = itip.StatusSent
			m.Object = s.forExternal(ch.Owner.Username, m.Object, m.Recipient)
			d.sends = append(d.sends, send{msg: m, email: ical.Email(m.Recipient)})
		}
		switch {
		case ch.New == nil:
		case reply:
			itip.SetOrganizerStatus(ch.New, status)
		default:
			itip.SetAttendeeStatus(ch.New, m.Recipient, status)
		}
	}
	return d, nil
}

// PlanFor is Plan for a change Cog made itself (the web UI) in the
// calendar of the user with ownerID, who may always schedule.
func (s *Server) PlanFor(ownerID string, old, new *ical.Object) (*Delivery, error) {
	u, err := s.Users.ByID(ownerID)
	if err != nil {
		return nil, err
	}
	return s.Plan(ScheduleChange{Owner: u, Old: old, New: new, MaySend: true})
}

// resolve finds the Cog users among normalised addresses: mailto addresses
// by email, in one query, and principal URLs by username.
func (s *Server) resolve(addrs []string) (map[string]*User, error) {
	out := map[string]*User{}
	var emails []string
	for _, a := range addrs {
		if e := ical.Email(a); e != "" {
			emails = append(emails, e)
			continue
		}
		rest, ok := strings.CutPrefix(a, Root+"principals/users/")
		if !ok {
			continue
		}
		name, err := url.PathUnescape(strings.TrimSuffix(rest, "/"))
		if err != nil || name == "" || strings.Contains(name, "/") {
			continue
		}
		u, err := s.Users.ByUsername(name)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[a] = u
	}
	if len(emails) > 0 {
		byEmail, err := s.Users.ByEmails(emails)
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			if u, ok := byEmail[strings.ToLower(ical.Email(a))]; ok {
				out[a] = u
			}
		}
	}
	return out, nil
}

// Send delivers the messages. Failures are logged: the change they come
// from is already stored, and its SCHEDULE-STATUS was recorded with it.
func (d *Delivery) Send() {
	if d == nil {
		return
	}
	for _, sd := range d.sends {
		var err error
		if sd.to != nil {
			err = d.s.deliver(sd.to, sd.msg)
		} else {
			err = d.s.sendMail(d.from, sd.email, sd.msg)
		}
		if err != nil {
			d.s.Log.Error("dav: schedule", "method", sd.msg.Method, "uid", sd.msg.Object.UID, "to", sd.msg.Recipient, "err", err)
		}
	}
}

// deliver applies a message to a Cog user's calendar (itip.Apply) and keeps
// it in their inbox. A REQUEST for an event they do not have yet lands in
// their default calendar; a reply or cancellation of one they do not have
// only reaches the inbox.
func (s *Server) deliver(u *User, m itip.Message) error {
	bes := s.Backends()
	var events Backend
	for _, b := range bes {
		if slices.Contains(b.Components(), VEVENT) {
			events = b
		}
	}
	inbox := s.inbox(bes)
	if events == nil || inbox == nil {
		return errors.New("no backend stores events")
	}
	var text bytes.Buffer
	if err := contentline.Write(&text, []*contentline.Component{m.Calendar()}); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	cols, err := s.Collections.EnsureDefault(u.ID, HomeCalendars)
	if err != nil {
		return err
	}
	// An invitation never lands in, or changes, a calendar Cog fills.
	cols = slices.DeleteFunc(cols, func(c Collection) bool { return !c.Has(VEVENT) || c.ReadOnly() })
	var applyErr error
	if len(cols) > 0 {
		applyErr = s.apply(bes, events, u, cols, m)
	}
	if err := inbox.Deliver(u.Username, m.Object.UID, text.Bytes(), s.Clock().Add(-s.retention())); err != nil {
		return errors.Join(applyErr, err)
	}
	return applyErr
}

// apply stores the recipient's copy as a message changes it, retrying when
// the copy changed between reading and writing it.
func (s *Server) apply(bes []Backend, events Backend, u *User, cols []Collection, m itip.Message) error {
	for attempt := 0; ; attempt++ {
		c, r, err := s.locate(events, u.Username, cols, m.Object.UID)
		if err != nil {
			return err
		}
		var cur *ical.Object
		cond := davsql.Conditions{IfNoneMatchAny: true}
		uri := ""
		if r != nil {
			if cur, err = decodeEvent(r.Data); err != nil {
				return fmt.Errorf("read %s: %w", r.URI, err)
			}
			cond, uri = davsql.Conditions{IfMatch: []int64{r.Version}}, r.URI
		} else {
			c, uri = &cols[0], resourceName(m.Object.UID)
		}
		next, changed := itip.Apply(m.Method, cur, m.Object, c.zone())
		if !changed || next == nil {
			return nil
		}
		req, err := objectRequest(uri, next, cond)
		if err != nil {
			return err
		}
		_, err = s.put(bes, u.Username, c, req)
		if errors.Is(err, ErrPrecondition) && attempt < 2 {
			continue
		}
		return err
	}
}

// locate finds the resource with uid among cols, with its data.
func (s *Server) locate(b Backend, owner string, cols []Collection, uid string) (*Collection, *Resource, error) {
	byID := func(id CollectionID) *Collection {
		for i := range cols {
			if cols[i].ID == id {
				return &cols[i]
			}
		}
		return nil
	}
	var c *Collection
	var uri string
	if l, ok := b.(UIDLocator); ok {
		ids := make([]CollectionID, len(cols))
		for i := range cols {
			ids[i] = cols[i].ID
		}
		id, u, err := l.LocateUID(owner, ids, uid)
		if errors.Is(err, ErrNotFound) {
			return nil, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		c, uri = byID(id), u
	} else {
		for i := range cols {
			u, err := b.FindUID(owner, &cols[i], uid)
			if err != nil {
				return nil, nil, err
			}
			if u != "" {
				c, uri = &cols[i], u
				break
			}
		}
	}
	if c == nil {
		return nil, nil, nil
	}
	list, err := b.Get(owner, c, []string{uri}, true)
	if err != nil || len(list) == 0 {
		return nil, nil, err
	}
	return c, &list[0], nil
}

func decodeEvent(data []byte) (*ical.Object, error) {
	comps, err := contentline.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if len(comps) != 1 {
		return nil, errors.New("not one VCALENDAR")
	}
	return ical.Decode(comps[0], ical.VEVENT)
}

// encodeObject writes an object as Cog serves it.
func encodeObject(o *ical.Object) (*contentline.Component, []byte, error) {
	cal := o.Encode(o.IANAZones)
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{cal}); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", o.UID, err)
	}
	return cal, b.Bytes(), nil
}

// objectRequest is a PutRequest storing o.
func objectRequest(uri string, o *ical.Object, cond davsql.Conditions) (*PutRequest, error) {
	cal, data, err := encodeObject(o)
	if err != nil {
		return nil, err
	}
	return &PutRequest{URI: uri, UID: o.UID, Component: VEVENT, Data: data, Object: cal, Conditions: cond}, nil
}

// resourceName names a delivered event after its UID when the UID is safe
// in a path, as clients name theirs, and with a fresh UUID otherwise.
func resourceName(uid string) string {
	ok := uid != "" && len(uid) <= 200
	for _, r := range uid {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.@", r)) {
			ok = false
			break
		}
	}
	if !ok {
		uid = uuid.NewString()
	}
	return uid + ".ics"
}

// sendMail sends a message to an external recipient as iMIP (RFC 6047):
// a short text and the iTIP message attached.
func (s *Server) sendMail(from *User, to string, m itip.Message) error {
	mail := s.mailer()
	if mail == nil {
		return errors.New("email is not configured")
	}
	var text bytes.Buffer
	if err := contentline.Write(&text, []*contentline.Component{m.Calendar()}); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	e := m.Object.Components[0]
	name := from.DisplayName
	if name == "" {
		name = from.Username
	}
	var subject string
	switch m.Method {
	case itip.Cancel:
		subject = "Cancelled: " + e.Summary
	case itip.Reply:
		subject = name + " replied: " + e.Summary
		if len(e.Attendees) > 0 {
			subject = name + " " + strings.ToLower(e.Attendees[0].PartStat) + ": " + e.Summary
		}
	default:
		subject = "Invitation: " + e.Summary
	}
	var body strings.Builder
	fmt.Fprintf(&body, "Event: %s\n", e.Summary)
	if when := describeTime(m.Object, e); when != "" {
		fmt.Fprintf(&body, "When: %s\n", when)
	}
	if e.Location != "" {
		fmt.Fprintf(&body, "Where: %s\n", e.Location)
	}
	if e.Description != "" {
		fmt.Fprintf(&body, "\n%s\n", e.Description)
	}
	switch m.Method {
	case itip.Cancel:
		body.WriteString("\nThis event has been cancelled.\n")
	case itip.Reply:
		fmt.Fprintf(&body, "\n%s has replied: %s.\n", name, strings.ToLower(e.Attendees[0].PartStat))
	}
	return mail.SendWithAttachments(to, subject, body.String(), "", []plugin.EmailAttachment{{
		Filename:    "invite.ics",
		ContentType: "text/calendar; method=" + string(m.Method) + "; charset=utf-8",
		Data:        text.Bytes(),
	}})
}

// describeTime says when a component starts, in its own zone.
func describeTime(o *ical.Object, e *ical.Component) string {
	if e.Start.IsZero() {
		return ""
	}
	t, err := e.Start.Time(ical.ZonesFor(o, time.UTC))
	if err != nil {
		return ""
	}
	if e.Start.IsDate {
		return t.Format("Monday, January 2, 2006")
	}
	return t.Format("Monday, January 2, 2006 15:04 MST")
}

// --- Inbox and outbox ---

func inboxCollection(owner *User) *Collection {
	return &Collection{ID: InboxID, OwnerID: owner.ID, Home: HomeCalendars, Name: inboxName, DisplayName: "Inbox", Components: []Component{VEVENT}}
}

func outboxHref(username string) string { return homeHref(HomeCalendars, username) + outboxName + "/" }

const (
	allowInbox   = "OPTIONS, PROPFIND, REPORT"
	allowOutbox  = "OPTIONS, PROPFIND, POST"
	allowMessage = "OPTIONS, GET, HEAD, DELETE, PROPFIND"
)

// scheduling serves a user's inbox and outbox, and the messages in the
// inbox. Only their owner may use them.
func (q *request) scheduling(owner *User, name, uri string) {
	if owner.ID != q.user.ID {
		http.Error(q.w, "Forbidden", http.StatusForbidden)
		return
	}
	if q.s.inbox(q.bes) == nil {
		http.NotFound(q.w, q.r)
		return
	}
	if q.r.Method == "MKCALENDAR" || q.r.Method == "MKCOL" {
		writeError(q.w, http.StatusMethodNotAllowed, dav("resource-must-be-null"), "")
		return
	}
	if name == outboxName {
		if uri != "" {
			http.NotFound(q.w, q.r)
			return
		}
		switch q.r.Method {
		case "PROPFIND":
			q.propfindOutbox(owner)
		case http.MethodPost:
			q.outboxPost(owner)
		case http.MethodOptions:
			q.options(allowOutbox)
		default:
			q.methodNotAllowed(allowOutbox)
		}
		return
	}
	c := inboxCollection(owner)
	a := access{owner: true}
	if uri == "" {
		switch q.r.Method {
		case "PROPFIND":
			q.propfindCollection(c, owner, a)
		case "REPORT":
			q.report(c, owner, a)
		case http.MethodOptions:
			q.options(allowInbox)
		default:
			q.methodNotAllowed(allowInbox)
		}
		return
	}
	switch q.r.Method {
	case http.MethodGet, http.MethodHead:
		q.get(c, owner, a, uri)
	case http.MethodDelete:
		q.delete(c, owner, a, uri)
	case "PROPFIND":
		q.propfindResource(c, owner, a, uri)
	case http.MethodOptions:
		q.options(allowMessage)
	case http.MethodPut:
		// Messages arrive by delivery only (RFC 6638 §2.2).
		writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
	default:
		q.methodNotAllowed(allowMessage)
	}
}

// Privileges of the owner on their scheduling collections (RFC 6638 §6).
var (
	inboxPrivileges = []string{"read", "unbind", "read-current-user-privilege-set",
		"schedule-deliver", "schedule-deliver-invite", "schedule-deliver-reply", "schedule-query-freebusy"}
	outboxPrivileges = []string{"read", "read-current-user-privilege-set",
		"schedule-send", "schedule-send-invite", "schedule-send-reply", "schedule-send-freebusy"}
)

// inboxProps are the inbox's properties; tok is its sync token.
func (q *request) inboxProps(c *Collection, ownerName string, tok SyncToken) []prop {
	var rs strings.Builder
	for _, r := range []string{"calendar-multiget", "calendar-query"} {
		rs.WriteString(el(dav("supported-report"), el(dav("report"), el(caldav(r), ""))))
	}
	rs.WriteString(el(dav("supported-report"), el(dav("report"), el(dav("sync-collection"), ""))))
	props := []prop{
		{dav("resourcetype"), el(dav("collection"), "") + el(caldav("schedule-inbox"), "")},
		{dav("displayname"), escape(c.Title())},
		{dav("owner"), hrefEl(principalHref(ownerName))},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
		{dav("current-user-privilege-set"), privilegeSet(inboxPrivileges)},
		{dav("supported-report-set"), rs.String()},
		{dav("sync-token"), escape(tok.String())},
		{cs("getctag"), escape(ctag(tok, 0))},
		{caldav("supported-calendar-component-set"), `<C:comp name="VEVENT"/>`},
	}
	if def, err := q.defaultCalendar(c.OwnerID); err != nil {
		q.s.Log.Error("dav: default calendar", "user", ownerName, "err", err)
	} else if def != nil {
		props = append(props, prop{caldav("schedule-default-calendar-URL"), hrefEl(collectionHref(def, ownerName))})
	}
	return props
}

// defaultCalendar is where invitations to a user land: their first calendar
// that holds events.
func (q *request) defaultCalendar(ownerID string) (*Collection, error) {
	cols, err := q.s.Collections.EnsureDefault(ownerID, HomeCalendars)
	if err != nil {
		return nil, err
	}
	for i := range cols {
		if cols[i].Has(VEVENT) && !cols[i].ReadOnly() {
			return &cols[i], nil
		}
	}
	return nil, nil
}

func (q *request) outboxProps(owner *User) []prop {
	return []prop{
		{dav("resourcetype"), el(dav("collection"), "") + el(caldav("schedule-outbox"), "")},
		{dav("displayname"), "Outbox"},
		{dav("owner"), hrefEl(principalHref(owner.Username))},
		{dav("current-user-principal"), hrefEl(principalHref(q.user.Username))},
		{dav("current-user-privilege-set"), privilegeSet(outboxPrivileges)},
		{dav("supported-report-set"), ""},
	}
}

func (q *request) propfindOutbox(owner *User) {
	pf, err := q.propfindBody()
	if err != nil {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	ms := multistatus{Responses: []response{{Href: outboxHref(owner.Username), Propstats: pf.evaluate(q.outboxProps(owner), nil)}}}
	ms.write(q.w)
}

// --- Free/busy ---

// busy returns the merged busy time of owner's collections cols.
func (q *request) busy(owner *User, cols []Collection, start, end time.Time) ([]itip.Period, error) {
	self := selfOf(owner)
	var all []itip.Period
	for i := range cols {
		for _, b := range q.backends(&cols[i]) {
			fq, ok := b.(FreeBusyQuerier)
			if !ok {
				continue
			}
			ps, err := fq.FreeBusy(owner.Username, &cols[i], self, start, end)
			if err != nil {
				return nil, err
			}
			all = append(all, ps...)
		}
	}
	return itip.Merge(all, start, end), nil
}

// freeBusyQuery answers the free-busy-query REPORT (RFC 4791 §7.10) with a
// VFREEBUSY of the collection's busy time.
func (q *request) freeBusyQuery(c *Collection, owner *User, n *node) {
	tr := n.child(caldav("time-range"))
	start, ok1 := parseUTC(tr.attr("start"))
	end, ok2 := parseUTC(tr.attr("end"))
	if !ok1 || !ok2 || !end.After(start) {
		http.Error(q.w, "free-busy-query needs a time-range with a start and an end", http.StatusBadRequest)
		return
	}
	ps, err := q.busy(owner, []Collection{*c}, start, end)
	if err != nil {
		q.fail(err)
		return
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{itip.Calendar("", itip.FreeBusy(ps, start, end, q.s.Clock()))}); err != nil {
		q.fail(err)
		return
	}
	q.w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	q.w.WriteHeader(http.StatusOK)
	q.w.Write(b.Bytes())
}

// outboxPost answers a free/busy request POSTed to the outbox (RFC 6638
// §5): for each attendee, the busy time of their calendars that the
// requester may see. That is all of a user's own calendars, and of someone
// else's those they shared with the requester with the FreeBusy or Read
// permission; a calendar marked transparent (schedule-calendar-transp)
// never counts. Other iTIP messages are delivered implicitly, so the outbox
// accepts no others.
func (q *request) outboxPost(owner *User) {
	data, err := io.ReadAll(io.LimitReader(q.r.Body, maxResourceSize+1))
	if err != nil || len(data) > maxResourceSize {
		http.Error(q.w, "read body", http.StatusBadRequest)
		return
	}
	bad := func() { writeError(q.w, http.StatusBadRequest, caldav("valid-scheduling-message"), "") }
	comps, err := contentline.Parse(bytes.NewReader(data))
	if err != nil || len(comps) != 1 || !comps[0].Is("VCALENDAR") {
		bad()
		return
	}
	cal := comps[0]
	if m := cal.Prop("METHOD"); m == nil || !strings.EqualFold(m.Value, string(itip.Request)) {
		bad()
		return
	}
	var fb *contentline.Component
	for _, sub := range cal.Components {
		if sub.Is("VFREEBUSY") {
			fb = sub
		}
	}
	if fb == nil {
		writeError(q.w, http.StatusForbidden, caldav("supported-calendar-component"), "")
		return
	}
	org := fb.Prop("ORGANIZER")
	if org == nil || !selfOf(owner)(itip.Normalize(org.Value)) {
		writeError(q.w, http.StatusForbidden, caldav("organizer-allowed"), "")
		return
	}
	var start, end time.Time
	ok := true
	if p := fb.Prop("DTSTART"); p != nil {
		start, ok = parseUTC(p.Value)
	}
	if p := fb.Prop("DTEND"); p != nil && ok {
		end, ok = parseUTC(p.Value)
	}
	if !ok || start.IsZero() || !end.After(start) {
		bad()
		return
	}
	var attendees []*contentline.Property
	var addrs []string
	for p := range fb.PropsNamed("ATTENDEE") {
		attendees = append(attendees, p)
		addrs = append(addrs, itip.Normalize(p.Value))
	}
	users, err := q.s.resolve(addrs)
	if err != nil {
		q.fail(err)
		return
	}
	shared, err := q.sharedWith()
	if err != nil {
		q.fail(err)
		return
	}
	var out strings.Builder
	out.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<C:schedule-response` + rootDecls + `>`)
	for i, att := range attendees {
		status, reply, err := q.freeBusyReply(users[addrs[i]], shared, fb, org, att, start, end)
		if err != nil {
			q.fail(err)
			return
		}
		out.WriteString(`<C:response>` + el(caldav("recipient"), hrefEl(att.Value)) + el(caldav("request-status"), escape(status)))
		if reply != "" {
			out.WriteString(el(caldav("calendar-data"), escape(reply)))
		}
		out.WriteString(`</C:response>`)
	}
	out.WriteString(`</C:schedule-response>`)
	q.w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	q.w.WriteHeader(http.StatusOK)
	io.WriteString(q.w, out.String())
}

// freeBusyReply answers one attendee of a free/busy request: a request
// status, and the VFREEBUSY reply when there is one.
func (q *request) freeBusyReply(u *User, shared shareMap, req *contentline.Component, org, att *contentline.Property, start, end time.Time) (string, string, error) {
	if u == nil {
		return "3.7;Invalid calendar user", "", nil
	}
	var cols []Collection
	if u.ID == q.user.ID {
		own, err := q.s.Collections.ListWith(u.ID, VEVENT)
		if err != nil {
			return "", "", err
		}
		cols = own
	} else {
		var ids []CollectionID
		for id, byOwner := range shared {
			if byOwner[u.ID]&(plugin.PermRead|plugin.PermFreeBusy) != 0 {
				ids = append(ids, id)
			}
		}
		found, err := q.s.Collections.ByIDs(ids)
		if err != nil {
			return "", "", err
		}
		for _, c := range found {
			if c.OwnerID == u.ID && c.Has(VEVENT) {
				cols = append(cols, c)
			}
		}
		if len(cols) == 0 {
			return "3.8;No authority", "", nil
		}
	}
	cols = slices.DeleteFunc(cols, func(c Collection) bool { return c.transparent() })
	ps, err := q.busy(u, cols, start, end)
	if err != nil {
		return "", "", err
	}
	props := []contentline.Property{*org, *att}
	if uid := req.Prop("UID"); uid != nil {
		props = append(props, *uid)
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{itip.Calendar(itip.Reply, itip.FreeBusy(ps, start, end, q.s.Clock(), props...))}); err != nil {
		return "", "", err
	}
	return "2.0;Success", b.String(), nil
}

// CalendarAddress is the address Cog writes for a user as ORGANIZER or
// ATTENDEE: their email as a mailto address, or their principal URL when
// they have none.
func CalendarAddress(u *User) string {
	if u.Email != "" {
		return "mailto:" + u.Email
	}
	return principalHref(u.Username)
}
