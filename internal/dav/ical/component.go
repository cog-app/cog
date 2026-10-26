package ical

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// ProdID names Cog in objects it writes that did not come with one.
const ProdID = "-//Cog//Cog Calendar//EN"

// Kind is the type of a calendar component Cog models.
type Kind string

const (
	VEVENT   Kind = "VEVENT"
	VTODO    Kind = "VTODO"
	VJOURNAL Kind = "VJOURNAL"
)

func names(ns ...string) map[string]bool {
	m := make(map[string]bool, len(ns))
	for _, n := range ns {
		m[n] = true
	}
	return m
}

// common lists the properties every kind models.
var common = []string{
	"UID", "SUMMARY", "DESCRIPTION", "URL", "STATUS", "CLASS", "SEQUENCE", "DTSTART",
	"DTSTAMP", "CREATED", "LAST-MODIFIED", "RECURRENCE-ID", "RRULE", "RDATE", "EXDATE",
	"ORGANIZER", "ATTENDEE", "CATEGORIES",
}

// models lists the properties each kind claims, as RFC 5545 allows them in
// it; anything else, including a property another kind would claim, is a
// leftover. RELATED-TO is a leftover on events, and ATTACH on tasks and
// journals (DESIGN/dav-storage.md sections 4.2 and 4.3).
var models = map[Kind]map[string]bool{
	VEVENT:   names(append(common, "LOCATION", "TRANSP", "PRIORITY", "DTEND", "DURATION", "ATTACH")...),
	VTODO:    names(append(common, "LOCATION", "PRIORITY", "DUE", "DURATION", "COMPLETED", "PERCENT-COMPLETE", "RELATED-TO", "X-APPLE-SORT-ORDER")...),
	VJOURNAL: names(append(common, "RELATED-TO")...),
}

// hasAlarms reports whether a kind may hold VALARMs; in a VJOURNAL one is a
// leftover.
func (k Kind) hasAlarms() bool { return k == VEVENT || k == VTODO }

// Attendee is an ATTENDEE property.
type Attendee struct {
	Address  string              `json:"address"` // cal-address, usually mailto:
	CN       string              `json:"cn,omitempty"`
	PartStat string              `json:"partstat,omitempty"`
	Role     string              `json:"role,omitempty"`
	RSVP     string              `json:"rsvp,omitempty"`
	CUType   string              `json:"cutype,omitempty"`
	Params   []contentline.Param `json:"params,omitempty"` // others, in order
}

// Email returns the address without its mailto: scheme, or "" when it is
// not a mailto address.
func Email(address string) string {
	if len(address) > 7 && strings.EqualFold(address[:7], "mailto:") {
		return address[7:]
	}
	return ""
}

// Organizer is an ORGANIZER property.
type Organizer struct {
	Address string              `json:"address"`
	CN      string              `json:"cn,omitempty"`
	Params  []contentline.Param `json:"params,omitempty"`
}

// Relation is a RELATED-TO property: another component, by UID, and how it
// relates to this one.
type Relation struct {
	// Type is RELTYPE as written; "" (absent) means PARENT.
	Type   string              `json:"type,omitempty"`
	UID    string              `json:"uid"`
	Params []contentline.Param `json:"params,omitempty"` // others, in order
}

// IsParent reports whether the relation names the component's parent.
func (r *Relation) IsParent() bool { return r.Type == "" || strings.EqualFold(r.Type, "PARENT") }

// Attachment is an ATTACH property: a URI, or inline data kept as the base64
// text that was sent (VALUE=BINARY with ENCODING=BASE64). A managed
// attachment (RFC 8607) is a URI Cog serves, named by its MANAGED-ID.
type Attachment struct {
	Value     string              `json:"value"` // the URI, or the base64 text when Inline
	Inline    bool                `json:"inline,omitempty"`
	ManagedID string              `json:"managed_id,omitempty"`
	FmtType   string              `json:"fmttype,omitempty"`
	Size      *int64              `json:"size,omitempty"` // SIZE, in octets
	Filename  string              `json:"filename,omitempty"`
	Params    []contentline.Param `json:"params,omitempty"` // others, in order
}

