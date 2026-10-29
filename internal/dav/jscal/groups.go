package jscal

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
)

type (
	object = jsobj.Object
	reader = jsobj.Reader
	group  = jsobj.Group[ical.Component]
)

const v = jsobj.Vendor

// enum maps a lower-case JSCalendar value to the iCalendar value it is
// written as.
type enum map[string]string

// out returns the JSCalendar value of an iCalendar one, or "" when it has
// none and the raw value travels in a vendor property.
func (e enum) out(raw string) string {
	for js, ic := range e {
		if strings.EqualFold(ic, raw) {
			return js
		}
	}
	return ""
}

var (
	eventStatus = enum{"confirmed": "CONFIRMED", "cancelled": "CANCELLED", "tentative": "TENTATIVE"}
	taskStatus  = enum{"needs-action": "NEEDS-ACTION", "in-process": "IN-PROCESS", "completed": "COMPLETED", "cancelled": "CANCELLED"}
	privacies   = enum{"public": "PUBLIC", "private": "PRIVATE", "secret": "CONFIDENTIAL"}
	transps     = enum{"busy": "OPAQUE", "free": "TRANSPARENT"}
	partstats   = enum{"needs-action": "NEEDS-ACTION", "accepted": "ACCEPTED", "declined": "DECLINED", "tentative": "TENTATIVE", "delegated": "DELEGATED"}
	cutypes     = enum{"individual": "INDIVIDUAL", "group": "GROUP", "resource": "RESOURCE", "location": "ROOM"}
	actions     = enum{"display": "DISPLAY", "email": "EMAIL"}
)

// enumGroup maps one iCalendar enumeration: values JSCalendar has go to
// prop, others to a vendor property as written.
func enumGroup(name, prop string, e enum, field func(*ical.Component) *string) group {
	vendor := v + strings.ToLower(name)
	g := group{Name: name, Props: []string{prop, vendor}}
	g.Enc = func(c *ical.Component, out object) {
		raw := *field(c)
		if raw == "" {
			return
		}
		if js := e.out(raw); js != "" {
			out[prop] = js
			return
		}
		out[vendor] = raw
	}
	g.Dec = func(in reader, c *ical.Component) {
		*field(c) = ""
		if raw := in.String(vendor); raw != "" {
			*field(c) = raw
			return
		}
		js := in.String(prop)
		if js == "" {
			return
		}
		ic, ok := e[strings.ToLower(js)]
		if !ok {
			in.Fail(prop)
			return
		}
		*field(c) = ic
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) string { return *field(c) }, func(c *ical.Component, s string) { *field(c) = s })
	return g
}

func textGroup(prop string, field func(*ical.Component) *string) group {
	g := group{Name: prop, Props: []string{prop}}
	g.Enc = func(c *ical.Component, out object) { jsobj.Set(out, prop, *field(c)) }
	g.Dec = func(in reader, c *ical.Component) { *field(c) = in.String(prop) }
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) string { return *field(c) }, func(c *ical.Component, s string) { *field(c) = s })
	return g
}

func intGroup(name, prop string, field func(*ical.Component) **int) group {
	g := group{Name: name, Props: []string{prop}}
	g.Enc = func(c *ical.Component, out object) { jsobj.Set(out, prop, *field(c)) }
	g.Dec = func(in reader, c *ical.Component) { *field(c) = in.Int(prop) }
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) *int { return *field(c) }, func(c *ical.Component, n *int) { *field(c) = n })
	return g
}

// utcGroup maps a DATE-TIME that is usually UTC to a UTCDateTime.
func (x *ctx) utcGroup(name, prop string, field func(*ical.Component) *ical.DateTime) group {
	g := group{Name: name, Props: []string{prop}}
	g.Enc = func(c *ical.Component, out object) { jsobj.Set(out, prop, utcOut(*field(c), x.zones)) }
	g.Dec = func(in reader, c *ical.Component) {
		*field(c) = ical.DateTime{}
		if s := in.String(prop); s != "" {
			d, err := utcIn(s)
			if err != nil {
				in.Fail(prop)
				return
			}
			*field(c) = d
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) ical.DateTime { return *field(c) }, func(c *ical.Component, d ical.DateTime) { *field(c) = d })
	return g
}

