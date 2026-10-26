// Package itip works out the iTIP messages (RFC 5546) a change to a calendar
// object implies, and applies the messages a calendar user receives to their
// copy, for server-side scheduling (RFC 6638). It does no I/O: the DAV core
// resolves addresses, delivers and stores.
//
// Only events are scheduled; DESIGN/dav-storage.md section 9 says why.
package itip

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// Method is an iTIP method.
type Method string

const (
	Request Method = "REQUEST"
	Reply   Method = "REPLY"
	Cancel  Method = "CANCEL"
)

// Scheduling parameters (RFC 6638 §7). They are for the server and are never
// sent to recipients.
const (
	paramAgent     = "SCHEDULE-AGENT"
	paramStatus    = "SCHEDULE-STATUS"
	paramForceSend = "SCHEDULE-FORCE-SEND"
)

// SCHEDULE-STATUS values (RFC 6638 §3.2.9 and RFC 5546 §3.6).
const (
	StatusSent        = "1.1" // handed to another system (email)
	StatusDelivered   = "1.2"
	StatusInvalidUser = "3.7"
	StatusNoAuthority = "3.8"
	StatusUnavailable = "5.1"
)

// Message is one iTIP message to one recipient.
type Message struct {
	Method Method
	// Recipient is the normalised cal-address of the recipient.
	Recipient string
	// Object holds the components sent. Calendar adds METHOD.
	Object *ical.Object
}

// Calendar writes the message as a VCALENDAR with METHOD.
func (m Message) Calendar() *contentline.Component {
	o := *m.Object
	o.Props = append([]contentline.Property{{Name: "METHOD", Value: string(m.Method)}}, withoutMethod(m.Object.Props)...)
	return o.Encode(o.IANAZones)
}

func withoutMethod(props []contentline.Property) []contentline.Property {
	return slices.DeleteFunc(slices.Clone(props), func(p contentline.Property) bool { return p.Is("METHOD") })
}

// Normalize returns the form of a cal-address used to compare addresses: a
// mailto address lower-cased, an http(s) URL or a path reduced to its path
// with a trailing slash (principal URLs), and anything else as written.
func Normalize(addr string) string {
	a := strings.TrimSpace(addr)
	if e := ical.Email(a); e != "" {
		return "mailto:" + strings.ToLower(e)
	}
	if strings.HasPrefix(strings.ToLower(a), "http://") || strings.HasPrefix(strings.ToLower(a), "https://") {
		u, err := url.Parse(a)
		if err != nil {
			return a
		}
		a = u.Path
	}
	if strings.HasPrefix(a, "/") && !strings.HasSuffix(a, "/") {
		a += "/"
	}
	return a
}

// --- Parameters ---

func param(ps []contentline.Param, name string) string {
	for _, p := range ps {
		if strings.EqualFold(p.Name, name) && len(p.Values) > 0 {
			return p.Values[0]
		}
	}
	return ""
}

// setParam replaces the parameter name with v, or removes it when v is "".
func setParam(ps []contentline.Param, name, v string) []contentline.Param {
	out := slices.DeleteFunc(slices.Clone(ps), func(p contentline.Param) bool { return strings.EqualFold(p.Name, name) })
	if v != "" {
		out = append(out, contentline.Param{Name: name, Values: []string{v}})
	}
	return out
}

// serverAgent reports whether the server schedules for a property with
// these parameters: SCHEDULE-AGENT absent or SERVER (RFC 6638 §7.1).
func serverAgent(ps []contentline.Param) bool {
	a := param(ps, paramAgent)
	return a == "" || strings.EqualFold(a, "SERVER")
}

// stripParams removes the scheduling parameters, which are never sent to a
// recipient.
func stripParams(ps []contentline.Param) []contentline.Param {
	return slices.DeleteFunc(slices.Clone(ps), func(p contentline.Param) bool {
		return strings.EqualFold(p.Name, paramAgent) || strings.EqualFold(p.Name, paramStatus) || strings.EqualFold(p.Name, paramForceSend)
	})
}

// --- Reading objects ---

// Organizer returns the object's ORGANIZER, from its first component that
// has one.
func Organizer(o *ical.Object) *ical.Organizer {
	if o == nil {
		return nil
	}
	for _, e := range o.Components {
		if e.Organizer != nil {
			return e.Organizer
		}
	}
	return nil
}