// Alarm is a VALARM component.
type Alarm struct {
	Action string
	// Trigger is a duration, or a UTC date-time when Absolute.
	Trigger        string
	Related        string // RELATED as given: "", START or END
	Absolute       bool
	Repeat         *int
	RepeatDuration string // DURATION
	Description    string
	Summary        string
	Leftovers      codec.Leftovers
}

// DateValue is one value of an RDATE or EXDATE property.
type DateValue struct {
	DateTime
	// Period is the rest of an RDATE PERIOD value, "/" and its end or
	// duration.
	Period string              `json:"p,omitempty"`
	Params []contentline.Param `json:"params,omitempty"` // others on the property
}

// Component is one VEVENT, VTODO or VJOURNAL: a recurring series' master,
// one of its overrides, or a single component. Fields a kind does not model
// stay empty; the codec leaves such properties among the leftovers.
type Component struct {
	Kind Kind

	RecurrenceID  DateTime
	ThisAndFuture bool // RANGE=THISANDFUTURE

	Summary     string
	Description string
	Location    string
	URL         string
	Status      string
	Class       string
	Transp      string
	Priority    *int
	Sequence    *int

	Start    DateTime
	End      DateTime // DTEND (events)
	Due      DateTime // DUE (tasks)
	Duration string

	// Tasks only: COMPLETED, PERCENT-COMPLETE, and Apple's
	// X-APPLE-SORT-ORDER, which orders a list's tasks.
	Completed       DateTime
	PercentComplete *int
	SortOrder       *int

	RRule   string
	RDates  []DateValue
	ExDates []DateValue

	DTStamp      DateTime
	Created      DateTime
	LastModified DateTime

	Organizer  *Organizer
	Attendees  []Attendee
	Categories []string
	Relations  []Relation // tasks and journals
	// Attachments holds ATTACH (events).
	Attachments []Attachment
	Alarms      []Alarm

	Leftovers codec.Leftovers
}

// IsOverride reports whether the component overrides one occurrence.
func (e *Component) IsOverride() bool { return !e.RecurrenceID.IsZero() }

// Parent returns the UID of the component's parent, from its first
// RELATED-TO with RELTYPE=PARENT (or none), or "".
func (e *Component) Parent() string {
	for i := range e.Relations {
		if e.Relations[i].IsParent() {
			return e.Relations[i].UID
		}
	}
	return ""
}

// SetParent makes uid the component's parent, replacing the UID of its first
// parent relation (and keeping that relation's parameters), adding one, or,
// for "", removing every parent relation. It reports whether anything
// changed.
func (e *Component) SetParent(uid string) bool {
	if uid == "" {
		n := len(e.Relations)
		e.Relations = slices.DeleteFunc(e.Relations, func(r Relation) bool { return r.IsParent() })
		return len(e.Relations) != n
	}
	for i := range e.Relations {
		if r := &e.Relations[i]; r.IsParent() {
			if r.UID == uid {
				return false
			}
			r.UID = uid
			return true
		}
	}
	e.Relations = append(e.Relations, Relation{Type: "PARENT", UID: uid})
	return true
}

// Object is one calendar object resource: one UID, its components of one
// kind, and what the VCALENDAR around them carried.
type Object struct {
	UID        string
	Components []*Component
	// Props holds the VCALENDAR's properties other than VERSION, in order.
	Props []contentline.Property
	// Others holds components that are neither of the object's kind nor time
	// zones.
	Others []*contentline.Component
	// Zones holds the client's own VTIMEZONEs, by TZID, for TZIDs Cog cannot
	// resolve to a zone it knows. Others are regenerated on output.
	Zones map[string]*contentline.Component
}

// Master returns the component that is not an override, or nil.
func (o *Object) Master() *Component {
	for _, e := range o.Components {
		if !e.IsOverride() {
			return e
		}
	}
	return nil
}

// SortMaster moves the master to the front, keeping the overrides' order:
// stores read the first component as the one the web UI shows.
func (o *Object) SortMaster() {
	slices.SortStableFunc(o.Components, func(a, b *Component) int {
		switch {
		case a.IsOverride() == b.IsOverride():
			return 0
		case !a.IsOverride():
			return -1
		}
		return 1
	})
}