// ctx is what encoding or decoding one object's components needs.
type ctx struct {
	kind  ical.Kind
	zones ical.Zones
	// override is set for a component inside recurrenceOverrides, whose
	// recurrence id is its key.
	override bool
	// master is set for a series' master, whose RDATEs and EXDATEs are
	// keys of recurrenceOverrides.
	master bool
	// extra collects the JSPROP properties of what a client sent that Cog
	// does not model, for the component being decoded.
	extra []contentline.Property
}

// groups are a component's groups, in the order they are read: times
// before what depends on them.
func (x *ctx) groups() []group {
	gs := []group{
		textGroup("title", func(c *ical.Component) *string { return &c.Summary }),
		textGroup("description", func(c *ical.Component) *string { return &c.Description }),
		x.locationGroup(),
		x.timeGroup(),
	}
	if !x.override {
		gs = append(gs, x.recurrenceIDGroup())
	}
	switch x.kind {
	case ical.VEVENT:
		gs = append(gs, enumGroup("status", "status", eventStatus, func(c *ical.Component) *string { return &c.Status }))
	case ical.VTODO:
		gs = append(gs, enumGroup("status", "progress", taskStatus, func(c *ical.Component) *string { return &c.Status }))
	default:
		gs = append(gs, enumGroup("status", v+"journalStatus", enum{}, func(c *ical.Component) *string { return &c.Status }))
	}
	gs = append(gs,
		enumGroup("class", "privacy", privacies, func(c *ical.Component) *string { return &c.Class }),
		enumGroup("transp", "freeBusyStatus", transps, func(c *ical.Component) *string { return &c.Transp }),
		intGroup("priority", "priority", func(c *ical.Component) **int { return &c.Priority }),
		intGroup("sequence", "sequence", func(c *ical.Component) **int { return &c.Sequence }),
		intGroup("percentComplete", "percentComplete", func(c *ical.Component) **int { return &c.PercentComplete }),
		intGroup("sortOrder", v+"sortOrder", func(c *ical.Component) **int { return &c.SortOrder }),
		x.utcGroup("created", "created", func(c *ical.Component) *ical.DateTime { return &c.Created }),
		x.utcGroup("updated", "updated", func(c *ical.Component) *ical.DateTime { return &c.LastModified }),
		x.utcGroup("dtstamp", v+"dtstamp", func(c *ical.Component) *ical.DateTime { return &c.DTStamp }),
		x.utcGroup("completed", "progressUpdated", func(c *ical.Component) *ical.DateTime { return &c.Completed }),
		keywordsGroup(),
		x.relatedGroup(),
		x.linksGroup(),
		x.participantsGroup(),
		x.alertsGroup(),
		x.ruleGroup(),
	)
	if !x.master {
		gs = append(gs, x.datesGroup())
	}
	return gs
}

// componentKnown are the properties a component's groups read.
var componentKnown = jsobj.Known("@type", "title", "description", "locations", "start", "timeZone", "showWithoutTime",
	"duration", "due", "estimatedDuration", "recurrenceId", "recurrenceIdTimeZone", "status", "progress", "privacy",
	"freeBusyStatus", "priority", "sequence", "percentComplete", "created", "updated", "progressUpdated", "keywords",
	"relatedTo", "links", "participants", "replyTo", "alerts", "recurrenceRules")

// --- Location ---

func (x *ctx) locationGroup() group {
	g := group{Name: "location", Props: []string{"locations"}}
	g.Enc = func(c *ical.Component, out object) {
		if c.Location == "" {
			return
		}
		id, _ := jsobj.TakeJSID(c.Leftovers.Params["LOCATION"])
		out["locations"] = object{jsobj.IDs([]string{id})[0]: object{"@type": "Location", "name": c.Location}}
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.Location = ""
		ids, items := in.Items("locations")
		// The first location with a name is LOCATION; the others are
		// kept whole, inside the map when LOCATION makes one.
		rest := object{}
		for i, it := range items {
			if c.Location == "" && it.String("name") != "" {
				c.Location = it.String("name")
				setParamJSID(&c.Leftovers.Params, "LOCATION", jsobj.OwnID(ids[i], 0))
				continue
			}
			rest[ids[i]] = it.Obj
		}
		if c.Location == "" {
			if len(rest) > 0 {
				x.sink("/locations", rest)
			}
			return
		}
		for _, id := range jsobj.SortedIDs(rest) {
			x.sink("/locations/"+jsobj.Escape(id), rest[id])
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) string { return c.Location }, func(c *ical.Component, s string) { c.Location = s })
	return g
}

