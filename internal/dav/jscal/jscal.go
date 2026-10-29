// Package jscal maps calendar objects (internal/dav/ical models) to
// JSCalendar (RFC 8984) and back: events and journal entries as Event, tasks
// as Task. It works on the same models the iCalendar text is generated from,
// never on the text.
//
// Nothing is lost either way (DESIGN/dav-storage.md section 9): what the
// model holds that JSCalendar cannot say travels in "cog:" vendor
// properties, and what a JSON client sends that the model has no field for
// is kept as a JSPROP property (internal/dav/jsobj). Decode(Encode(o))
// equals o for every resource in the fixture corpus.
//
// A journal entry has no JSCalendar type; it is an Event with
// "cog:component": "VJOURNAL". Overrides of a series are
// recurrenceOverrides patches against the master, RDATEs are empty
// patches and EXDATEs excluded ones.
package jscal

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
)

// Object-level properties.
const (
	keyType      = "@type"
	keyUID       = "uid"
	keyOverrides = "recurrenceOverrides"
	keyZones     = "timeZones"
	keyComponent = v + "component"
	keyCalendar  = v + "calendar"
	keyOrphans   = v + "orphans"
	// In an override's patch: its exact RECURRENCE-ID when the key does
	// not say it, RANGE=THISANDFUTURE, and the properties a patch may not
	// set (RFC 8984 section 4.3.5), with null for "absent".
	keyRID      = v + "recurrenceId"
	keyFuture   = v + "thisAndFuture"
	keyOverride = v + "override"
	// Components beside the master and its overrides, whole: a second
	// override of one occurrence, a second master.
	keyMore = v + "more"
)

var objectKnown = jsobj.Known(keyType, keyUID, "prodId", "method", keyOverrides, keyZones)

// forbidden are the properties of a component a recurrenceOverrides patch
// may not set; an override that differs in them says so in keyOverride.
var forbidden = jsobj.Known("privacy", "relatedTo", "replyTo", "recurrenceRules", "excludedRecurrenceRules", "sentBy")

// schema is what Strip keeps of a component: everything else a client sent
// becomes a JSPROP.
var schema = func() *jsobj.Schema {
	known := jsobj.Known()
	for k := range componentKnown {
		known[k] = true
	}
	for k := range objectKnown {
		known[k] = true
	}
	return &jsobj.Schema{
		Known: known,
		Maps: map[string]*jsobj.Schema{
			"participants": {Known: participantKnown},
			"alerts": {Known: alertKnown, Objects: map[string]*jsobj.Schema{
				"trigger": {Known: jsobj.Known("@type", "offset", "relativeTo", "when")},
			}},
			"locations": {Known: jsobj.Known("@type", "name")},
			"links":     {Known: jsobj.Known("@type", "href", "rel", "contentType", "size", "title")},
			"relatedTo": {Known: jsobj.Known("@type", "relation")},
		},
	}
}()

func cloneComp(c *ical.Component) *ical.Component { return c.Clone() }

// Encode writes a calendar object as a JSCalendar object.
func Encode(o *ical.Object) (jsobj.Object, error) {
	if len(o.Components) == 0 {
		return nil, ical.ErrNoComponent
	}
	kind := o.Components[0].Kind
	x := &ctx{kind: kind, zones: ical.ZonesFor(o, time.UTC)}
	m := o.Master()
	main, orphans := m, []*ical.Component(nil)
	if m == nil {
		main, orphans = o.Components[0], o.Components[1:]
	}
	mx := *x
	mx.master = m != nil
	out, err := mx.encodeComponent(main)
	if err != nil {
		return nil, err
	}
	if m != nil {
		if err := x.encodeOverrides(o, m, out); err != nil {
			return nil, err
		}
	}
	if len(orphans) > 0 {
		var list []any
		for _, c := range orphans {
			js, err := x.encodeComponent(c)
			if err != nil {
				return nil, err
			}
			list = append(list, js)
		}
		out[keyOrphans] = list
	}
	switch kind {
	case ical.VTODO:
		out[keyType] = "Task"
	case ical.VJOURNAL:
		out[keyType] = "Event"
		out[keyComponent] = string(kind)
	default:
		out[keyType] = "Event"
	}
	out[keyUID] = o.UID
	for _, p := range o.Props {
		switch {
		case p.Is("PRODID") && out["prodId"] == nil:
			out["prodId"] = p.Value
		case p.Is("METHOD") && out["method"] == nil:
			out["method"] = strings.ToLower(p.Value)
		}
	}
	if len(o.Props) > 0 || len(o.Others) > 0 {
		cal := jsobj.Object{}
		if len(o.Props) > 0 {
			cal["props"] = o.Props
		}
		if len(o.Others) > 0 {
			cal["comps"] = o.Others
		}
		out[keyCalendar] = cal
	}
	if zs := zonesOut(o); len(zs) > 0 {
		out[keyZones] = zs
	}
	tree, err := jsobj.Tree(out)
	if err != nil {
		return nil, err
	}
	return tree.(jsobj.Object), nil
}