// ErrNoComponent is returned for a VCALENDAR without a component of the kind
// asked for.
var ErrNoComponent = errors.New("ical: no component of the resource's kind")

// Decode reads the components of one kind in a resource. TZIDs that name a
// zone Cog knows (directly, by a Windows name, by X-LIC-LOCATION, or by a
// path suffix) are stored as its IANA name; others keep their TZID and their
// VTIMEZONE. Components of other kinds are kept in Others.
func Decode(cal *contentline.Component, kind Kind) (*Object, error) {
	o := &Object{}
	vtzs := map[string]*contentline.Component{}
	aliases := map[string]string{}
	for _, sub := range cal.Components {
		if !sub.Is("VTIMEZONE") {
			continue
		}
		if id := sub.Prop("TZID"); id != nil {
			vtzs[id.Value] = sub
			if loc := sub.Prop("X-LIC-LOCATION"); loc != nil {
				aliases[id.Value] = loc.Value
			}
		}
	}
	resolved := map[string]string{}
	tzid := func(id string) string {
		id = strings.Trim(id, `"`)
		if name, ok := resolved[id]; ok {
			return name
		}
		name := id
		if loc := tz.Resolve(id, aliases); loc != nil {
			name = loc.String()
		} else if vtz, ok := vtzs[id]; ok {
			if o.Zones == nil {
				o.Zones = map[string]*contentline.Component{}
			}
			o.Zones[id] = vtz
		}
		resolved[id] = name
		return name
	}
	for i := range cal.Props {
		if !cal.Props[i].Is("VERSION") {
			o.Props = append(o.Props, cal.Props[i])
		}
	}
	for _, sub := range cal.Components {
		switch {
		case sub.Is("VTIMEZONE"):
		case sub.Is(string(kind)):
			if uid := sub.Prop("UID"); uid != nil && o.UID == "" {
				o.UID = uid.Value
			}
			o.Components = append(o.Components, decodeComponent(sub, kind, tzid))
		default:
			o.Others = append(o.Others, sub)
		}
	}
	if len(o.Components) == 0 {
		return nil, ErrNoComponent
	}
	return o, nil
}

// claimer tracks which single-valued properties a component has claimed, so
// a second one stays a leftover.
type claimer map[string]bool

func (c claimer) once(name string) bool {
	if c[name] {
		return false
	}
	c[name] = true
	return true
}

func decodeComponent(c *contentline.Component, kind Kind, tzid func(string) string) *Component {
	e := &Component{Kind: kind}
	l := &e.Leftovers
	claimed := claimer{}
	for i := range c.Props {
		p := &c.Props[i]
		if p.Malformed() || p.Group != "" || !models[kind][strings.ToUpper(p.Name)] || !claimProp(e, p, claimed, tzid) {
			l.Props = append(l.Props, *p)
		}
	}
	for _, sub := range c.Components {
		if sub.Is("VALARM") && kind.hasAlarms() {
			e.Alarms = append(e.Alarms, decodeAlarm(sub))
			continue
		}
		l.Comps = append(l.Comps, sub)
	}
	return e
}