// setParamJSID sets or removes the JSID among a modelled property's kept
// parameters.
func setParamJSID(params *map[string][]contentline.Param, prop, id string) {
	_, rest := jsobj.TakeJSID((*params)[prop])
	rest = jsobj.WithJSID(rest, id)
	if len(rest) == 0 {
		delete(*params, prop)
		return
	}
	if *params == nil {
		*params = map[string][]contentline.Param{}
	}
	(*params)[prop] = rest
}

func (x *ctx) sink(ptr string, val any) {
	p, err := jsobj.Sink(ptr, val)
	if err == nil {
		x.extra = append(x.extra, p)
	}
}

// --- Times ---

type times struct {
	Start, End, Due ical.DateTime
	Duration        string
}

func getTimes(c *ical.Component) times {
	return times{c.Start, c.End, c.Due, c.Duration}
}

func putTimes(c *ical.Component, t times) {
	c.Start, c.End, c.Due, c.Duration = t.Start, t.End, t.Due, t.Duration
}

// timeGroup maps DTSTART, DTEND, DUE and DURATION: an event's start and
// duration, a task's start, due and estimated duration, in one time zone.
func (x *ctx) timeGroup() group {
	g := group{Name: "time", Props: []string{"start", "timeZone", "showWithoutTime", "duration", "due", "estimatedDuration"}}
	g.Enc = func(c *ical.Component, out object) {
		anchor := c.Start
		if anchor.IsZero() {
			anchor = c.Due
		}
		if anchor.IsZero() {
			return
		}
		jsobj.Set(out, "timeZone", zoneOut(anchor.TZID))
		jsobj.Set(out, "showWithoutTime", anchor.IsDate)
		if !c.Start.IsZero() {
			out["start"] = localOf(c.Start)
		}
		if !c.Due.IsZero() {
			out["due"] = localOf(c.Due)
		}
		switch {
		case c.Kind == ical.VTODO:
			if d, ok := durationOut(c.Duration); ok {
				out["estimatedDuration"] = d
			}
		case c.Duration != "":
			if d, ok := durationOut(c.Duration); ok {
				out["duration"] = d
			}
		case !c.End.IsZero() && !c.Start.IsZero():
			if d, ok := between(c.Start, c.End, x.zones); ok {
				out["duration"] = d
			}
		}
	}
	g.Dec = func(in reader, c *ical.Component) {
		putTimes(c, times{})
		zone := in.String("timeZone")
		date := in.Bool("showWithoutTime")
		read := func(prop string) ical.DateTime {
			s := in.String(prop)
			if s == "" {
				return ical.DateTime{}
			}
			d, err := dateTimeOf(s, zone, date)
			if err != nil {
				in.Fail(prop)
			}
			return d
		}
		c.Start = read("start")
		c.Due = read("due")
		if c.Kind == ical.VTODO {
			c.Duration = in.String("estimatedDuration")
			return
		}
		dur := in.String("duration")
		if dur == "" || c.Start.IsZero() {
			return
		}
		end, err := plus(c.Start, dur, x.zones)
		if err != nil {
			in.Fail("duration")
			return
		}
		c.End = end
	}
	g.Snap, g.Load = jsobj.Fields(getTimes, putTimes)
	return g
}

type recurrenceID struct {
	ID            ical.DateTime
	ThisAndFuture bool
}

// recurrenceIDGroup maps the RECURRENCE-ID of a component that stands
// alone: an occurrence of a series whose master is not in the resource.
func (x *ctx) recurrenceIDGroup() group {
	g := group{Name: "recurrenceId", Props: []string{"recurrenceId", "recurrenceIdTimeZone", v + "thisAndFuture"}}
	g.Enc = func(c *ical.Component, out object) {
		if c.RecurrenceID.IsZero() {
			return
		}
		out["recurrenceId"] = localOf(c.RecurrenceID)
		jsobj.Set(out, "recurrenceIdTimeZone", zoneOut(c.RecurrenceID.TZID))
		jsobj.Set(out, v+"thisAndFuture", c.ThisAndFuture)
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.RecurrenceID, c.ThisAndFuture = ical.DateTime{}, false
		s := in.String("recurrenceId")
		if s == "" {
			return
		}
		d, err := dateTimeOf(s, in.String("recurrenceIdTimeZone"), c.Start.IsDate)
		if err != nil {
			in.Fail("recurrenceId")
			return
		}
		c.RecurrenceID, c.ThisAndFuture = d, in.Bool(v+"thisAndFuture")
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) recurrenceID { return recurrenceID{c.RecurrenceID, c.ThisAndFuture} },
		func(c *ical.Component, r recurrenceID) { c.RecurrenceID, c.ThisAndFuture = r.ID, r.ThisAndFuture })
	return g
}