// encodeComponent writes one component's properties, its exact forms, its
// leftovers, and the values of its JSPROPs at their pointers.
func (x *ctx) encodeComponent(c *ical.Component) (jsobj.Object, error) {
	out := jsobj.Object{}
	if err := jsobj.EncodeGroups(x.groups(), c, out, cloneComp); err != nil {
		return nil, fmt.Errorf("%s %s: %w", c.Kind, c.Summary, err)
	}
	tree, err := jsobj.Tree(out)
	if err != nil {
		return nil, err
	}
	obj := tree.(jsobj.Object)
	l := c.Leftovers.Clone()
	l.Props = jsobj.Lift(l.Props, obj)
	if !l.IsZero() {
		lt, err := jsobj.Tree(l)
		if err != nil {
			return nil, err
		}
		obj[jsobj.LeftoversKey] = lt
	}
	return obj, nil
}

// anchor is the value a master's occurrences are counted from.
func anchor(m *ical.Component) ical.DateTime {
	if m.Start.IsZero() && m.Kind == ical.VTODO {
		return m.Due
	}
	return m.Start
}

// baseOf is what an occurrence of the master inherits: its properties
// without the recurrence, in an override's shape.
func (x *ctx) baseOf(m *ical.Component) (jsobj.Object, error) {
	inst := m.Clone()
	inst.RRule, inst.RDates, inst.ExDates = "", nil, nil
	ox := *x
	ox.override = true
	return ox.encodeComponent(inst)
}

// at is base moved to the occurrence at key.
func at(base jsobj.Object, m *ical.Component, key string) jsobj.Object {
	b := jsobj.Clone(base).(jsobj.Object)
	if !m.Start.IsZero() {
		b["start"] = key
	} else if !m.Due.IsZero() {
		b["due"] = key
	}
	return b
}

// recurrence is the exact form of a master's RDATEs and EXDATEs and the
// order of its overrides, kept when the keys of recurrenceOverrides do not
// say them.
type recurrence struct {
	RDates  []ical.DateValue `json:"rdates,omitempty"`
	ExDates []ical.DateValue `json:"exdates,omitempty"`
	Order   []string         `json:"order,omitempty"`
}

func (x *ctx) encodeOverrides(o *ical.Object, m *ical.Component, out jsobj.Object) error {
	a := anchor(m)
	entries := jsobj.Object{}
	entry := func(k string) jsobj.Object {
		e, ok := entries[k].(jsobj.Object)
		if !ok {
			e = jsobj.Object{}
			entries[k] = e
		}
		return e
	}
	var order []string
	var more []any
	var base jsobj.Object
	ox := *x
	ox.override = true
	for _, c := range o.Components {
		if c == m {
			continue
		}
		if !c.IsOverride() {
			// A second master (a client's broken resource): kept whole.
			js, err := x.encodeComponent(c)
			if err != nil {
				return err
			}
			order = append(order, "#"+strconv.Itoa(len(more)))
			more = append(more, js)
			continue
		}
		if base == nil {
			var err error
			if base, err = x.baseOf(m); err != nil {
				return err
			}
		}
		k := keyOf(c.RecurrenceID, a, x.zones)
		if e, ok := entries[k].(jsobj.Object); ok && isOverride(e) {
			// A second override of one occurrence: kept whole.
			js, err := x.encodeComponent(c)
			if err != nil {
				return err
			}
			order = append(order, "#"+strconv.Itoa(len(more)))
			more = append(more, js)
			continue
		}
		full, err := ox.encodeComponent(c)
		if err != nil {
			return err
		}
		patch := diff(at(base, m, k), full)
		if rid, err := atKey(k, a); err != nil || rid != c.RecurrenceID {
			patch[keyRID] = c.RecurrenceID
		}
		if c.ThisAndFuture {
			patch[keyFuture] = true
		}
		if len(patch) == 0 {
			patch[keyOverride] = jsobj.Object{}
		}
		e := entry(k)
		maps.Copy(e, patch)
		order = append(order, k)
	}
	for _, d := range m.RDates {
		entry(keyOf(d.DateTime, a, x.zones))
	}
	for _, d := range m.ExDates {
		entry(keyOf(d.DateTime, a, x.zones))["excluded"] = true
	}
	if len(more) > 0 {
		out[keyMore] = more
	}
	if len(entries) == 0 {
		return nil
	}
	out[keyOverrides] = entries
	exact := recurrence{RDates: m.RDates, ExDates: m.ExDates, Order: order}
	std, err := splitEntries(entries, a, len(more))
	if err != nil || !jsobj.Equal(std, exact) {
		ex, _ := out[jsobj.ExactKey].(jsobj.Object)
		if ex == nil {
			ex = jsobj.Object{}
			out[jsobj.ExactKey] = ex
		}
		ex["recurrence"] = exact
	}
	return nil
}