// claimProp moves a property into e's fields and reports whether it did.
func claimProp(e *Component, p *contentline.Property, claimed claimer, tzid func(string) string) bool {
	name := strings.ToUpper(p.Name)
	l := &e.Leftovers
	text := func(field *string) bool {
		if p.Value == "" || !claimed.once(name) {
			return false
		}
		*field = codec.UnescapeText(p.Value)
		l.KeepParams(name, p.Params)
		return true
	}
	raw := func(field *string) bool {
		if p.Value == "" || !claimed.once(name) {
			return false
		}
		*field = p.Value
		l.KeepParams(name, p.Params)
		return true
	}
	date := func(field *DateTime, extra ...string) bool {
		if claimed[name] {
			return false
		}
		d, err := parseValue(p.Value, p.Param("VALUE"), p.Param("TZID"), tzid)
		if err != nil {
			return false
		}
		claimed[name] = true
		*field = d
		l.KeepParams(name, p.Params, append(extra, "VALUE", "TZID")...)
		return true
	}
	number := func(field **int) bool {
		n, err := strconv.Atoi(strings.TrimSpace(p.Value))
		if err != nil || !claimed.once(name) {
			return false
		}
		*field = &n
		l.KeepParams(name, p.Params)
		return true
	}
	switch name {
	case "UID":
		// The resource's UID; written on every component from Object.UID.
		if !claimed.once(name) {
			return false
		}
		l.KeepParams(name, p.Params)
		return true
	case "SUMMARY":
		return text(&e.Summary)
	case "DESCRIPTION":
		return text(&e.Description)
	case "LOCATION":
		return text(&e.Location)
	case "URL":
		return raw(&e.URL)
	case "STATUS":
		return raw(&e.Status)
	case "CLASS":
		return raw(&e.Class)
	case "TRANSP":
		return raw(&e.Transp)
	case "RRULE":
		return raw(&e.RRule)
	case "DURATION":
		if _, err := ParseDuration(p.Value); err != nil {
			return false
		}
		return raw(&e.Duration)
	case "PRIORITY":
		return number(&e.Priority)
	case "SEQUENCE":
		return number(&e.Sequence)
	case "PERCENT-COMPLETE":
		return number(&e.PercentComplete)
	case "X-APPLE-SORT-ORDER":
		return number(&e.SortOrder)
	case "DTSTART":
		return date(&e.Start)
	case "DTEND":
		return date(&e.End)
	case "DUE":
		return date(&e.Due)
	case "COMPLETED":
		return date(&e.Completed)
	case "DTSTAMP":
		return date(&e.DTStamp)
	case "CREATED":
		return date(&e.Created)
	case "LAST-MODIFIED":
		return date(&e.LastModified)
	case "RECURRENCE-ID":
		if !date(&e.RecurrenceID, "RANGE") {
			return false
		}
		e.ThisAndFuture = strings.EqualFold(p.Param("RANGE"), "THISANDFUTURE")
		return true
	case "RDATE", "EXDATE":
		vals, ok := dateList(p, tzid, name == "RDATE")
		if !ok {
			return false
		}
		if name == "RDATE" {
			e.RDates = append(e.RDates, vals...)
		} else {
			e.ExDates = append(e.ExDates, vals...)
		}
		return true
	case "ORGANIZER":
		if p.Value == "" || !claimed.once(name) {
			return false
		}
		m, other := codec.TakeParams(p.Params, "CN")
		e.Organizer = &Organizer{Address: p.Value, CN: m["CN"], Params: other}
		return true
	case "ATTENDEE":
		if p.Value == "" {
			return false
		}
		m, other := codec.TakeParams(p.Params, "CN", "PARTSTAT", "ROLE", "RSVP", "CUTYPE")
		e.Attendees = append(e.Attendees, Attendee{
			Address: p.Value, CN: m["CN"], PartStat: m["PARTSTAT"], Role: m["ROLE"],
			RSVP: m["RSVP"], CUType: m["CUTYPE"], Params: other,
		})
		return true
	case "RELATED-TO":
		if p.Value == "" {
			return false
		}
		m, other := codec.TakeParams(p.Params, "RELTYPE")
		e.Relations = append(e.Relations, Relation{Type: m["RELTYPE"], UID: p.Value, Params: other})
		return true
	case "ATTACH":
		a, ok := decodeAttachment(p)
		if ok {
			e.Attachments = append(e.Attachments, a)
		}
		return ok
	case "CATEGORIES":
		if len(p.Params) > 0 || p.Value == "" {
			return false
		}
		e.Categories = append(e.Categories, codec.SplitText(p.Value)...)
		return true
	}
	return false
}