// ruleGroup maps RRULE to recurrenceRules, or, when JSCalendar cannot say
// it, keeps it as written.
func (x *ctx) ruleGroup() group {
	g := group{Name: "rrule", Props: []string{"recurrenceRules", v + "rrule"}}
	g.Enc = func(c *ical.Component, out object) {
		if c.RRule == "" {
			return
		}
		if r, ok := ruleOut(c.RRule, c.Start, x.zones); ok {
			out["recurrenceRules"] = []any{r}
			return
		}
		out[v+"rrule"] = c.RRule
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.RRule = in.String(v + "rrule")
		rules, ok := in.Obj["recurrenceRules"].([]any)
		if !ok {
			if in.Has("recurrenceRules") {
				in.Fail("recurrenceRules")
			}
			return
		}
		for i, r := range rules {
			ro, ok := r.(object)
			if !ok {
				in.Fail("recurrenceRules")
				return
			}
			rr := reader{Obj: ro, Path: in.Path + "/recurrenceRules/" + strconv.Itoa(i), Bad: in.Bad}
			if i > 0 {
				// One RRULE per component (RFC 5545 SHOULD NOT have more);
				// the others are kept.
				x.sink("/recurrenceRules/"+strconv.Itoa(i), ro)
				continue
			}
			anchor := c.Start
			if anchor.IsZero() {
				anchor = c.Due
			}
			if s, ok := ruleIn(rr, anchor, x.zones); ok {
				c.RRule = s
			}
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) string { return c.RRule }, func(c *ical.Component, s string) { c.RRule = s })
	return g
}

type dates struct {
	RDates, ExDates []ical.DateValue
}

// datesGroup keeps the RDATE and EXDATE of a component that is not a
// series' master, which JSCalendar cannot say: only a master's are keys of
// recurrenceOverrides.
func (x *ctx) datesGroup() group {
	g := group{Name: "dates", Props: []string{v + "rdates", v + "exdates"}}
	g.Enc = func(c *ical.Component, out object) {
		if len(c.RDates) > 0 {
			out[v+"rdates"] = c.RDates
		}
		if len(c.ExDates) > 0 {
			out[v+"exdates"] = c.ExDates
		}
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.RDates, c.ExDates = nil, nil
		in.Decode(v+"rdates", &c.RDates)
		in.Decode(v+"exdates", &c.ExDates)
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) dates { return dates{c.RDates, c.ExDates} },
		func(c *ical.Component, d dates) { c.RDates, c.ExDates = d.RDates, d.ExDates })
	return g
}

// --- Keywords and relations ---

func keywordsGroup() group {
	g := group{Name: "categories", Props: []string{"keywords"}}
	g.Enc = func(c *ical.Component, out object) {
		if len(c.Categories) == 0 {
			return
		}
		set := object{}
		for _, k := range c.Categories {
			set[k] = true
		}
		out["keywords"] = set
	}
	g.Dec = func(in reader, c *ical.Component) { c.Categories = in.Set("keywords") }
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) []string { return c.Categories }, func(c *ical.Component, s []string) { c.Categories = s })
	return g
}

// relatedGroup maps RELATED-TO. RELTYPE is the relation, lower case; a
// relation without one has an empty set, which RFC 5545 reads as PARENT.
func (x *ctx) relatedGroup() group {
	g := group{Name: "relatedTo", Props: []string{"relatedTo"}}
	g.Enc = func(c *ical.Component, out object) {
		if len(c.Relations) == 0 {
			return
		}
		rel := object{}
		for _, r := range c.Relations {
			o := object{"@type": "Relation"}
			if r.Type != "" {
				o["relation"] = object{strings.ToLower(r.Type): true}
			}
			if len(r.Params) > 0 {
				o[jsobj.ParamsKey] = r.Params
			}
			rel[r.UID] = o
		}
		out["relatedTo"] = rel
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.Relations = nil
		ids, items := in.Items("relatedTo")
		for i, it := range items {
			r := ical.Relation{UID: ids[i]}
			if types := it.Set("relation"); len(types) > 0 {
				r.Type = strings.ToUpper(types[0])
			}
			it.Decode(jsobj.ParamsKey, &r.Params)
			c.Relations = append(c.Relations, r)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) []ical.Relation { return c.Relations }, func(c *ical.Component, r []ical.Relation) { c.Relations = r })
	return g
}

