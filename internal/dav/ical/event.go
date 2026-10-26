package ical

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// ProdID names Cog in objects it writes that did not come with one.
const ProdID = "-//Cog//Cog Calendar//EN"

// Leftovers is what a codec did not model in one component, kept so it can
// be written back.
type Leftovers struct {
	Props []contentline.Property   `json:"props,omitempty"` // in received order
	Comps []*contentline.Component `json:"comps,omitempty"` // in received order
	// Params holds parameters Cog does not model on properties it does,
	// keyed by the property's upper-case name: SUMMARY;LANGUAGE=de.
	Params map[string][]contentline.Param `json:"params,omitempty"`
}

// IsZero reports whether nothing was left over.
func (l *Leftovers) IsZero() bool {
	return len(l.Props) == 0 && len(l.Comps) == 0 && len(l.Params) == 0
}

func (l *Leftovers) keepParams(name string, params []contentline.Param, modelled ...string) {
	var unknown []contentline.Param
	for _, p := range params {
		if p.Name != "" && slices.ContainsFunc(modelled, func(m string) bool { return strings.EqualFold(m, p.Name) }) {
			continue
		}
		unknown = append(unknown, p)
	}
	if len(unknown) == 0 {
		return
	}
	if l.Params == nil {
		l.Params = map[string][]contentline.Param{}
	}
	l.Params[name] = unknown
}

// Drop removes leftover properties by name. A name ending in "*" is a prefix.
func (l *Leftovers) Drop(names ...string) {
	l.Props = slices.DeleteFunc(l.Props, func(p contentline.Property) bool {
		for _, n := range names {
			if prefix, ok := strings.CutSuffix(n, "*"); ok {
				if len(p.Name) >= len(prefix) && strings.EqualFold(p.Name[:len(prefix)], prefix) {
					return true
				}
			} else if p.Is(n) {
				return true
			}
		}
		return false
	})
}

// Has reports whether a leftover property has the name.
func (l *Leftovers) Has(name string) bool {
	return slices.ContainsFunc(l.Props, func(p contentline.Property) bool { return p.Is(name) })
}

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
	Leftovers      Leftovers
}

// DateValue is one value of an RDATE or EXDATE property.
type DateValue struct {
	DateTime
	// Period is the rest of an RDATE PERIOD value, "/" and its end or
	// duration.
	Period string              `json:"p,omitempty"`
	Params []contentline.Param `json:"params,omitempty"` // others on the property
}

// Event is one VEVENT component: a recurring event's master, one of its
// overrides, or a single event.
type Event struct {
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
	End      DateTime
	Duration string

	RRule   string
	RDates  []DateValue
	ExDates []DateValue

	DTStamp      DateTime
	Created      DateTime
	LastModified DateTime

	Organizer  *Organizer
	Attendees  []Attendee
	Categories []string
	Alarms     []Alarm

	Leftovers Leftovers
}

// IsOverride reports whether the component overrides one occurrence.
func (e *Event) IsOverride() bool { return !e.RecurrenceID.IsZero() }

// Object is one calendar object resource holding events: one UID, its
// components, and what the VCALENDAR around them carried.
type Object struct {
	UID    string
	Events []*Event
	// Props holds the VCALENDAR's properties other than VERSION, in order.
	Props []contentline.Property
	// Comps holds components that are neither events nor time zones.
	Comps []*contentline.Component
	// Zones holds the client's own VTIMEZONEs, by TZID, for TZIDs Cog cannot
	// resolve to a zone it knows. Others are regenerated on output.
	Zones map[string]*contentline.Component
}

// Master returns the component that is not an override, or nil.
func (o *Object) Master() *Event {
	for _, e := range o.Events {
		if !e.IsOverride() {
			return e
		}
	}
	return nil
}

// ErrNoEvent is returned for a VCALENDAR without a VEVENT.
var ErrNoEvent = errors.New("ical: no VEVENT")