// diff returns the patch that turns base into full, top-level properties
// replaced whole. Properties a patch may not set go in keyOverride.
func diff(base, full jsobj.Object) jsobj.Object {
	patch := jsobj.Object{}
	over := jsobj.Object{}
	put := func(k string, val any) {
		if forbidden[k] {
			over[k] = val
		} else {
			patch[k] = val
		}
	}
	for k, val := range full {
		if b, ok := base[k]; !ok || !jsobj.Equal(b, val) {
			put(k, val)
		}
	}
	for k := range base {
		if _, ok := full[k]; !ok {
			put(k, nil)
		}
	}
	if len(over) > 0 {
		patch[keyOverride] = over
	}
	return patch
}

// splitEntries reads the RDATEs, EXDATEs and override order the keys of
// recurrenceOverrides say: an empty patch is an RDATE, an excluded one an
// EXDATE, anything else an override. The more overrides of occurrences
// that have one come last, as "#" and their index.
func splitEntries(entries jsobj.Object, a ical.DateTime, more int) (recurrence, error) {
	var r recurrence
	for _, k := range sortedKeys(entries) {
		e, _ := entries[k].(jsobj.Object)
		excluded := e["excluded"] == true
		d, err := atKey(k, a)
		if err != nil {
			return r, err
		}
		if excluded {
			r.ExDates = append(r.ExDates, ical.DateValue{DateTime: d})
		}
		switch {
		case isOverride(e):
			r.Order = append(r.Order, k)
		case !excluded:
			r.RDates = append(r.RDates, ical.DateValue{DateTime: d})
		}
	}
	for i := range more {
		r.Order = append(r.Order, "#"+strconv.Itoa(i))
	}
	return r, nil
}

func isOverride(e jsobj.Object) bool {
	for k := range e {
		if k != "excluded" {
			return true
		}
	}
	return false
}

func sortedKeys(o jsobj.Object) []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// --- Decoding ---

// Decode reads a JSCalendar Event or Task. Properties it cannot read are
// reported as a *jsobj.Invalid.
func Decode(in jsobj.Object) (*ical.Object, error) {
	var bad []string
	r := jsobj.Reader{Obj: in, Bad: &bad}
	x := &ctx{}
	switch t := r.String(keyType); {
	case t == "Task":
		x.kind = ical.VTODO
	case t == "Event" && r.String(keyComponent) == string(ical.VJOURNAL):
		x.kind = ical.VJOURNAL
	case t == "Event":
		x.kind = ical.VEVENT
	default:
		return nil, &jsobj.Invalid{Properties: []string{keyType}}
	}
	o := &ical.Object{UID: r.String(keyUID)}
	if o.UID == "" {
		bad = append(bad, keyUID)
	}
	o.Zones = zonesIn(r.At(keyZones))
	x.zones = ical.ZonesFor(o, time.UTC)

	if cal, ok := r.Object(keyCalendar); ok {
		cr := jsobj.Reader{Obj: cal, Path: "/" + keyCalendar, Bad: &bad}
		cr.Decode("props", &o.Props)
		cr.Decode("comps", &o.Others)
	}
	if p := r.String("prodId"); p != "" {
		o.Props = setCalendarProp(o.Props, "PRODID", p)
	}
	if mth := r.String("method"); mth != "" {
		o.Props = setCalendarProp(o.Props, "METHOD", strings.ToUpper(mth))
	}

	mx := *x
	mx.master = !r.Has("recurrenceId")
	main, err := mx.decodeComponent(in, false, &bad)
	if err != nil {
		return nil, err
	}
	o.Components = append(o.Components, main)
	if main.IsOverride() {
		list, _ := in[keyOrphans].([]any)
		for i, item := range list {
			js, ok := item.(jsobj.Object)
			if !ok {
				bad = append(bad, fmt.Sprintf("%s/%d", keyOrphans, i))
				continue
			}
			c, err := x.decodeComponent(js, false, &bad)
			if err != nil {
				return nil, err
			}
			o.Components = append(o.Components, c)
		}
	} else if entries, ok := r.Object(keyOverrides); ok || r.Has(keyMore) {
		exact, _ := in[jsobj.ExactKey].(jsobj.Object)
		more, _ := in[keyMore].([]any)
		overrides, err := x.decodeOverrides(main, entries, more, exact["recurrence"], &bad)
		if err != nil {
			return nil, err
		}
		o.Components = append(o.Components, overrides...)
	}
	if len(bad) > 0 {
		return nil, &jsobj.Invalid{Properties: bad}
	}
	return o, nil
}