// --- Links: URL and ATTACH ---

type links struct {
	URL         string
	Attachments []ical.Attachment
}

const (
	relURL    = "describedby"
	relAttach = "enclosure"
)

// linksGroup maps URL (a link with rel describedby) and every ATTACH (rel
// enclosure; inline data as a data: URI).
func (x *ctx) linksGroup() group {
	g := group{Name: "links", Props: []string{"links"}}
	g.Enc = func(c *ical.Component, out object) {
		if c.URL == "" && len(c.Attachments) == 0 {
			return
		}
		var own []string
		if c.URL != "" {
			id, _ := jsobj.TakeJSID(c.Leftovers.Params["URL"])
			own = append(own, id)
		}
		for _, a := range c.Attachments {
			id, _ := jsobj.TakeJSID(a.Params)
			own = append(own, id)
		}
		ids := jsobj.IDs(own)
		set := object{}
		if c.URL != "" {
			set[ids[0]] = object{"@type": "Link", "href": c.URL, "rel": relURL}
			ids = ids[1:]
		}
		for i, a := range c.Attachments {
			l := object{"@type": "Link", "rel": relAttach}
			if a.Inline {
				l["href"] = "data:" + a.FmtType + ";base64," + a.Value
			} else {
				l["href"] = a.Value
			}
			jsobj.Set(l, "contentType", a.FmtType)
			if a.Size != nil {
				l["size"] = json.Number(strconv.FormatInt(*a.Size, 10))
			}
			jsobj.Set(l, "title", a.Filename)
			jsobj.Set(l, v+"managedId", a.ManagedID)
			if _, params := jsobj.TakeJSID(a.Params); len(params) > 0 {
				l[jsobj.ParamsKey] = params
			}
			set[ids[i]] = l
		}
		out["links"] = set
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.URL, c.Attachments = "", nil
		ids, items := in.Items("links")
		pos := 0
		for i, it := range items {
			href := it.String("href")
			if href == "" {
				it.Fail("href")
				continue
			}
			if c.URL == "" && it.String("rel") == relURL {
				c.URL = href
				setParamJSID(&c.Leftovers.Params, "URL", jsobj.OwnID(ids[i], pos))
				pos++
				continue
			}
			a := ical.Attachment{Value: href, FmtType: it.String("contentType"), Filename: it.String("title"), ManagedID: it.String(v + "managedId")}
			if rest, ok := strings.CutPrefix(href, "data:"); ok {
				if typ, data, ok := strings.Cut(rest, ";base64,"); ok {
					a.Inline, a.Value = true, data
					if a.FmtType == "" {
						a.FmtType = typ
					}
				}
			}
			if n := it.Int("size"); n != nil {
				size := int64(*n)
				a.Size = &size
			}
			it.Decode(jsobj.ParamsKey, &a.Params)
			a.Params = jsobj.WithJSID(a.Params, jsobj.OwnID(ids[i], pos))
			pos++
			c.Attachments = append(c.Attachments, a)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) links { return links{c.URL, c.Attachments} },
		func(c *ical.Component, l links) { c.URL, c.Attachments = l.URL, l.Attachments })
	return g
}

// --- Participants: ORGANIZER and ATTENDEE ---

type people struct {
	Organizer *ical.Organizer
	Attendees []ical.Attendee
}

// sendTo writes a calendar address as a participant's sendTo: imip for
// mailto, other for anything else.
func sendTo(address string) object {
	if ical.Email(address) != "" {
		return object{"imip": address}
	}
	return object{"other": address}
}