// decodeAttachment reads an ATTACH property, or reports false when it does
// not fit the model: no value, a SIZE that is not a number, or inline data
// other than base64 (RFC 5545 allows no other encoding).
func decodeAttachment(p *contentline.Property) (Attachment, bool) {
	if p.Value == "" {
		return Attachment{}, false
	}
	m, other := codec.TakeParams(p.Params, "VALUE", "ENCODING", "FMTTYPE", "MANAGED-ID", "SIZE", "FILENAME")
	a := Attachment{Value: p.Value, ManagedID: m["MANAGED-ID"], FmtType: m["FMTTYPE"], Filename: m["FILENAME"]}
	value, hasValue := m["VALUE"]
	enc, hasEnc := m["ENCODING"]
	switch {
	case strings.EqualFold(value, "BINARY") || hasEnc:
		if !strings.EqualFold(value, "BINARY") || !strings.EqualFold(enc, "BASE64") || a.ManagedID != "" {
			return Attachment{}, false
		}
		a.Inline = true
	case hasValue:
		// VALUE=URI, the default, written as sent.
		other = append([]contentline.Param{{Name: "VALUE", Values: []string{value}}}, other...)
	}
	if s, ok := m["SIZE"]; ok {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			return Attachment{}, false
		}
		a.Size = &n
	}
	a.Params = other
	return a, true
}

// property writes the attachment as an ATTACH property.
func (a *Attachment) property() contentline.Property {
	var params []contentline.Param
	add := func(name, v string) {
		if v != "" {
			params = append(params, contentline.Param{Name: name, Values: []string{v}})
		}
	}
	if a.Inline {
		add("VALUE", "BINARY")
		add("ENCODING", "BASE64")
	}
	add("FMTTYPE", a.FmtType)
	add("MANAGED-ID", a.ManagedID)
	if a.Size != nil {
		add("SIZE", strconv.FormatInt(*a.Size, 10))
	}
	add("FILENAME", a.Filename)
	return contentline.Property{Name: "ATTACH", Params: append(params, a.Params...), Value: a.Value}
}

// dateList reads an RDATE or EXDATE property, or reports false when a value
// cannot be read.
func dateList(p *contentline.Property, tzid func(string) string, periods bool) ([]DateValue, bool) {
	valueParam := p.Param("VALUE")
	isPeriod := strings.EqualFold(valueParam, "PERIOD")
	if isPeriod && !periods {
		return nil, false
	}
	_, other := codec.TakeParams(p.Params, "VALUE", "TZID")
	var out []DateValue
	for v := range strings.SplitSeq(p.Value, ",") {
		var rest string
		if isPeriod {
			var ok bool
			if v, rest, ok = strings.Cut(v, "/"); !ok {
				return nil, false
			}
			rest = "/" + rest
			valueParam = ""
		}
		d, err := parseValue(v, valueParam, p.Param("TZID"), tzid)
		if err != nil {
			return nil, false
		}
		out = append(out, DateValue{DateTime: d, Period: rest, Params: other})
	}
	return out, len(out) > 0
}

func decodeAlarm(c *contentline.Component) Alarm {
	a := Alarm{}
	l := &a.Leftovers
	claimed := claimer{}
	for i := range c.Props {
		p := &c.Props[i]
		name := strings.ToUpper(p.Name)
		ok := false
		if !p.Malformed() && p.Group == "" && p.Value != "" && !claimed[name] {
			ok = true
			switch name {
			case "ACTION":
				a.Action = p.Value
				l.KeepParams(name, p.Params)
			case "TRIGGER":
				a.Absolute = strings.EqualFold(p.Param("VALUE"), "DATE-TIME")
				if !a.Absolute {
					if _, err := ParseDuration(p.Value); err != nil {
						ok = false
						break
					}
				}
				a.Trigger = p.Value
				a.Related = p.Param("RELATED")
				l.KeepParams(name, p.Params, "VALUE", "RELATED")
			case "REPEAT":
				n, err := strconv.Atoi(p.Value)
				if err != nil {
					ok = false
					break
				}
				a.Repeat = &n
				l.KeepParams(name, p.Params)
			case "DURATION":
				a.RepeatDuration = p.Value
				l.KeepParams(name, p.Params)
			case "DESCRIPTION":
				a.Description = codec.UnescapeText(p.Value)
				l.KeepParams(name, p.Params)
			case "SUMMARY":
				a.Summary = codec.UnescapeText(p.Value)
				l.KeepParams(name, p.Params)
			default:
				ok = false
			}
		}
		if ok {
			claimed[name] = true
		} else {
			l.Props = append(l.Props, *p)
		}
	}
	l.Comps = append(l.Comps, c.Components...)
	return a
}