// setCalendarProp sets the first VCALENDAR property of a name, or adds it.
func setCalendarProp(props []contentline.Property, name, value string) []contentline.Property {
	for i := range props {
		if props[i].Is(name) {
			props[i].Value = value
			return props
		}
	}
	return append(props, contentline.Property{Name: name, Value: value})
}

// decodeComponent reads one component: unknown properties become JSPROPs
// first, so what is compared with exact forms is only what Cog reads.
func (x *ctx) decodeComponent(in jsobj.Object, override bool, bad *[]string) (*ical.Component, error) {
	clean := jsobj.Clone(in).(jsobj.Object)
	if !override {
		for k := range objectKnown {
			delete(clean, k)
		}
	}
	unknown, err := jsobj.Strip(clean, schema)
	if err != nil {
		return nil, err
	}
	cx := *x
	cx.override, cx.extra = override, nil
	c := &ical.Component{Kind: x.kind}
	r := jsobj.Reader{Obj: clean, Bad: bad}
	r.Decode(jsobj.LeftoversKey, &c.Leftovers)
	jsobj.DecodeGroups(cx.groups(), r, c, cloneComp)
	c.Leftovers.Props = append(c.Leftovers.Props, unknown...)
	c.Leftovers.Props = append(c.Leftovers.Props, cx.extra...)
	return c, nil
}

func (x *ctx) decodeOverrides(m *ical.Component, entries jsobj.Object, more []any, exactRaw any, bad *[]string) ([]*ical.Component, error) {
	a := anchor(m)
	std, err := splitEntries(entries, a, len(more))
	if err != nil {
		*bad = append(*bad, keyOverrides)
		return nil, nil
	}
	m.RDates, m.ExDates = std.RDates, std.ExDates
	order := std.Order
	if exactRaw != nil {
		var ex recurrence
		if b, err := json.Marshal(exactRaw); err == nil && json.Unmarshal(b, &ex) == nil && consistent(ex, std, entries, a, x.zones) {
			m.RDates, m.ExDates = ex.RDates, ex.ExDates
			order = append(slices.DeleteFunc(slices.Clone(ex.Order), func(k string) bool { return !slices.Contains(std.Order, k) }),
				slices.DeleteFunc(slices.Clone(std.Order), func(k string) bool { return slices.Contains(ex.Order, k) })...)
		}
	}
	if len(order) == 0 {
		return nil, nil
	}
	base, err := x.baseOf(m)
	if err != nil {
		return nil, err
	}
	var out []*ical.Component
	for _, k := range order {
		if i, err := strconv.Atoi(strings.TrimPrefix(k, "#")); err == nil && strings.HasPrefix(k, "#") {
			js, ok := more[i].(jsobj.Object)
			if !ok {
				*bad = append(*bad, keyMore+"/"+strconv.Itoa(i))
				continue
			}
			c, err := x.decodeComponent(js, false, bad)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
			continue
		}
		e := entries[k].(jsobj.Object)
		patch := jsobj.Object{}
		for pk, pv := range e {
			switch pk {
			case "excluded", keyRID, keyFuture, keyOverride:
			default:
				patch[pk] = pv
			}
		}
		full := at(base, m, k)
		if err := applyPatch(full, patch); err != nil {
			*bad = append(*bad, keyOverrides+"/"+jsobj.Escape(k))
			continue
		}
		if over, ok := e[keyOverride].(jsobj.Object); ok {
			for ok, ov := range over {
				if ov == nil {
					delete(full, ok)
				} else {
					full[ok] = ov
				}
			}
		}
		c, err := x.decodeComponent(full, true, bad)
		if err != nil {
			return nil, err
		}
		c.RecurrenceID, _ = atKey(k, a)
		if raw, ok := e[keyRID]; ok {
			var rid ical.DateTime
			if b, err := json.Marshal(raw); err == nil && json.Unmarshal(b, &rid) == nil && keyOf(rid, a, x.zones) == k {
				c.RecurrenceID = rid
			}
		}
		c.ThisAndFuture = e[keyFuture] == true
		out = append(out, c)
	}
	return out, nil
}