func addressOf(r reader, key string) string {
	st, ok := r.Object(key)
	if !ok {
		return ""
	}
	sr := reader{Obj: st, Path: r.Path + "/" + key, Bad: r.Bad}
	if a := sr.String("imip"); a != "" {
		return a
	}
	if a := sr.String("other"); a != "" {
		return a
	}
	for _, k := range jsobj.SortedIDs(st) {
		if a, ok := st[k].(string); ok && a != "" {
			return a
		}
	}
	return ""
}

func roleOut(role string) (object, string) {
	switch strings.ToUpper(role) {
	case "", "REQ-PARTICIPANT":
		return object{"attendee": true}, ""
	case "OPT-PARTICIPANT":
		return object{"attendee": true, "optional": true}, ""
	case "NON-PARTICIPANT":
		return object{"informational": true}, ""
	case "CHAIR":
		return object{"attendee": true, "chair": true}, ""
	}
	return object{"attendee": true}, role
}

func roleIn(roles map[string]bool) string {
	switch {
	case roles["chair"]:
		return "CHAIR"
	case roles["optional"]:
		return "OPT-PARTICIPANT"
	case roles["informational"] && !roles["attendee"]:
		return "NON-PARTICIPANT"
	}
	return "REQ-PARTICIPANT"
}

// emailParam is RFC 7986's EMAIL parameter: a participant's email when it
// is not the address they are scheduled at.
const emailParam = "EMAIL"

// takeParam removes the first single-valued parameter of a name.
func takeParam(params []contentline.Param, name string) (string, []contentline.Param) {
	for i, p := range params {
		if strings.EqualFold(p.Name, name) && len(p.Values) == 1 {
			return p.Values[0], slices.Delete(slices.Clone(params), i, i+1)
		}
	}
	return "", params
}

// participant writes the parts of a participant an ATTENDEE and an
// ORGANIZER share: who they are and how to reach them.
func participant(address, cn string, params []contentline.Param) (object, []contentline.Param) {
	p := object{"@type": "Participant", "sendTo": sendTo(address)}
	jsobj.Set(p, "name", cn)
	email, params := takeParam(params, emailParam)
	if email == "" {
		email = ical.Email(address)
	}
	jsobj.Set(p, "email", email)
	return p, params
}

// withEmail keeps a participant's email as an EMAIL parameter when its
// address does not say it.
func withEmail(params []contentline.Param, email, address string) []contentline.Param {
	if email == "" || strings.EqualFold(email, ical.Email(address)) {
		return params
	}
	return append(params, contentline.Param{Name: emailParam, Values: []string{email}})
}

