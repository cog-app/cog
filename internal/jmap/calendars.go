package jmap

import (
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/itip"
	"git.duckfam.us/jj-software/cog/internal/dav/jscal"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// JMAP for Calendars (draft-ietf-jmap-calendars), behind Server.Calendars:
// Calendar and CalendarEvent over the calendar collections that hold
// events. Tasks are not offered (JMAP for Tasks is a separate draft).

// defaultCalendar names the calendar the core makes for every user.
const defaultCalendar = "personal"

var calendars = &collType{
	name:       "Calendar",
	capability: CapCalendars,
	home:       dav.HomeCalendars,
	comps:      []dav.Component{dav.VEVENT, dav.VTODO},
	colls:      func(a *account) []dav.Granted { return a.cals },
	removeArg:  "onDestroyRemoveEvents",
	toJSON: func(a *account, g *dav.Granted) jsobj.Object {
		r := g.Rights
		return jsobj.Object{
			"id":                       collectionID(g.ID),
			"name":                     g.Title(),
			"description":              nullable(g.Description),
			"color":                    nullable(g.Color),
			"sortOrder":                g.SortOrder,
			"isSubscribed":             true,
			"isVisible":                true,
			"isDefault":                a.own && g.Name == defaultCalendar,
			"includeInAvailability":    "all",
			"defaultAlertsWithTime":    nil,
			"defaultAlertsWithoutTime": nil,
			"timeZone":                 nullable(g.Timezone),
			"shareWith":                nil,
			"myRights": jsobj.Object{
				"mayReadFreeBusy":  r.FreeBusy,
				"mayReadItems":     r.Read,
				"mayWriteAll":      r.Write,
				"mayWriteOwn":      r.Write,
				"mayUpdatePrivate": r.Write,
				"mayRSVP":          r.Write,
				"mayAdmin":         r.Owner,
				"mayDelete":        r.Owner,
			},
		}
	},
	apply: applyCalendar,
}

// applyCalendar sets a calendar's properties a client may set. The
// per-user display preferences (isSubscribed, isVisible,
// includeInAvailability, default alerts) are accepted and not kept.
func applyCalendar(obj jsobj.Object, c *dav.Collection) *setError {
	var bad []string
	rest := jsobj.Object{}
	for k, v := range obj {
		switch k {
		case "color":
			s, ok := v.(string)
			if !ok && v != nil {
				bad = append(bad, k)
				continue
			}
			c.Color = s
		case "timeZone":
			s, ok := v.(string)
			if v != nil && (!ok || tz.Load(s) == nil) {
				bad = append(bad, k)
				continue
			}
			c.Timezone = s
		case "isVisible", "includeInAvailability", "defaultAlertsWithTime", "defaultAlertsWithoutTime":
		default:
			rest[k] = v
		}
	}
	if se := applyBook(rest, c); se != nil {
		bad = append(bad, se.Properties...)
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return &setError{Type: "invalidProperties", Properties: bad}
	}
	return nil
}

func (c *call) calendarGet(args jsobj.Object) (any, error) {
	return c.collectionsGet(calendars, args)
}

func (c *call) calendarChanges(args jsobj.Object) (any, error) {
	return c.collectionsChanges(calendars, args)
}

func (c *call) calendarSet(args jsobj.Object) (any, error) {
	return c.collectionsSet(calendars, args)
}

// --- CalendarEvent ---

// readable returns the calendars whose events may be read: a calendar shared
// for free/busy only is listed, but its events, even their IDs, are not.
func readable(cals []dav.Granted) []dav.Granted {
	return slices.DeleteFunc(slices.Clone(cals), func(g dav.Granted) bool { return !g.Rights.Read })
}

var calendarEvents = &itemType{
	name:       "CalendarEvent",
	capability: CapCalendars,
	comp:       dav.VEVENT,
	ext:        ".ics",
	collProp:   "calendarIds",
	colls:      func(a *account) []dav.Granted { return readable(a.cals) },
	serverSet:  []string{"isDraft", "isOrigin", "utcStart", "utcEnd", "baseEventId"},
	codec:      eventCodec{},
	decorate: func(c *call, g *dav.Granted, m *dav.Model, obj jsobj.Object) {
		obj["isDraft"] = false
		obj["isOrigin"] = isOrigin(g.Owner, m.Object)
	},
}

func init() { calendars.items = calendarEvents }

// isOrigin reports whether the calendar's owner organizes the event (or
// nobody does): their copy is the original, not an invitation.
func isOrigin(owner *dav.User, o *ical.Object) bool {
	org := itip.Organizer(o)
	if org == nil {
		return true
	}
	addr := itip.Normalize(org.Address)
	for _, a := range dav.Addresses(owner) {
		if itip.Normalize(a) == addr {
			return true
		}
	}
	return false
}

type eventCodec struct{}

func (eventCodec) encode(m *dav.Model) (jsobj.Object, error) { return jscal.Encode(m.Object) }

func (eventCodec) decode(obj jsobj.Object) (*dav.Model, error) {
	if t, _ := obj["@type"].(string); t != "Event" {
		return nil, &jsobj.Invalid{Properties: []string{"@type"}}
	}
	o, err := jscal.Decode(obj)
	if err != nil {
		return nil, err
	}
	if o.Components[0].Kind != ical.VEVENT {
		return nil, &jsobj.Invalid{Properties: []string{jsobj.Vendor + "component"}}
	}
	return &dav.Model{Object: o}, nil
}

func (eventCodec) uid(m *dav.Model) string         { return m.Object.UID }
func (eventCodec) setUID(m *dav.Model, uid string) { m.Object.UID = uid }

func (eventCodec) created(m *dav.Model, t time.Time) {
	for _, e := range m.Object.Components {
		if e.Created.IsZero() {
			e.Created = ical.DateTimeOf(t.UTC(), false)
		}
		e.Stamp(t, false)
	}
}

// edited records an edit as the web UI does (section 3.2): a changed
// field's vendor copies go, DTSTAMP and LAST-MODIFIED move, and SEQUENCE
// moves when what attendees are told about changed, unless the client set
// it itself.
func (eventCodec) edited(old, cur *dav.Model, t time.Time) {
	prev := map[string]*ical.Component{}
	for _, e := range old.Object.Components {
		prev[e.RecurrenceID.Local+"|"+e.RecurrenceID.TZID] = e
	}
	for _, e := range cur.Object.Components {
		p := prev[e.RecurrenceID.Local+"|"+e.RecurrenceID.TZID]
		if p == nil {
			if e.Created.IsZero() {
				e.Created = ical.DateTimeOf(t.UTC(), false)
			}
			e.Stamp(t, false)
			continue
		}
		if jsobj.Equal(p, e) {
			continue
		}
		timing := p.Start != e.Start || p.End != e.End || p.Due != e.Due || p.Duration != e.Duration || p.RRule != e.RRule ||
			!jsobj.Equal(p.RDates, e.RDates) || !jsobj.Equal(p.ExDates, e.ExDates)
		if timing {
			e.Changed("TIME")
		}
		if p.Description != e.Description {
			e.Changed("DESCRIPTION")
		}
		if p.Location != e.Location {
			e.Changed("LOCATION")
		}
		for i := range e.Alarms {
			if i < len(p.Alarms) && (p.Alarms[i].Trigger != e.Alarms[i].Trigger || p.Alarms[i].Related != e.Alarms[i].Related) {
				e.Alarms[i].AlarmChanged()
			}
		}
		news := timing || p.Location != e.Location || !jsobj.Equal(attendeesOf(p), attendeesOf(e))
		e.Stamp(t, news && jsobj.Equal(p.Sequence, e.Sequence))
	}
}

// attendeesOf is who attends, for telling whether that changed: a
// PARTSTAT answer is not news to the others.
func attendeesOf(e *ical.Component) []string {
	var out []string
	for _, a := range e.Attendees {
		out = append(out, strings.ToLower(a.Address))
	}
	return out
}

func (eventCodec) render(m *dav.Model) *contentline.Component {
	return m.Object.Encode(m.Object.IANAZones)
}

var eventGetArgs = []string{"recurrenceOverridesBefore", "recurrenceOverridesAfter", "reduceParticipants", "timeZone"}

func (c *call) eventGet(args jsobj.Object) (any, error) {
	return c.getItems(calendarEvents, args, eventGetArgs...)
}

func (c *call) eventChanges(args jsobj.Object) (any, error) {
	return c.itemChanges(calendarEvents, args)
}

// eventSet answers CalendarEvent/set. Scheduling messages go only when
// the client asks (sendSchedulingMessages), as the draft says.
func (c *call) eventSet(args jsobj.Object) (any, error) {
	var opts setOptions
	if v, ok := args["sendSchedulingMessages"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return nil, invalidArguments("sendSchedulingMessages is a boolean")
		}
		opts.send = b
		args = jsobj.Clone(args).(jsobj.Object)
		delete(args, "sendSchedulingMessages")
	}
	return c.setItems(calendarEvents, args, opts)
}