func attendee(e *ical.Component, addr string) *ical.Attendee {
	for i := range e.Attendees {
		if Normalize(e.Attendees[i].Address) == addr {
			return &e.Attendees[i]
		}
	}
	return nil
}

// attendees returns the normalised addresses of every attendee in o, in
// order of appearance, except those self names.
func attendees(o *ical.Object, self func(string) bool) []string {
	if o == nil {
		return nil
	}
	var out []string
	for _, e := range o.Components {
		for _, a := range e.Attendees {
			if n := Normalize(a.Address); !self(n) && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	return out
}

// firstAttendee returns addr's ATTENDEE in the first component of o listing
// it.
func firstAttendee(o *ical.Object, addr string) *ical.Attendee {
	if o == nil {
		return nil
	}
	for _, e := range o.Components {
		if a := attendee(e, addr); a != nil {
			return a
		}
	}
	return nil
}

func sequence(e *ical.Component) int {
	if e == nil || e.Sequence == nil {
		return 0
	}
	return *e.Sequence
}

// sameRID reports whether two components describe the same occurrence: both
// masters, or overrides of the same instant.
func sameRID(a, b *ical.Component, z ical.Zones) bool {
	if a.IsOverride() != b.IsOverride() {
		return false
	}
	if !a.IsOverride() {
		return true
	}
	ta, err1 := a.RecurrenceID.Time(z)
	tb, err2 := b.RecurrenceID.Time(z)
	if err1 != nil || err2 != nil {
		return a.RecurrenceID == b.RecurrenceID
	}
	return ta.Equal(tb)
}

func find(o *ical.Object, e *ical.Component, z ical.Zones) *ical.Component {
	if o == nil {
		return nil
	}
	for _, c := range o.Components {
		if sameRID(c, e, z) {
			return c
		}
	}
	return nil
}

// --- Organizer ---

// view returns the part of o that attendee addr is invited to, as it is
// sent to them: the master and the overrides listing them, with an EXDATE
// for each override that leaves them out (the series ending before a
// RANGE=THISANDFUTURE one), or only those overrides when the master does
// not list them. It is nil when they are invited to nothing.
func view(o *ical.Object, addr string) *ical.Object {
	if o == nil {
		return nil
	}
	var comps []*ical.Component
	m := o.Master()
	if m != nil && attendee(m, addr) != nil {
		mc := sendable(m)
		series := &ical.Object{UID: o.UID, Components: []*ical.Component{mc}, Zones: o.Zones}
		z := ical.ZonesFor(o, time.UTC) // floating times stay floating either way
		var overrides []*ical.Component
		for _, e := range o.Components {
			switch {
			case !e.IsOverride():
			case attendee(e, addr) != nil:
				overrides = append(overrides, sendable(e))
			case !mc.Recurs():
			case e.ThisAndFuture:
				// Left out from this occurrence on: their series ends there.
				if rid, err := e.RecurrenceID.Time(z); err == nil {
					if remains, err := series.Truncate(rid, z); err == nil && !remains {
						series.Components = nil
					}
				}
			default:
				mc.ExDates = append(mc.ExDates, ical.DateValue{DateTime: e.RecurrenceID})
			}
		}
		comps = append(series.Components, overrides...)
	} else {
		for _, e := range o.Components {
			if e.IsOverride() && attendee(e, addr) != nil {
				comps = append(comps, sendable(e))
			}
		}
	}
	if len(comps) == 0 {
		return nil
	}
	return &ical.Object{UID: o.UID, Components: comps, Props: withoutMethod(o.Props), Zones: o.Zones}
}

// sendable copies a component as it goes to someone else: without the
// sender's alarms or scheduling parameters.
func sendable(e *ical.Component) *ical.Component {
	c := e.Clone()
	c.Alarms = nil
	for i := range c.Attendees {
		c.Attendees[i].Params = stripParams(c.Attendees[i].Params)
	}
	if c.Organizer != nil {
		c.Organizer.Params = stripParams(c.Organizer.Params)
	}
	return c
}

// significant is what of a view an attendee must hear about when it
// changes: everything iTIP carries except time stamps and replies.
func significant(v *ical.Object) string {
	type att struct{ Address, Role, CUType string }
	type comp struct {
		RID                            ical.DateTime
		Future                         bool
		Start, End                     ical.DateTime
		Duration, RRule                string
		RDates, ExDates                []ical.DateValue
		Summary, Location, Description string
		Status, URL                    string
		Sequence                       int
		Organizer                      string
		Attendees                      []att
	}
	var cs []comp
	for _, e := range v.Components {
		c := comp{
			RID: e.RecurrenceID, Future: e.ThisAndFuture, Start: e.Start, End: e.End, Duration: e.Duration,
			RRule: e.RRule, RDates: e.RDates, ExDates: e.ExDates, Summary: e.Summary, Location: e.Location,
			Description: e.Description, Status: strings.ToUpper(e.Status), URL: e.URL, Sequence: sequence(e),
		}
		if e.Organizer != nil {
			c.Organizer = Normalize(e.Organizer.Address)
		}
		for _, a := range e.Attendees {
			c.Attendees = append(c.Attendees, att{Normalize(a.Address), strings.ToUpper(a.Role), strings.ToUpper(a.CUType)})
		}
		cs = append(cs, c)
	}
	b, _ := json.Marshal(cs) // plain values: cannot fail
	return string(b)
}

func cancelled(v *ical.Object) bool {
	for _, e := range v.Components {
		if !strings.EqualFold(e.Status, "CANCELLED") {
			return false
		}
	}
	return true
}

// asCancel turns a view into the components of a CANCEL: every one
// cancelled, at a SEQUENCE above seq.
func asCancel(v *ical.Object, seq int) *ical.Object {
	for _, e := range v.Components {
		e.Status = "CANCELLED"
		if sequence(e) <= seq {
			n := seq + 1
			e.Sequence = &n
		}
	}
	return v
}

func stamp(v *ical.Object, now time.Time) *ical.Object {
	t := ical.DateTimeOf(now.UTC(), false)
	for _, e := range v.Components {
		e.DTStamp = t
	}
	return v
}

// Organize returns the messages an organizer's change sends: old is the
// object before it (nil when created) and new after (nil when deleted).
// self reports whether a normalised address is the organizer's, which is
// how the calendar user is told apart from the attendees. Attendees whose
// SCHEDULE-AGENT is not SERVER are left to the client.
//
// An attendee gets a REQUEST when they are new or what they are invited to
// changed in a way that matters (times, recurrence, text, status, who
// attends, SEQUENCE), or when SCHEDULE-FORCE-SEND=REQUEST asks; a CANCEL
// when they were removed, the event was deleted or everything they are
// invited to was cancelled.
func Organize(old, new *ical.Object, self func(string) bool, now time.Time) []Message {
	org := Organizer(new)
	if org == nil {
		org = Organizer(old)
	}
	if org == nil || !self(Normalize(org.Address)) {
		return nil
	}
	if new != nil && Organizer(new) != nil && !self(Normalize(Organizer(new).Address)) {
		// Handed to another organizer: theirs to schedule.
		return nil
	}
	oldSeq := 0
	if old != nil {
		oldSeq = sequence(old.Master())
		for _, e := range old.Components {
			oldSeq = max(oldSeq, sequence(e))
		}
	}
	var out []Message
	for _, addr := range union(attendees(old, self), attendees(new, self)) {
		a := firstAttendee(new, addr)
		if a == nil {
			a = firstAttendee(old, addr)
		}
		if !serverAgent(a.Params) {
			continue
		}
		force := strings.EqualFold(param(a.Params, paramForceSend), string(Request))
		ov, nv := view(old, addr), view(new, addr)
		if prev := firstAttendee(old, addr); prev != nil && !serverAgent(prev.Params) {
			ov = nil // the client scheduled them until now: new to the server
		}
		switch {
		case nv == nil && ov == nil:
		case nv == nil:
			out = append(out, Message{Cancel, addr, stamp(asCancel(ov, oldSeq), now)})
		case ov != nil && !force && significant(ov) == significant(nv):
		case cancelled(nv):
			out = append(out, Message{Cancel, addr, stamp(nv, now)})
		default:
			out = append(out, Message{Request, addr, stamp(nv, now)})
		}
	}
	return out
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// --- Attendee ---

// Respond returns the REPLY an attendee's change sends to the organizer, or
// nil: old is the attendee's copy before it (nil when created) and new
// after (nil when deleted). self reports whether a normalised address is
// the attendee's. A REPLY carries the occurrences whose PARTSTAT changed,
// or all of them when SCHEDULE-FORCE-SEND=REPLY is on the ORGANIZER;
// deleting the event declines it.
func Respond(old, new *ical.Object, self func(string) bool, now time.Time) *Message {
	cur := new
	if cur == nil {
		cur = old
	}
	org := Organizer(cur)
	if org == nil || self(Normalize(org.Address)) || !serverAgent(org.Params) {
		return nil
	}
	me := ""
	for _, e := range cur.Components {
		for _, a := range e.Attendees {
			if n := Normalize(a.Address); self(n) {
				me = n
				break
			}
		}
		if me != "" {
			break
		}
	}
	if me == "" {
		return nil
	}
	force := strings.EqualFold(param(org.Params, paramForceSend), string(Reply))
	z := ical.ZonesFor(cur, time.UTC)
	var comps []*ical.Component
	for _, e := range cur.Components {
		a := attendee(e, me)
		if a == nil {
			continue
		}
		if new == nil {
			comps = append(comps, replyComponent(e, a, "DECLINED", now))
			continue
		}
		before := "NEEDS-ACTION"
		if old != nil {
			prev := find(old, e, z)
			if prev == nil {
				prev = old.Master() // an occurrence the attendee answered on its own
			}
			if prev != nil {
				if pa := attendee(prev, me); pa != nil {
					before = pa.PartStat
				}
			}
		}
		if force || !strings.EqualFold(a.PartStat, before) {
			comps = append(comps, replyComponent(e, a, a.PartStat, now))
		}
	}
	if len(comps) == 0 {
		return nil
	}
	return &Message{Reply, Normalize(org.Address), &ical.Object{UID: cur.UID, Components: comps, Props: withoutMethod(cur.Props), Zones: cur.Zones}}
}

// replyComponent is what a REPLY says about one occurrence (RFC 5546
// §3.2.3): who answers, and enough of the event to recognise it.
func replyComponent(e *ical.Component, a *ical.Attendee, partstat string, now time.Time) *ical.Component {
	me := *a
	me.PartStat = partstat
	me.RSVP = ""
	me.Params = stripParams(a.Params)
	c := &ical.Component{
		Kind: e.Kind, RecurrenceID: e.RecurrenceID, ThisAndFuture: e.ThisAndFuture,
		Summary: e.Summary, Start: e.Start, End: e.End, Duration: e.Duration,
		DTStamp: ical.DateTimeOf(now.UTC(), false), Attendees: []ical.Attendee{me},
	}
	if e.Sequence != nil {
		n := *e.Sequence
		c.Sequence = &n
	}
	if e.Organizer != nil {
		org := *e.Organizer
		org.Params = stripParams(org.Params)
		c.Organizer = &org
	}
	return c
}

// --- Bookkeeping on the stored copy ---

// Clean prepares a client's copy for storing: SCHEDULE-FORCE-SEND is
// removed (it asks for one send, RFC 6638 §7.2), and SCHEDULE-STATUS is the
// server's to set, so a client's value gives way to what old held for the
// same address.
func Clean(old, new *ical.Object) {
	prev := func(addr string, organizer bool) string {
		if old == nil {
			return ""
		}
		for _, e := range old.Components {
			if organizer {
				if e.Organizer != nil && Normalize(e.Organizer.Address) == addr {
					return param(e.Organizer.Params, paramStatus)
				}
			} else if a := attendee(e, addr); a != nil {
				return param(a.Params, paramStatus)
			}
		}
		return ""
	}
	for _, e := range new.Components {
		for i := range e.Attendees {
			a := &e.Attendees[i]
			a.Params = setParam(setParam(a.Params, paramForceSend, ""), paramStatus, prev(Normalize(a.Address), false))
		}
		if o := e.Organizer; o != nil {
			o.Params = setParam(setParam(o.Params, paramForceSend, ""), paramStatus, prev(Normalize(o.Address), true))
		}
	}
}

// SetAttendeeStatus records the outcome of a message to attendee addr on
// every component listing them.
func SetAttendeeStatus(o *ical.Object, addr, status string) {
	for _, e := range o.Components {
		if a := attendee(e, addr); a != nil {
			a.Params = setParam(a.Params, paramStatus, status)
		}
	}
}

// SetOrganizerStatus records the outcome of a REPLY on the ORGANIZER.
func SetOrganizerStatus(o *ical.Object, status string) {
	for _, e := range o.Components {
		if e.Organizer != nil {
			e.Organizer.Params = setParam(e.Organizer.Params, paramStatus, status)
		}
	}
}

// --- Receiving ---

// Apply applies a message received to the recipient's copy of the object
// (nil when they have none), whose floating times are in floating. It
// returns the copy to store and whether it changed; a nil copy means there
// is nothing to store.
//
//   - REQUEST: a whole series replaces the copy, unless its SEQUENCE is
//     older; overrides alone replace or join the copy's. The recipient's own
//     alarms and transparency stay.
//   - CANCEL: of the whole event, every component is marked cancelled, so
//     the recipient sees it; of occurrences, they are excluded from the
//     series (from that one on for RANGE=THISANDFUTURE), or cancelled when
//     there is no series.
//   - REPLY: the sender's PARTSTAT is recorded on the occurrences they
//     answered, creating an override for one that had none.
func Apply(m Method, cur, msg *ical.Object, floating *time.Location) (*ical.Object, bool) {
	in := msg.Clone()
	in.Props = withoutMethod(in.Props)
	z := zones(cur, in, floating)
	switch m {
	case Request:
		return applyRequest(cur, in, z)
	case Cancel:
		return applyCancel(cur, in, z)
	case Reply:
		return applyReply(cur, in, z)
	}
	return cur, false
}

// zones reads the TZIDs of both objects: a message may use a client's own
// zone the copy does not.
func zones(cur, in *ical.Object, floating *time.Location) ical.Zones {
	z := ical.ZonesFor(in, floating)
	if cur != nil {
		for id, loc := range ical.ZonesFor(cur, floating).Custom {
			if z.Custom == nil {
				z.Custom = map[string]*time.Location{}
			}
			z.Custom[id] = loc
		}
	}
	return z
}

// personal carries what is the recipient's own from their component to the
// organizer's: alarms and transparency, which are the attendee's choices,
// not the organizer's.
func personal(from, to *ical.Component) {
	if from == nil {
		return
	}
	if len(to.Alarms) == 0 {
		to.Alarms = from.Alarms
	}
	if from.Transp != "" {
		to.Transp = from.Transp
	}
}

func applyRequest(cur, in *ical.Object, z ical.Zones) (*ical.Object, bool) {
	if cur == nil {
		return in, true
	}
	if m := in.Master(); m != nil {
		if cm := cur.Master(); cm != nil && sequence(m) < sequence(cm) {
			return cur, false // an older version arriving late
		}
		for _, e := range in.Components {
			personal(find(cur, e, z), e)
		}
		return in, true
	}
	out := cur.Clone()
	for _, e := range in.Components {
		if prev := find(out, e, z); prev != nil {
			if sequence(e) < sequence(prev) {
				continue
			}
			personal(prev, e)
			out.Components[slices.Index(out.Components, prev)] = e
		} else {
			personal(out.Master(), e)
			out.Components = append(out.Components, e)
		}
	}
	return out, true
}

func applyCancel(cur, in *ical.Object, z ical.Zones) (*ical.Object, bool) {
	if cur == nil {
		return nil, false
	}
	out := cur.Clone()
	cancelAll := func() (*ical.Object, bool) {
		for _, e := range out.Components {
			e.Status = "CANCELLED"
		}
		return out, true
	}
	if in.Master() != nil {
		return cancelAll()
	}
	for _, e := range in.Components {
		rid, err := e.RecurrenceID.Time(z)
		if err != nil {
			continue
		}
		m := out.Master()
		switch {
		case m != nil && m.Recurs() && e.ThisAndFuture:
			remains, err := out.Truncate(rid, z)
			if err == nil && !remains {
				return cancelAll()
			}
		case m != nil && m.Recurs():
			out.Exclude(rid, z)
		default:
			if c := find(out, e, z); c != nil {
				c.Status = "CANCELLED"
			}
		}
	}
	return out, true
}

func applyReply(cur, in *ical.Object, z ical.Zones) (*ical.Object, bool) {
	if cur == nil {
		return nil, false
	}
	out := cur.Clone()
	changed := false
	for _, e := range in.Components {
		for _, a := range e.Attendees {
			addr := Normalize(a.Address)
			target := find(out, e, z)
			if target == nil && e.IsOverride() {
				if m := out.Master(); m == nil || attendee(m, addr) == nil {
					continue
				}
				rid, err := e.RecurrenceID.Time(z)
				if err != nil {
					continue
				}
				if target, err = out.Override(rid, z); err != nil {
					continue // no such occurrence
				}
			}
			if target == nil {
				continue
			}
			t := attendee(target, addr)
			if t == nil {
				continue // not invited: Cog does not add party crashers
			}
			if !strings.EqualFold(t.PartStat, a.PartStat) {
				t.PartStat = a.PartStat
				changed = true
			}
		}
	}
	return out, changed
}