// participantsGroup maps ORGANIZER to replyTo and a participant with the
// owner role: the attendee with its address or, when the organizer's JSID
// names one, that attendee; otherwise a participant of its own. Each
// ATTENDEE is a participant.
func (x *ctx) participantsGroup() group {
	g := group{Name: "participants", Props: []string{"participants", "replyTo"}}
	g.Enc = func(c *ical.Component, out object) {
		org := c.Organizer
		if org == nil && len(c.Attendees) == 0 {
			return
		}
		own := make([]string, len(c.Attendees))
		for i, a := range c.Attendees {
			own[i], _ = jsobj.TakeJSID(a.Params)
		}
		ids := jsobj.IDs(own)
		var orgID string
		var orgParams []contentline.Param
		merged := -1
		if org != nil {
			orgID, orgParams = jsobj.TakeJSID(org.Params)
			for i, a := range c.Attendees {
				if orgID != "" && ids[i] == orgID || orgID == "" && strings.EqualFold(org.Address, a.Address) {
					merged = i
					break
				}
			}
		}
		parts := object{}
		for i, a := range c.Attendees {
			_, params := jsobj.TakeJSID(a.Params)
			p, params := participant(a.Address, a.CN, params)
			roles, rawRole := roleOut(a.Role)
			jsobj.Set(p, v+"role", rawRole)
			if i == merged {
				roles["owner"] = true
				if len(orgParams) > 0 {
					p[v+"organizerParams"] = orgParams
				}
				jsobj.Set(p, v+"organizerName", org.CN)
			}
			p["roles"] = roles
			if a.PartStat != "" {
				if js := partstats.out(a.PartStat); js != "" {
					p["participationStatus"] = js
				} else {
					p[v+"partstat"] = a.PartStat
				}
			}
			if a.CUType != "" {
				if js := cutypes.out(a.CUType); js != "" {
					p["kind"] = js
				} else {
					p[v+"cutype"] = a.CUType
				}
			}
			switch strings.ToUpper(a.RSVP) {
			case "":
			case "TRUE":
				p["expectReply"] = true
			case "FALSE":
				p["expectReply"] = false
			default:
				p[v+"rsvp"] = a.RSVP
			}
			if len(params) > 0 {
				p[jsobj.ParamsKey] = params
			}
			parts[ids[i]] = p
		}
		if org != nil {
			out["replyTo"] = sendTo(org.Address)
			if merged < 0 {
				id := orgID
				if id == "" || parts[id] != nil {
					id = "0"
				}
				p, params := participant(org.Address, org.CN, orgParams)
				p["roles"] = object{"owner": true}
				if len(params) > 0 {
					p[jsobj.ParamsKey] = params
				}
				parts[id] = p
			}
		}
		if len(parts) > 0 {
			out["participants"] = parts
		}
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.Organizer, c.Attendees = nil, nil
		replyTo := addressOf(in, "replyTo")
		ids, items := in.Items("participants")
		for i, it := range items {
			roles := map[string]bool{}
			for _, r := range it.Set("roles") {
				roles[r] = true
			}
			email := it.String("email")
			addr := addressOf(it, "sendTo")
			if addr == "" && email != "" {
				addr = "mailto:" + email
			}
			if addr == "" {
				it.Fail("sendTo")
				continue
			}
			attends := !roles["owner"] || roles["attendee"] || roles["optional"] || roles["informational"] || roles["chair"]
			if roles["owner"] && c.Organizer != nil {
				// ORGANIZER is single: a second owner is kept as sent.
				x.sink(it.Path, it.Obj)
				continue
			}
			if roles["owner"] {
				o := &ical.Organizer{Address: addr}
				if replyTo != "" {
					o.Address = replyTo
				}
				if attends {
					o.CN = it.String(v + "organizerName")
					it.Decode(v+"organizerParams", &o.Params)
					if !strings.EqualFold(o.Address, addr) {
						o.Params = jsobj.WithJSID(o.Params, ids[i])
					}
				} else {
					o.CN = it.String("name")
					it.Decode(jsobj.ParamsKey, &o.Params)
					o.Params = withEmail(o.Params, email, o.Address)
					if ids[i] != "0" {
						o.Params = jsobj.WithJSID(o.Params, ids[i])
					}
				}
				c.Organizer = o
			}
			if !attends {
				continue
			}
			a := ical.Attendee{Address: addr, CN: it.String("name"), Role: it.String(v + "role")}
			if a.Role == "" {
				a.Role = roleIn(roles)
			}
			a.PartStat = it.String(v + "partstat")
			if a.PartStat == "" {
				if js := it.String("participationStatus"); js != "" {
					a.PartStat = strings.ToUpper(js)
				}
			}
			a.CUType = it.String(v + "cutype")
			if a.CUType == "" {
				if js := it.String("kind"); js != "" {
					if ic, ok := cutypes[strings.ToLower(js)]; ok {
						a.CUType = ic
					} else {
						a.CUType = strings.ToUpper(js)
					}
				}
			}
			a.RSVP = it.String(v + "rsvp")
			if b, ok := it.Obj["expectReply"].(bool); ok && a.RSVP == "" {
				a.RSVP = strings.ToUpper(strconv.FormatBool(b))
			}
			it.Decode(jsobj.ParamsKey, &a.Params)
			a.Params = withEmail(a.Params, email, addr)
			a.Params = jsobj.WithJSID(a.Params, jsobj.OwnID(ids[i], len(c.Attendees)))
			c.Attendees = append(c.Attendees, a)
		}
		if replyTo != "" && c.Organizer == nil {
			c.Organizer = &ical.Organizer{Address: replyTo}
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) people { return people{c.Organizer, c.Attendees} },
		func(c *ical.Component, p people) { c.Organizer, c.Attendees = p.Organizer, p.Attendees })
	return g
}

// participantKnown are the properties a participant's decoding reads.
var participantKnown = jsobj.Known("@type", "name", "email", "sendTo", "roles", "participationStatus", "kind", "expectReply")

// --- Alerts: VALARM ---