// --- CalendarEvent/query ---

type eventQueryArgs struct {
	queryArgs
	ExpandRecurrences bool    `json:"expandRecurrences"`
	TimeZone          *string `json:"timeZone"`
}

func (c *call) eventQuery(args jsobj.Object) (any, error) {
	var in eventQueryArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if in.ExpandRecurrences {
		return nil, invalidArguments("expandRecurrences is not supported; expand the recurrence rules of the events returned")
	}
	a, err := c.account(in.AccountID, CapCalendars)
	if err != nil {
		return nil, err
	}
	state, _, _, err := c.state(calendarEvents, a)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id    string
		o     *ical.Object
		start time.Time
	}
	var hits []hit
	for _, g := range readable(a.cals) {
		models, err := c.req.dav.Models(&g, dav.VEVENT, nil)
		if err != nil {
			return nil, err
		}
		for i := range models {
			o := models[i].Object
			z := ical.ZonesFor(o, zoneOf(&g))
			start, end, unbounded, err := o.Bounds(z)
			if err != nil {
				start, end, unbounded = time.Time{}, time.Time{}, true
			}
			if in.Filter != nil {
				ok, err := eventMatches(in.Filter, &g, o, start, end, unbounded)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
			}
			hits = append(hits, hit{resourceID(g.ID, models[i].URI), o, start})
		}
	}
	var cmps []func(x, y hit) int
	for _, s := range in.Sort {
		asc := s.IsAscending == nil || *s.IsAscending
		var cmp func(x, y hit) int
		switch s.Property {
		case "start":
			cmp = func(x, y hit) int { return x.start.Compare(y.start) }
		case "uid":
			cmp = func(x, y hit) int { return strings.Compare(x.o.UID, y.o.UID) }
		case "created", "updated":
			field := func(h hit) string {
				m := h.o.Master()
				if m == nil {
					m = h.o.Components[0]
				}
				if s.Property == "created" {
					return m.Created.Local
				}
				return m.LastModified.Local
			}
			cmp = func(x, y hit) int { return strings.Compare(field(x), field(y)) }
		default:
			return nil, errorOf("unsupportedSort", "cannot sort by %q", s.Property)
		}
		if !asc {
			inner := cmp
			cmp = func(x, y hit) int { return -inner(x, y) }
		}
		cmps = append(cmps, cmp)
	}
	slices.SortStableFunc(hits, func(x, y hit) int {
		for _, cmp := range cmps {
			if d := cmp(x, y); d != 0 {
				return d
			}
		}
		return strings.Compare(x.id, y.id)
	})
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	return window(a.id, state, ids, in.queryArgs)
}