// DecodeEvents reads the events of one resource. TZIDs that name a zone Cog
// knows (directly, by a Windows name, by X-LIC-LOCATION, or by a path
// suffix) are stored as its IANA name; others keep their TZID and their
// VTIMEZONE.
func DecodeEvents(cal *contentline.Component) (*Object, error) {
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
		case sub.Is("VEVENT"):
			if uid := sub.Prop("UID"); uid != nil && o.UID == "" {
				o.UID = uid.Value
			}
			o.Events = append(o.Events, decodeEvent(sub, tzid))
		default:
			o.Comps = append(o.Comps, sub)
		}
	}
	if len(o.Events) == 0 {
		return nil, ErrNoEvent
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

func decodeEvent(c *contentline.Component, tzid func(string) string) *Event {
	e := &Event{}
	l := &e.Leftovers
	claimed := claimer{}
	for i := range c.Props {
		p := &c.Props[i]
		if p.Malformed() || p.Group != "" || !claimProp(e, p, claimed, tzid) {
			l.Props = append(l.Props, *p)
		}
	}
	for _, sub := range c.Components {
		if sub.Is("VALARM") {
			e.Alarms = append(e.Alarms, decodeAlarm(sub))
			continue
		}
		l.Comps = append(l.Comps, sub)
	}
	return e
}

// claimProp moves a property into e's fields and reports whether it did.
func claimProp(e *Event, p *contentline.Property, claimed claimer, tzid func(string) string) bool {
	name := strings.ToUpper(p.Name)
	l := &e.Leftovers
	text := func(field *string) bool {
		if p.Value == "" || !claimed.once(name) {
			return false
		}
		*field = UnescapeText(p.Value)
		l.keepParams(name, p.Params)
		return true
	}
	raw := func(field *string) bool {
		if p.Value == "" || !claimed.once(name) {
			return false
		}
		*field = p.Value
		l.keepParams(name, p.Params)
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
		l.keepParams(name, p.Params, append(extra, "VALUE", "TZID")...)
		return true
	}
	number := func(field **int) bool {
		n, err := strconv.Atoi(strings.TrimSpace(p.Value))
		if err != nil || !claimed.once(name) {
			return false
		}
		*field = &n
		l.keepParams(name, p.Params)
		return true
	}
	switch name {
	case "UID":
		// The resource's UID; written on every component from Object.UID.
		if !claimed.once(name) {
			return false
		}
		l.keepParams(name, p.Params)
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
	case "DTSTART":
		return date(&e.Start)
	case "DTEND":
		return date(&e.End)
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
		m, other := takeParams(p.Params, "CN")
		e.Organizer = &Organizer{Address: p.Value, CN: m["CN"], Params: other}
		return true
	case "ATTENDEE":
		if p.Value == "" {
			return false
		}
		m, other := takeParams(p.Params, "CN", "PARTSTAT", "ROLE", "RSVP", "CUTYPE")
		e.Attendees = append(e.Attendees, Attendee{
			Address: p.Value, CN: m["CN"], PartStat: m["PARTSTAT"], Role: m["ROLE"],
			RSVP: m["RSVP"], CUType: m["CUTYPE"], Params: other,
		})
		return true
	case "CATEGORIES":
		if len(p.Params) > 0 || p.Value == "" {
			return false
		}
		e.Categories = append(e.Categories, splitText(p.Value)...)
		return true
	}
	return false
}

// takeParams splits params into the first single-valued occurrence of each
// modelled name, and the rest in order. A modelled parameter given twice, or
// with several values, keeps its other copies among the rest.
func takeParams(params []contentline.Param, modelled ...string) (map[string]string, []contentline.Param) {
	var out []contentline.Param
	m := map[string]string{}
	for _, p := range params {
		up := strings.ToUpper(p.Name)
		if _, seen := m[up]; p.Name != "" && !seen && slices.Contains(modelled, up) && len(p.Values) == 1 {
			m[up] = p.Values[0]
			continue
		}
		out = append(out, p)
	}
	return m, out
}

// dateList reads an RDATE or EXDATE property, or reports false when a value
// cannot be read.
func dateList(p *contentline.Property, tzid func(string) string, periods bool) ([]DateValue, bool) {
	valueParam := p.Param("VALUE")
	isPeriod := strings.EqualFold(valueParam, "PERIOD")
	if isPeriod && !periods {
		return nil, false
	}
	_, other := takeParams(p.Params, "VALUE", "TZID")
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
				l.keepParams(name, p.Params)
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
				l.keepParams(name, p.Params, "VALUE", "RELATED")
			case "REPEAT":
				n, err := strconv.Atoi(p.Value)
				if err != nil {
					ok = false
					break
				}
				a.Repeat = &n
				l.keepParams(name, p.Params)
			case "DURATION":
				a.RepeatDuration = p.Value
				l.keepParams(name, p.Params)
			case "DESCRIPTION":
				a.Description = UnescapeText(p.Value)
				l.keepParams(name, p.Params)
			case "SUMMARY":
				a.Summary = UnescapeText(p.Value)
				l.keepParams(name, p.Params)
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
// used, then the events in order. zones may be nil to write no VTIMEZONEs.
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
	for _, e := range o.Events {
		cal.Components = append(cal.Components, e.encode(o.UID))
	}
	cal.Components = append(cal.Components, o.Comps...)
	return cal
}

// tzids returns every TZID the events use other than UTC, with the earliest
// year each is used in.
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
	for _, e := range o.Events {
		note(e.Start)
		note(e.End)
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

func (e *Event) encode(uid string) *contentline.Component {
	c := &contentline.Component{Name: "VEVENT"}
	l := &e.Leftovers
	add := func(name, value string, params ...contentline.Param) {
		c.Props = append(c.Props, contentline.Property{Name: name, Params: append(params, l.Params[name]...), Value: value})
	}
	text := func(name, v string) {
		if v != "" {
			add(name, EscapeText(v))
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
	date("DTSTART", e.Start)
	date("DTEND", e.End)
	raw("DURATION", e.Duration)
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
	if len(e.Categories) > 0 {
		vals := make([]string, len(e.Categories))
		for i, v := range e.Categories {
			vals[i] = EscapeText(v)
		}
		c.Props = append(c.Props, contentline.Property{Name: "CATEGORIES", Value: strings.Join(vals, ",")})
	}
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
			slices.EqualFunc(vals[j].Params, vals[i].Params, paramEqual) {
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

func paramEqual(a, b contentline.Param) bool {
	return a.Name == b.Name && slices.Equal(a.Values, b.Values)
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
	add("DESCRIPTION", EscapeText(a.Description))
	add("SUMMARY", EscapeText(a.Summary))
	c.Props = append(c.Props, l.Props...)
	c.Components = append(c.Components, l.Comps...)
	return c
}

// Dependent leftovers: vendor copies of a modelled field, which become wrong
// when Cog edits that field (DESIGN/dav-storage.md section 3.2). A DAV PUT
// replaces the whole component and does not need these.
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
// property name, or TIME for any of DTSTART, DTEND, DURATION, RRULE and
// all-day.
func (e *Event) Changed(fields ...string) {
	for _, f := range fields {
		e.Leftovers.Drop(dependents[f]...)
	}
}

// AlarmChanged drops the leftovers that refer to an alarm's old trigger.
func (a *Alarm) AlarmChanged() { a.Leftovers.Drop(dependents["TRIGGER"]...) }

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
	for _, e := range o.Events {
		b.Events = append(b.Events, &Event{
			RecurrenceID: e.RecurrenceID, ThisAndFuture: e.ThisAndFuture, Summary: "Busy",
			Status: e.Status, Transp: e.Transp, Start: e.Start, End: e.End, Duration: e.Duration,
			RRule: e.RRule, RDates: strip(e.RDates), ExDates: strip(e.ExDates), DTStamp: e.DTStamp,
		})
	}
	return b
}