// ZoneSource returns the VTIMEZONE to write for a TZID, starting at year
// from, or nil when there is none to write.
type ZoneSource func(tzid string, from int) *contentline.Component

// IANAZones writes generated VTIMEZONEs for the zones Cog knows and the
// client's own for the rest.
func (o *Object) IANAZones(tzid string, from int) *contentline.Component {
	if vtz, ok := o.Zones[tzid]; ok {
		return vtz
	}
	if loc := tz.Load(tzid); loc != nil {
		return tz.VTimezone(loc, from)
	}
	return nil
}

// Encode writes the object as a VCALENDAR: VERSION, the stored calendar
// properties (PRODID first when there is none), a VTIMEZONE for every TZID
// used, then the components in order. zones may be nil to write no
// VTIMEZONEs.
func (o *Object) Encode(zones ZoneSource) *contentline.Component {
	cal := &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{{Name: "VERSION", Value: "2.0"}}}
	if !slices.ContainsFunc(o.Props, func(p contentline.Property) bool { return p.Is("PRODID") }) {
		cal.Props = append(cal.Props, contentline.Property{Name: "PRODID", Value: ProdID})
	}
	cal.Props = append(cal.Props, o.Props...)
	if zones != nil {
		used := o.tzids()
		ids := make([]string, 0, len(used))
		for id := range used {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			if vtz := zones(id, used[id]); vtz != nil {
				cal.Components = append(cal.Components, vtz)
			}
		}
	}
	for _, e := range o.Components {
		cal.Components = append(cal.Components, e.encode(o.UID))
	}
	cal.Components = append(cal.Components, o.Others...)
	return cal
}

// tzids returns every TZID the components use other than UTC, with the
// earliest year each is used in.
func (o *Object) tzids() map[string]int {
	used := map[string]int{}
	note := func(d DateTime) {
		if d.IsZero() || d.IsDate || d.TZID == "" || d.TZID == "UTC" {
			return
		}
		y := d.Year()
		if cur, ok := used[d.TZID]; !ok || y < cur {
			used[d.TZID] = y
		}
	}
	for _, e := range o.Components {
		note(e.Start)
		note(e.End)
		note(e.Due)
		note(e.Completed)
		note(e.RecurrenceID)
		for _, d := range e.RDates {
			note(d.DateTime)
		}
		for _, d := range e.ExDates {
			note(d.DateTime)
		}
	}
	return used
}