// alertsGroup maps each VALARM to an alert: its trigger and action, and
// what JSCalendar has no property for in vendor properties.
func (x *ctx) alertsGroup() group {
	g := group{Name: "alarms", Props: []string{"alerts"}}
	g.Enc = func(c *ical.Component, out object) {
		if len(c.Alarms) == 0 {
			return
		}
		own := make([]string, len(c.Alarms))
		for i, a := range c.Alarms {
			own[i], _ = jsobj.TakeJSID(a.Leftovers.Params["ACTION"])
		}
		ids := jsobj.IDs(own)
		set := object{}
		for i := range c.Alarms {
			a := &c.Alarms[i]
			al := object{"@type": "Alert"}
			if js := actions.out(a.Action); js != "" {
				al["action"] = js
			} else if a.Action != "" {
				al[v+"action"] = a.Action
			}
			if trig, ok := triggerOut(a); ok {
				al["trigger"] = trig
			} else {
				al[v+"trigger"] = a.Trigger
				jsobj.Set(al, v+"related", a.Related)
				jsobj.Set(al, v+"absolute", a.Absolute)
			}
			jsobj.Set(al, v+"description", a.Description)
			jsobj.Set(al, v+"summary", a.Summary)
			jsobj.Set(al, v+"repeat", a.Repeat)
			jsobj.Set(al, v+"repeatDuration", a.RepeatDuration)
			l := a.Leftovers.Clone()
			setParamJSID(&l.Params, "ACTION", "")
			if !l.IsZero() {
				al[jsobj.LeftoversKey] = l
			}
			set[ids[i]] = al
		}
		out["alerts"] = set
	}
	g.Dec = func(in reader, c *ical.Component) {
		c.Alarms = nil
		ids, items := in.Items("alerts")
		for i, it := range items {
			var a ical.Alarm
			it.Decode(jsobj.LeftoversKey, &a.Leftovers)
			a.Action = it.String(v + "action")
			if a.Action == "" {
				a.Action = "DISPLAY"
				if js := it.String("action"); js != "" {
					ic, ok := actions[strings.ToLower(js)]
					if !ok {
						it.Fail("action")
					}
					a.Action = ic
				}
			}
			if raw := it.String(v + "trigger"); raw != "" {
				a.Trigger, a.Related, a.Absolute = raw, it.String(v+"related"), it.Bool(v+"absolute")
			} else if !triggerIn(it.At("trigger"), &a) {
				it.Fail("trigger")
				continue
			}
			a.Description = it.String(v + "description")
			a.Summary = it.String(v + "summary")
			a.Repeat = it.Int(v + "repeat")
			a.RepeatDuration = it.String(v + "repeatDuration")
			setParamJSID(&a.Leftovers.Params, "ACTION", jsobj.OwnID(ids[i], len(c.Alarms)))
			c.Alarms = append(c.Alarms, a)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *ical.Component) []ical.Alarm { return c.Alarms }, func(c *ical.Component, a []ical.Alarm) { c.Alarms = a })
	return g
}

// alertKnown are the properties an alert's decoding reads.
var alertKnown = jsobj.Known("@type", "action", "trigger")

func triggerOut(a *ical.Alarm) (object, bool) {
	if a.Absolute {
		if !strings.HasSuffix(a.Trigger, "Z") {
			return nil, false
		}
		d, err := utcIn(localOf(ical.DateTime{Local: strings.TrimSuffix(a.Trigger, "Z")}) + "Z")
		if err != nil {
			return nil, false
		}
		return object{"@type": "AbsoluteTrigger", "when": localOf(d) + "Z"}, true
	}
	off, ok := signedOut(a.Trigger)
	if !ok {
		return nil, false
	}
	t := object{"@type": "OffsetTrigger", "offset": off}
	switch strings.ToUpper(a.Related) {
	case "":
	case "START":
		t["relativeTo"] = "start"
	case "END":
		t["relativeTo"] = "end"
	default:
		return nil, false
	}
	return t, true
}

func triggerIn(r reader, a *ical.Alarm) bool {
	switch r.String("@type") {
	case "AbsoluteTrigger":
		d, err := utcIn(r.String("when"))
		if err != nil {
			return false
		}
		a.Trigger, a.Absolute = d.Local+"Z", true
		return true
	case "OffsetTrigger", "":
		off := r.String("offset")
		if _, err := ical.ParseDuration(off); err != nil {
			return false
		}
		a.Trigger = off
		switch r.String("relativeTo") {
		case "start":
			a.Related = "START"
		case "end":
			a.Related = "END"
		}
		return true
	}
	return false
}