func zoneOf(g *dav.Granted) *time.Location {
	if loc := tz.Load(g.Timezone); loc != nil {
		return loc
	}
	return time.UTC
}

// eventMatches evaluates a CalendarEvent FilterCondition or
// FilterOperator against an event's components.
func eventMatches(f jsobj.Object, g *dav.Granted, o *ical.Object, start, end time.Time, unbounded bool) (bool, error) {
	if op, ok := f["operator"].(string); ok {
		conds, _ := f["conditions"].([]any)
		var results []bool
		for _, x := range conds {
			sub, ok := x.(jsobj.Object)
			if !ok {
				return false, invalidArguments("a condition is not an object")
			}
			r, err := eventMatches(sub, g, o, start, end, unbounded)
			if err != nil {
				return false, err
			}
			results = append(results, r)
		}
		switch op {
		case "AND":
			return !slices.Contains(results, false), nil
		case "OR":
			return slices.Contains(results, true), nil
		case "NOT":
			return !slices.Contains(results, true), nil
		}
		return false, invalidArguments("unknown operator %q", op)
	}
	anyComp := func(match func(e *ical.Component) bool) bool {
		return slices.ContainsFunc(o.Components, match)
	}
	for k, v := range f {
		s, isString := v.(string)
		var ok bool
		switch k {
		case "inCalendars":
			list, _ := v.([]any)
			ok = slices.Contains(list, any(collectionID(g.ID)))
		case "after", "before":
			t, err := time.Parse(time.RFC3339, s)
			if !isString || err != nil {
				return false, invalidArguments("%s is a UTCDate", k)
			}
			if k == "after" {
				ok = unbounded || end.After(t)
			} else {
				ok = !start.IsZero() && start.Before(t)
			}
		case "text":
			ok = isString && anyComp(func(e *ical.Component) bool {
				return contains(e.Summary, s) || contains(e.Description, s) || contains(e.Location, s) ||
					slices.ContainsFunc(e.Attendees, func(a ical.Attendee) bool { return contains(a.CN, s) || contains(a.Address, s) })
			})
		case "title":
			ok = isString && anyComp(func(e *ical.Component) bool { return contains(e.Summary, s) })
		case "description":
			ok = isString && anyComp(func(e *ical.Component) bool { return contains(e.Description, s) })
		case "location":
			ok = isString && anyComp(func(e *ical.Component) bool { return contains(e.Location, s) })
		case "owner":
			ok = isString && anyComp(func(e *ical.Component) bool {
				return e.Organizer != nil && (contains(e.Organizer.CN, s) || contains(e.Organizer.Address, s))
			})
		case "attendee":
			ok = isString && anyComp(func(e *ical.Component) bool {
				return slices.ContainsFunc(e.Attendees, func(a ical.Attendee) bool { return contains(a.CN, s) || contains(a.Address, s) })
			})
		case "uid":
			ok = isString && o.UID == s
		default:
			return false, errorOf("unsupportedFilter", "unknown condition %q", k)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