func (e *Component) encode(uid string) *contentline.Component {
	c := &contentline.Component{Name: string(e.Kind)}
	l := &e.Leftovers
	add := func(name, value string, params ...contentline.Param) {
		c.Props = append(c.Props, contentline.Property{Name: name, Params: append(params, l.Params[name]...), Value: value})
	}
	text := func(name, v string) {
		if v != "" {
			add(name, codec.EscapeText(v))
		}
	}
	raw := func(name, v string) {
		if v != "" {
			add(name, v)
		}
	}
	date := func(name string, d DateTime, extra ...contentline.Param) {
		if !d.IsZero() {
			add(name, d.value(), append(d.params(), extra...)...)
		}
	}
	number := func(name string, n *int) {
		if n != nil {
			add(name, strconv.Itoa(*n))
		}
	}

	add("UID", uid)
	if e.ThisAndFuture {
		date("RECURRENCE-ID", e.RecurrenceID, contentline.Param{Name: "RANGE", Values: []string{"THISANDFUTURE"}})
	} else {
		date("RECURRENCE-ID", e.RecurrenceID)
	}
	date("DTSTAMP", e.DTStamp)
	date("CREATED", e.Created)
	date("LAST-MODIFIED", e.LastModified)
	number("SEQUENCE", e.Sequence)
	text("SUMMARY", e.Summary)
	text("DESCRIPTION", e.Description)
	text("LOCATION", e.Location)
	raw("URL", e.URL)
	raw("STATUS", e.Status)
	raw("CLASS", e.Class)
	raw("TRANSP", e.Transp)
	number("PRIORITY", e.Priority)
	number("PERCENT-COMPLETE", e.PercentComplete)
	date("DTSTART", e.Start)
	date("DTEND", e.End)
	date("DUE", e.Due)
	raw("DURATION", e.Duration)
	date("COMPLETED", e.Completed)
	raw("RRULE", e.RRule)
	c.Props = appendDateList(c.Props, "RDATE", e.RDates)
	c.Props = appendDateList(c.Props, "EXDATE", e.ExDates)
	if o := e.Organizer; o != nil {
		var params []contentline.Param
		if o.CN != "" {
			params = append(params, contentline.Param{Name: "CN", Values: []string{o.CN}})
		}
		c.Props = append(c.Props, contentline.Property{Name: "ORGANIZER", Params: append(params, o.Params...), Value: o.Address})
	}
	for _, a := range e.Attendees {
		c.Props = append(c.Props, a.property())
	}
	for i := range e.Attachments {
		c.Props = append(c.Props, e.Attachments[i].property())
	}
	if len(e.Categories) > 0 {
		vals := make([]string, len(e.Categories))
		for i, v := range e.Categories {
			vals[i] = codec.EscapeText(v)
		}
		c.Props = append(c.Props, contentline.Property{Name: "CATEGORIES", Value: strings.Join(vals, ",")})
	}
	for _, r := range e.Relations {
		var params []contentline.Param
		if r.Type != "" {
			params = append(params, contentline.Param{Name: "RELTYPE", Values: []string{r.Type}})
		}
		c.Props = append(c.Props, contentline.Property{Name: "RELATED-TO", Params: append(params, r.Params...), Value: r.UID})
	}
	number("X-APPLE-SORT-ORDER", e.SortOrder)
	c.Props = append(c.Props, l.Props...)
	for i := range e.Alarms {
		c.Components = append(c.Components, e.Alarms[i].encode())
	}
	c.Components = append(c.Components, l.Comps...)
	return c
}

func (a *Attendee) property() contentline.Property {
	var params []contentline.Param
	add := func(name, v string) {
		if v != "" {
			params = append(params, contentline.Param{Name: name, Values: []string{v}})
		}
	}
	add("CUTYPE", a.CUType)
	add("ROLE", a.Role)
	add("PARTSTAT", a.PartStat)
	add("RSVP", a.RSVP)
	add("CN", a.CN)
	return contentline.Property{Name: "ATTENDEE", Params: append(params, a.Params...), Value: a.Address}
}

// appendDateList writes RDATE or EXDATE values, one property per run of
// values that share a zone, value type and parameters.
func appendDateList(props []contentline.Property, name string, vals []DateValue) []contentline.Property {
	for i := 0; i < len(vals); {
		j := i + 1
		for j < len(vals) && vals[j].sameKind(vals[i].DateTime) && (vals[j].Period == "") == (vals[i].Period == "") &&
			slices.EqualFunc(vals[j].Params, vals[i].Params, codec.ParamEqual) {
			j++
		}
		strs := make([]string, 0, j-i)
		for _, v := range vals[i:j] {
			strs = append(strs, v.value()+v.Period)
		}
		params := vals[i].params()
		if vals[i].Period != "" {
			params = slices.DeleteFunc(params, func(p contentline.Param) bool { return p.Name == "VALUE" })
			params = append([]contentline.Param{{Name: "VALUE", Values: []string{"PERIOD"}}}, params...)
		}
		props = append(props, contentline.Property{Name: name, Params: append(params, vals[i].Params...), Value: strings.Join(strs, ",")})
		i = j
	}
	return props
}