// consistent reports whether the exact form of a master's recurrence still
// says what recurrenceOverrides says.
func consistent(ex, std recurrence, entries jsobj.Object, a ical.DateTime, z ical.Zones) bool {
	keys := func(ds []ical.DateValue) []string {
		out := make([]string, len(ds))
		for i, d := range ds {
			out[i] = keyOf(d.DateTime, a, z)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	exKeys, stdEx := keys(ex.ExDates), keys(std.ExDates)
	if !slices.Equal(exKeys, stdEx) {
		return false
	}
	rd := keys(ex.RDates)
	for _, k := range rd {
		if _, ok := entries[k]; !ok {
			return false
		}
	}
	for _, k := range keys(std.RDates) {
		if !slices.Contains(rd, k) {
			return false
		}
	}
	return true
}

// applyPatch applies a PatchObject (RFC 8984 section 1.4.9): each key a
// JSON pointer without its leading slash, each value the new value or null
// to remove it.
func applyPatch(o jsobj.Object, patch jsobj.Object) error {
	for _, k := range sortedKeys(patch) {
		val := patch[k]
		parts := strings.Split(strings.TrimPrefix(k, "/"), "/")
		cur := o
		for _, part := range parts[:len(parts)-1] {
			next, ok := cur[unescapePtr(part)].(jsobj.Object)
			if !ok {
				return fmt.Errorf("patch %s: no such object", k)
			}
			cur = next
		}
		last := unescapePtr(parts[len(parts)-1])
		if val == nil {
			delete(cur, last)
		} else {
			cur[last] = jsobj.Clone(val)
		}
	}
	return nil
}

// ApplyPatch applies a JMAP or JSCalendar PatchObject to o in place.
func ApplyPatch(o jsobj.Object, patch jsobj.Object) error { return applyPatch(o, patch) }

func unescapePtr(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// --- Time zones ---

// zonesOut writes the custom time zones the components use: a client's own
// VTIMEZONE, whole in a vendor property and as JSCalendar rules, or a bare
// name for a TZID nothing defines.
func zonesOut(o *ical.Object) jsobj.Object {
	used := map[string]bool{}
	note := func(d ical.DateTime) {
		if !d.IsZero() && !d.IsDate && strings.HasPrefix(zoneOut(d.TZID), "/") {
			used[d.TZID] = true
		}
	}
	for _, c := range o.Components {
		for _, d := range []ical.DateTime{c.Start, c.End, c.Due, c.Completed, c.RecurrenceID} {
			note(d)
		}
		for _, d := range c.RDates {
			note(d.DateTime)
		}
		for _, d := range c.ExDates {
			note(d.DateTime)
		}
	}
	out := jsobj.Object{}
	for id := range used {
		z := jsobj.Object{"@type": "TimeZone", "tzId": id}
		if vtz, ok := o.Zones[id]; ok {
			z[v+"vtimezone"] = vtz
			maps.Copy(z, rulesOut(vtz))
		}
		out["/"+id] = z
	}
	return out
}

// zonesIn reads custom time zones: the VTIMEZONE a vendor property kept,
// while the rules are still what it says, or one built from the rules.
func zonesIn(r jsobj.Reader) map[string]*contentline.Component {
	var out map[string]*contentline.Component
	for _, k := range jsobj.SortedIDs(r.Obj) {
		zo, ok := r.Obj[k].(jsobj.Object)
		if !ok || !strings.HasPrefix(k, "/") {
			continue
		}
		zr := jsobj.Reader{Obj: zo, Path: r.Path + "/" + jsobj.Escape(k), Bad: r.Bad}
		id := k[1:]
		var vtz *contentline.Component
		zr.Decode(v+"vtimezone", &vtz)
		given := jsobj.Pick(zo, "standard", "daylight")
		if vtz != nil {
			derived, err := jsobj.Tree(rulesOut(vtz))
			if err != nil || !jsobj.Equal(derived, given) {
				vtz = nil
			}
		}
		if vtz == nil && len(given) > 0 {
			vtz = rulesIn(id, zr)
		}
		if vtz == nil {
			continue
		}
		if out == nil {
			out = map[string]*contentline.Component{}
		}
		out[id] = vtz
	}
	return out
}

// rulesOut writes a VTIMEZONE's observances as TimeZoneRules.
func rulesOut(vtz *contentline.Component) jsobj.Object {
	out := jsobj.Object{}
	for _, sub := range vtz.Components {
		kind := ""
		switch {
		case sub.Is("STANDARD"):
			kind = "standard"
		case sub.Is("DAYLIGHT"):
			kind = "daylight"
		default:
			continue
		}
		rule := jsobj.Object{"@type": "TimeZoneRule"}
		names := jsobj.Object{}
		overrides := jsobj.Object{}
		start := ical.DateTime{}
		for _, p := range sub.Props {
			switch {
			case p.Is("DTSTART"):
				start = ical.DateTime{Local: strings.TrimSpace(p.Value)}
				rule["start"] = localOf(start)
			case p.Is("TZOFFSETFROM"):
				rule["offsetFrom"] = p.Value
			case p.Is("TZOFFSETTO"):
				rule["offsetTo"] = p.Value
			case p.Is("TZNAME"):
				names[codec.UnescapeText(p.Value)] = true
			case p.Is("RDATE"):
				for d := range strings.SplitSeq(p.Value, ",") {
					overrides[localOf(ical.DateTime{Local: strings.TrimSuffix(d, "Z")})] = jsobj.Object{}
				}
			}
		}
		for _, p := range sub.Props {
			if p.Is("RRULE") {
				if r, ok := ruleOut(p.Value, start, ical.Zones{}); ok {
					rule["recurrenceRules"] = []any{r}
				}
			}
		}
		if len(names) > 0 {
			rule["names"] = names
		}
		if len(overrides) > 0 {
			rule["recurrenceOverrides"] = overrides
		}
		list, _ := out[kind].([]any)
		out[kind] = append(list, rule)
	}
	return out
}

// rulesIn builds a VTIMEZONE from TimeZoneRules.
func rulesIn(id string, r jsobj.Reader) *contentline.Component {
	vtz := &contentline.Component{Name: "VTIMEZONE", Props: []contentline.Property{{Name: "TZID", Value: id}}}
	for _, kind := range []string{"standard", "daylight"} {
		list, _ := r.Obj[kind].([]any)
		for _, item := range list {
			ro, ok := item.(jsobj.Object)
			if !ok {
				r.Fail(kind)
				continue
			}
			rr := jsobj.Reader{Obj: ro, Path: r.Path + "/" + kind, Bad: r.Bad}
			start, _, err := parseLocal(rr.String("start"), false)
			if err != nil {
				rr.Fail("start")
				continue
			}
			sub := &contentline.Component{Name: strings.ToUpper(kind)}
			add := func(name, value string) {
				if value != "" {
					sub.Props = append(sub.Props, contentline.Property{Name: name, Value: value})
				}
			}
			add("DTSTART", start)
			add("TZOFFSETFROM", rr.String("offsetFrom"))
			add("TZOFFSETTO", rr.String("offsetTo"))
			if rules, ok := ro["recurrenceRules"].([]any); ok && len(rules) > 0 {
				if first, ok := rules[0].(jsobj.Object); ok {
					if s, ok := ruleIn(jsobj.Reader{Obj: first, Path: rr.Path + "/recurrenceRules/0", Bad: r.Bad}, ical.DateTime{Local: start}, ical.Zones{}); ok {
						add("RRULE", s)
					}
				}
			}
			for _, name := range rr.Set("names") {
				add("TZNAME", codec.EscapeText(name))
			}
			if ov, ok := rr.Object("recurrenceOverrides"); ok {
				var dates []string
				for _, k := range sortedKeys(ov) {
					d, _, err := parseLocal(k, false)
					if err == nil {
						dates = append(dates, d)
					}
				}
				add("RDATE", strings.Join(dates, ","))
			}
			vtz.Components = append(vtz.Components, sub)
		}
	}
	return vtz
}