func (a *Alarm) encode() *contentline.Component {
	c := &contentline.Component{Name: "VALARM"}
	l := &a.Leftovers
	add := func(name, value string, params ...contentline.Param) {
		if value != "" {
			c.Props = append(c.Props, contentline.Property{Name: name, Params: append(params, l.Params[name]...), Value: value})
		}
	}
	add("ACTION", a.Action)
	var tp []contentline.Param
	if a.Related != "" {
		tp = append(tp, contentline.Param{Name: "RELATED", Values: []string{a.Related}})
	}
	if a.Absolute {
		tp = append(tp, contentline.Param{Name: "VALUE", Values: []string{"DATE-TIME"}})
	}
	add("TRIGGER", a.Trigger, tp...)
	if a.Repeat != nil {
		add("REPEAT", strconv.Itoa(*a.Repeat))
	}
	add("DURATION", a.RepeatDuration)
	add("DESCRIPTION", codec.EscapeText(a.Description))
	add("SUMMARY", codec.EscapeText(a.Summary))
	c.Props = append(c.Props, l.Props...)
	c.Components = append(c.Components, l.Comps...)
	return c
}

// Dependent leftovers: vendor copies of a modelled field, which become wrong
// when Cog edits that field (DESIGN/dav-storage.md section 3.2). A DAV PUT
// replaces the whole component and does not need these. No client in the
// corpus shadows a task's STATUS, COMPLETED or PERCENT-COMPLETE.
var dependents = map[string][]string{
	"SUMMARY":     nil,
	"DESCRIPTION": {"X-ALT-DESC"},
	"LOCATION":    {"X-APPLE-STRUCTURED-LOCATION"},
	"TIME": {
		"X-MICROSOFT-CDO-ALLDAYEVENT", "X-FUNAMBOL-ALLDAY", "X-MOZ-GENERATION",
		"X-MOZ-LASTACK", "X-MOZ-SNOOZE-TIME*", "X-NSCP-ORIGINAL-DTSTART", "X-NSCP-DTSTART-TZID",
	},
	"TRIGGER": {"X-MOZ-LASTACK", "X-MOZ-SNOOZE-TIME", "X-APPLE-DEFAULT-ALARM", "ACKNOWLEDGED"},
}

// Changed drops the leftovers that copy a field Cog changed. field is a
// property name, or TIME for any of DTSTART, DTEND, DUE, DURATION, RRULE and
// all-day.
func (e *Component) Changed(fields ...string) {
	for _, f := range fields {
		e.Leftovers.Drop(dependents[f]...)
	}
}

// AlarmChanged drops the leftovers that refer to an alarm's old trigger.
func (a *Alarm) AlarmChanged() { a.Leftovers.Drop(dependents["TRIGGER"]...) }

// Stamp records an edit made by Cog as RFC 5545 asks: DTSTAMP and
// LAST-MODIFIED always, and SEQUENCE when a field that matters to scheduling
// changed, so clients and invitees take the new version.
func (e *Component) Stamp(now time.Time, scheduling bool) {
	t := DateTimeOf(now.UTC(), false)
	e.DTStamp, e.LastModified = t, t
	if scheduling {
		seq := 1
		if e.Sequence != nil {
			seq = *e.Sequence + 1
		}
		e.Sequence = &seq
	}
}

// Busy returns a copy that shows only when the owner is busy, for a
// free/busy-only share: times, recurrence, status and transparency, with
// "Busy" as the summary.
func (o *Object) Busy() *Object {
	b := &Object{UID: o.UID, Zones: o.Zones}
	strip := func(vals []DateValue) []DateValue {
		out := make([]DateValue, len(vals))
		for i, v := range vals {
			out[i] = DateValue{DateTime: v.DateTime, Period: v.Period}
		}
		return out
	}
	for _, e := range o.Components {
		b.Components = append(b.Components, &Component{
			Kind: e.Kind, RecurrenceID: e.RecurrenceID, ThisAndFuture: e.ThisAndFuture, Summary: "Busy",
			Status: e.Status, Transp: e.Transp, Start: e.Start, End: e.End, Due: e.Due, Duration: e.Duration,
			RRule: e.RRule, RDates: strip(e.RDates), ExDates: strip(e.ExDates), DTStamp: e.DTStamp,
		})
	}
	return b
}
