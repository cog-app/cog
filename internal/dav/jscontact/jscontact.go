// Package jscontact maps contacts (internal/dav/vcard models) to JSContact
// (RFC 9553) cards and back. It works on the same model the vCard text is
// generated from, never on the text.
//
// Nothing is lost either way (DESIGN/dav-storage.md section 9): what the
// model holds that JSContact cannot say travels in "cog:" vendor
// properties (the vCard version, types JSContact has no context or feature
// for, Apple's property groups, unknown parameters, leftovers), and what a
// JSON client sends that the model has no field for is kept as a JSPROP
// property (RFC 9555 section 3.3.1). Decode(Encode(c)) equals c for every
// card in the fixture corpus.
package jscontact

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

type (
	object = jsobj.Object
	reader = jsobj.Reader
	group  = jsobj.Group[vcard.Card]
)

const v = jsobj.Vendor

// DefaultVersion is the vCard version of a card a JSON client creates, as
// the web UI's are.
const DefaultVersion = vcard.V30

// cloneCard copies a card for reading one group back into: groups replace
// the fields they read, and change Leftovers.Params only by key.
func cloneCard(c *vcard.Card) *vcard.Card {
	out := *c
	out.Leftovers = c.Leftovers.Clone()
	return &out
}

// ctx collects, while decoding, what a client sent that Cog does not model.
type ctx struct {
	extra []contentline.Property
}

func (x *ctx) sink(ptr string, val any) {
	// val was decoded from the request's JSON, so it encodes again: Sink
	// cannot fail on it.
	if p, err := jsobj.Sink(ptr, val); err == nil {
		x.extra = append(x.extra, p)
	}
}

var cardKnown = jsobj.Known("@type", "version", "uid", "kind", "name", "nicknames", "organizations", "titles", "notes",
	"anniversaries", "updated", "keywords", "emails", "phones", "addresses", "links", "onlineServices", "relatedTo",
	"members", "media")

var schema = &jsobj.Schema{
	Known: cardKnown,
	Objects: map[string]*jsobj.Schema{
		"name": {Known: jsobj.Known("@type", "components", "full"), Arrays: map[string]*jsobj.Schema{
			"components": {Known: jsobj.Known("@type", "kind", "value")},
		}},
	},
	Maps: map[string]*jsobj.Schema{
		"nicknames": {Known: jsobj.Known("@type", "name")},
		"organizations": {Known: jsobj.Known("@type", "name", "units"), Arrays: map[string]*jsobj.Schema{
			"units": {Known: jsobj.Known("@type", "name")},
		}},
		"titles": {Known: jsobj.Known("@type", "name", "kind")},
		"notes":  {Known: jsobj.Known("@type", "note")},
		"anniversaries": {Known: jsobj.Known("@type", "kind", "date"), Objects: map[string]*jsobj.Schema{
			"date": {Known: jsobj.Known("@type", "year", "month", "day")},
		}},
		"emails": {Known: jsobj.Known("@type", "address", "contexts", "pref", "label")},
		"phones": {Known: jsobj.Known("@type", "number", "features", "contexts", "pref", "label")},
		"addresses": {Known: jsobj.Known("@type", "components", "contexts", "pref"), Arrays: map[string]*jsobj.Schema{
			"components": {Known: jsobj.Known("@type", "kind", "value")},
		}},
		"links":          {Known: jsobj.Known("@type", "uri", "contexts", "pref", "label")},
		"onlineServices": {Known: jsobj.Known("@type", "uri", "contexts", "pref", "label")},
		"relatedTo":      {Known: jsobj.Known("@type", "relation")},
		"media":          {Known: jsobj.Known("@type", "kind", "uri")},
	},
}

// Encode writes a card as a JSContact Card.
func Encode(c *vcard.Card) (jsobj.Object, error) {
	x := &ctx{}
	out := object{"@type": "Card", "version": "1.0"}
	if err := jsobj.EncodeGroups(x.groups(), c, out, cloneCard); err != nil {
		return nil, fmt.Errorf("card %s: %w", c.UID, err)
	}
	if c.Photo != nil {
		out["media"] = photoOut(c)
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

// Decode reads a JSContact Card. Properties it cannot read are reported as
// a *jsobj.Invalid.
func Decode(in jsobj.Object) (*vcard.Card, error) {
	if t, _ := in["@type"].(string); t != "Card" {
		return nil, &jsobj.Invalid{Properties: []string{"@type"}}
	}
	clean := jsobj.Clone(in).(jsobj.Object)
	unknown, err := jsobj.Strip(clean, schema)
	if err != nil {
		return nil, err
	}
	var bad []string
	r := reader{Obj: clean, Bad: &bad}
	x := &ctx{}
	c := &vcard.Card{}
	r.Decode(jsobj.LeftoversKey, &c.Leftovers)
	jsobj.DecodeGroups(x.groups(), r, c, cloneCard)
	x.photoIn(r, c)
	if c.UID == "" {
		bad = append(bad, "uid")
	}
	if len(bad) > 0 {
		return nil, &jsobj.Invalid{Properties: bad}
	}
	c.Leftovers.Props = append(c.Leftovers.Props, unknown...)
	c.Leftovers.Props = append(c.Leftovers.Props, x.extra...)
	return c, nil
}

// groups are a card's groups, in the order they are read: the version
// first, which decides how values are written.
func (x *ctx) groups() []group {
	return []group{
		textGroup("version", v+"vcardVersion", func(c *vcard.Card) *string { return &c.Version }, DefaultVersion),
		textGroup("uid", "uid", func(c *vcard.Card) *string { return &c.UID }, ""),
		kindGroup(),
		x.nameGroup(),
		x.nicknamesGroup(),
		x.orgGroup(),
		x.titlesGroup(),
		x.notesGroup(),
		x.datesGroup(),
		textGroup("gender", v+"gender", func(c *vcard.Card) *string { return &c.Gender }, ""),
		revGroup(),
		keywordsGroup(),
		x.valuesGroup("emails", "EmailAddress", "address", func(c *vcard.Card) *[]vcard.Value { return &c.Emails }, false),
		x.valuesGroup("phones", "Phone", "number", func(c *vcard.Card) *[]vcard.Value { return &c.Phones }, true),
		x.addressesGroup(),
		x.valuesGroup("links", "Link", "uri", func(c *vcard.Card) *[]vcard.Value { return &c.URLs }, false),
		x.valuesGroup("onlineServices", "OnlineService", "uri", func(c *vcard.Card) *[]vcard.Value { return &c.IMPPs }, false),
		x.relatedGroup(),
		membersGroup(),
	}
}

func textGroup(name, prop string, field func(*vcard.Card) *string, def string) group {
	g := group{Name: name, Props: []string{prop}}
	g.Enc = func(c *vcard.Card, out object) { jsobj.Set(out, prop, *field(c)) }
	g.Dec = func(in reader, c *vcard.Card) {
		*field(c) = in.String(prop)
		if *field(c) == "" {
			*field(c) = def
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) string { return *field(c) }, func(c *vcard.Card, s string) { *field(c) = s })
	return g
}

var kinds = jsobj.Known("individual", "group", "org", "location", "device", "application")

func kindGroup() group {
	g := group{Name: "kind", Props: []string{"kind", v + "kind"}}
	g.Enc = func(c *vcard.Card, out object) {
		switch {
		case c.Kind == "":
		case kinds[strings.ToLower(c.Kind)]:
			out["kind"] = strings.ToLower(c.Kind)
		default:
			out[v+"kind"] = c.Kind
		}
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Kind = in.String(v + "kind")
		if c.Kind == "" {
			c.Kind = in.String("kind")
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) string { return c.Kind }, func(c *vcard.Card, s string) { c.Kind = s })
	return g
}

// --- Name ---

type names struct {
	FN string
	N  *vcard.Name
}

// nameKinds are the NameComponent kinds of N's components, in N's order.
var nameKinds = []string{"surname", "given", "given2", "title", "credential"}

func nameComponents(n *vcard.Name) []any {
	comps := []any{}
	for i, list := range [][]string{n.Family, n.Given, n.Additional, n.Prefix, n.Suffix} {
		for _, val := range list {
			comps = append(comps, object{"@type": "NameComponent", "kind": nameKinds[i], "value": val})
		}
	}
	return comps
}

// nameOf reads NameComponents as N. Kinds N does not have go to the
// component nearest in meaning; separators are formatting.
func nameOf(list []any, r reader) *vcard.Name {
	n := &vcard.Name{}
	fields := map[string]*[]string{
		"surname": &n.Family, "surname2": &n.Family, "given": &n.Given, "given2": &n.Additional,
		"title": &n.Prefix, "credential": &n.Suffix, "generation": &n.Suffix,
	}
	for i, item := range list {
		comp, ok := item.(object)
		if !ok {
			r.Fail("components/" + strconv.Itoa(i))
			continue
		}
		cr := reader{Obj: comp, Path: r.Path + "/components/" + strconv.Itoa(i), Bad: r.Bad}
		if f := fields[cr.String("kind")]; f != nil {
			*f = append(*f, cr.String("value"))
		}
	}
	return n
}

// compsParam keeps, on N or ADR, the components a client sent when they
// are not in the property's order or have kinds it does not: base64 of
// their JSON. They are written back while they still say what the
// property says.
const compsParam = "X-COG-JSCOMPS"

func stashed(params []contentline.Param) []any {
	for _, p := range params {
		if strings.EqualFold(p.Name, compsParam) && len(p.Values) == 1 {
			// A stash that does not decode (edited by hand, or by another
			// client) is ignored: the vCard properties still stand on their own.
			b, err := base64.RawURLEncoding.DecodeString(p.Values[0])
			if err != nil {
				return nil
			}
			v, err := jsobj.Parse(b)
			if err != nil {
				return nil
			}
			list, _ := v.([]any)
			return list
		}
	}
	return nil
}

// stash sets or removes the kept components.
func stash(params []contentline.Param, given, std []any) []contentline.Param {
	out := slices.DeleteFunc(slices.Clone(params), func(p contentline.Param) bool { return strings.EqualFold(p.Name, compsParam) })
	if given == nil || jsobj.Equal(untyped(given), untyped(std)) {
		return out
	}
	b, err := json.Marshal(given)
	if err != nil {
		return out
	}
	return append(out, contentline.Param{Name: compsParam, Values: []string{base64.RawURLEncoding.EncodeToString(b)}})
}

// untyped drops the @type of components, which is implied: a client that
// leaves it out sent the same components.
func untyped(list []any) []any {
	out := make([]any, len(list))
	for i, x := range list {
		if o, ok := x.(object); ok {
			o = jsobj.Clone(o).(object)
			delete(o, "@type")
			x = o
		}
		out[i] = x
	}
	return out
}

func (x *ctx) nameGroup() group {
	g := group{Name: "name", Props: []string{"name"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.FN == "" && c.N == nil {
			return
		}
		n := object{"@type": "Name"}
		jsobj.Set(n, "full", c.FN)
		if c.N != nil {
			comps := nameComponents(c.N)
			if kept := stashed(c.Leftovers.Params["N"]); kept != nil && jsobj.Equal(nameOf(kept, reader{}), c.N) {
				comps = kept
			}
			n["components"] = comps
		}
		out["name"] = n
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.FN, c.N = "", nil
		n := in.At("name")
		if n.Obj == nil {
			return
		}
		c.FN = n.String("full")
		list, ok := n.Obj["components"].([]any)
		if !ok {
			return
		}
		c.N = nameOf(list, n)
		params := stash(c.Leftovers.Params["N"], list, nameComponents(c.N))
		if len(params) > 0 {
			if c.Leftovers.Params == nil {
				c.Leftovers.Params = map[string][]contentline.Param{}
			}
			c.Leftovers.Params["N"] = params
		} else {
			delete(c.Leftovers.Params, "N")
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) names { return names{c.FN, c.N} }, func(c *vcard.Card, n names) { c.FN, c.N = n.FN, n.N })
	return g
}

// --- Single values kept as maps ---

// ownID is the id of a modelled property's item, from its JSID parameter.
func ownID(c *vcard.Card, prop string) string {
	id, _ := jsobj.TakeJSID(c.Leftovers.Params[prop])
	return id
}

func setJSID(c *vcard.Card, prop, id string) {
	_, rest := jsobj.TakeJSID(c.Leftovers.Params[prop])
	rest = jsobj.WithJSID(rest, id)
	if len(rest) == 0 {
		delete(c.Leftovers.Params, prop)
		return
	}
	if c.Leftovers.Params == nil {
		c.Leftovers.Params = map[string][]contentline.Param{}
	}
	c.Leftovers.Params[prop] = rest
}

// nicknameIDs are the ids of NICKNAME's values: a JSID parameter with one
// value per nickname, or their positions.
func nicknameIDs(c *vcard.Card) []string {
	own := make([]string, len(c.Nicknames))
	for _, p := range c.Leftovers.Params["NICKNAME"] {
		if strings.EqualFold(p.Name, jsobj.JSID) && len(p.Values) == len(own) {
			copy(own, p.Values)
		}
	}
	return jsobj.IDs(own)
}

func (x *ctx) nicknamesGroup() group {
	g := group{Name: "nicknames", Props: []string{"nicknames"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.Nicknames == nil {
			return
		}
		ids := nicknameIDs(c)
		m := object{}
		for i, n := range c.Nicknames {
			m[ids[i]] = object{"@type": "Nickname", "name": n}
		}
		out["nicknames"] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Nicknames = nil
		ids, items := in.Items("nicknames")
		positional := true
		for i, it := range items {
			c.Nicknames = append(c.Nicknames, it.String("name"))
			positional = positional && jsobj.OwnID(ids[i], i) == ""
		}
		params := slices.DeleteFunc(slices.Clone(c.Leftovers.Params["NICKNAME"]), func(p contentline.Param) bool { return strings.EqualFold(p.Name, jsobj.JSID) })
		if !positional {
			params = append(params, contentline.Param{Name: jsobj.JSID, Values: ids})
		}
		if len(params) > 0 {
			if c.Leftovers.Params == nil {
				c.Leftovers.Params = map[string][]contentline.Param{}
			}
			c.Leftovers.Params["NICKNAME"] = params
		} else {
			delete(c.Leftovers.Params, "NICKNAME")
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []string { return c.Nicknames }, func(c *vcard.Card, n []string) { c.Nicknames = n })
	return g
}

func (x *ctx) orgGroup() group {
	g := group{Name: "org", Props: []string{"organizations"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.Org == nil {
			return
		}
		o := object{"@type": "Organization"}
		jsobj.Set(o, "name", c.Org[0])
		if len(c.Org) > 1 {
			var units []any
			for _, u := range c.Org[1:] {
				units = append(units, object{"@type": "OrgUnit", "name": u})
			}
			o["units"] = units
		}
		out["organizations"] = object{jsobj.IDs([]string{ownID(c, "ORG")})[0]: o}
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Org = nil
		ids, items := in.Items("organizations")
		for i, it := range items {
			if i > 0 {
				x.sink(it.Path, it.Obj)
				continue
			}
			c.Org = []string{it.String("name")}
			units, _ := it.Obj["units"].([]any)
			for j, u := range units {
				uo, ok := u.(object)
				if !ok {
					it.Fail("units/" + strconv.Itoa(j))
					continue
				}
				c.Org = append(c.Org, reader{Obj: uo, Bad: in.Bad}.String("name"))
			}
			setJSID(c, "ORG", jsobj.OwnID(ids[i], 0))
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []string { return c.Org }, func(c *vcard.Card, o []string) { c.Org = o })
	return g
}

type titles struct{ Title, Role string }

func (x *ctx) titlesGroup() group {
	g := group{Name: "titles", Props: []string{"titles"}}
	g.Enc = func(c *vcard.Card, out object) {
		var own, kinds, vals []string
		if c.Title != "" {
			own, kinds, vals = append(own, ownID(c, "TITLE")), append(kinds, "title"), append(vals, c.Title)
		}
		if c.Role != "" {
			own, kinds, vals = append(own, ownID(c, "ROLE")), append(kinds, "role"), append(vals, c.Role)
		}
		if len(own) == 0 {
			return
		}
		ids := jsobj.IDs(own)
		m := object{}
		for i := range ids {
			m[ids[i]] = object{"@type": "Title", "name": vals[i], "kind": kinds[i]}
		}
		out["titles"] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Title, c.Role = "", ""
		ids, items := in.Items("titles")
		n := 0
		for i, it := range items {
			field, prop := &c.Title, "TITLE"
			if it.String("kind") == "role" {
				field, prop = &c.Role, "ROLE"
			}
			if *field != "" || it.String("name") == "" {
				x.sink(it.Path, it.Obj)
				continue
			}
			*field = it.String("name")
			setJSID(c, prop, jsobj.OwnID(ids[i], n))
			n++
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) titles { return titles{c.Title, c.Role} },
		func(c *vcard.Card, t titles) { c.Title, c.Role = t.Title, t.Role })
	return g
}

func (x *ctx) notesGroup() group {
	g := group{Name: "note", Props: []string{"notes"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.Note == "" {
			return
		}
		out["notes"] = object{jsobj.IDs([]string{ownID(c, "NOTE")})[0]: object{"@type": "Note", "note": c.Note}}
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Note = ""
		ids, items := in.Items("notes")
		for i, it := range items {
			if c.Note != "" || it.String("note") == "" {
				x.sink(it.Path, it.Obj)
				continue
			}
			c.Note = it.String("note")
			setJSID(c, "NOTE", jsobj.OwnID(ids[i], 0))
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) string { return c.Note }, func(c *vcard.Card, s string) { c.Note = s })
	return g
}

// --- Dates ---

type dates struct{ Birthday, Anniversary string }

// dateOut writes BDAY or ANNIVERSARY as a PartialDate, or reports false
// for a value that is not a date.
func dateOut(raw string, params []contentline.Param) (object, bool) {
	d, ok := vcard.ParseDate(raw, params)
	if !ok {
		return nil, false
	}
	o := object{"@type": "PartialDate", "month": json.Number(strconv.Itoa(int(d.Month))), "day": json.Number(strconv.Itoa(d.Day))}
	if d.Year != 0 {
		o["year"] = json.Number(strconv.Itoa(d.Year))
	}
	return o, true
}

// dateIn writes a PartialDate as the card's version writes dates.
func dateIn(r reader, version string) (string, bool) {
	month, day, year := r.Int("month"), r.Int("day"), r.Int("year")
	if month == nil || day == nil {
		return "", false
	}
	if version == vcard.V40 {
		if year == nil {
			return fmt.Sprintf("--%02d%02d", *month, *day), true
		}
		return fmt.Sprintf("%04d%02d%02d", *year, *month, *day), true
	}
	if year == nil {
		return fmt.Sprintf("--%02d-%02d", *month, *day), true
	}
	return fmt.Sprintf("%04d-%02d-%02d", *year, *month, *day), true
}

func (x *ctx) datesGroup() group {
	g := group{Name: "dates", Props: []string{"anniversaries", v + "bday", v + "anniversary"}}
	g.Enc = func(c *vcard.Card, out object) {
		var own, kinds []string
		var vals []any
		for _, d := range []struct{ raw, prop, kind string }{{c.Birthday, "BDAY", "birth"}, {c.Anniversary, "ANNIVERSARY", "wedding"}} {
			if d.raw == "" {
				continue
			}
			date, ok := dateOut(d.raw, c.Leftovers.Params[d.prop])
			if !ok {
				out[v+strings.ToLower(d.prop)] = d.raw
				continue
			}
			own, kinds, vals = append(own, ownID(c, d.prop)), append(kinds, d.kind), append(vals, date)
		}
		if len(own) == 0 {
			return
		}
		ids := jsobj.IDs(own)
		m := object{}
		for i := range ids {
			m[ids[i]] = object{"@type": "Anniversary", "kind": kinds[i], "date": vals[i]}
		}
		out["anniversaries"] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Birthday, c.Anniversary = in.String(v+"bday"), in.String(v+"anniversary")
		ids, items := in.Items("anniversaries")
		n := 0
		for i, it := range items {
			field, prop := (*string)(nil), ""
			switch it.String("kind") {
			case "birth":
				field, prop = &c.Birthday, "BDAY"
			case "wedding":
				field, prop = &c.Anniversary, "ANNIVERSARY"
			}
			if field == nil || *field != "" {
				x.sink(it.Path, it.Obj)
				continue
			}
			val, ok := dateIn(it.At("date"), c.Version)
			if !ok {
				x.sink(it.Path, it.Obj)
				continue
			}
			*field = val
			setJSID(c, prop, jsobj.OwnID(ids[i], n))
			n++
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) dates { return dates{c.Birthday, c.Anniversary} },
		func(c *vcard.Card, d dates) { c.Birthday, c.Anniversary = d.Birthday, d.Anniversary })
	return g
}

// revGroup maps REV to updated.
func revGroup() group {
	g := group{Name: "rev", Props: []string{"updated", v + "rev"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.Rev == "" {
			return
		}
		for _, layout := range []string{"20060102T150405Z", time.RFC3339, "2006-01-02T15:04:05Z0700", "20060102T150405Z0700"} {
			if t, err := time.Parse(layout, c.Rev); err == nil {
				out["updated"] = t.UTC().Format("2006-01-02T15:04:05Z")
				return
			}
		}
		out[v+"rev"] = c.Rev
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Rev = in.String(v + "rev")
		if s := in.String("updated"); s != "" && c.Rev == "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				in.Fail("updated")
				return
			}
			c.Rev = t.UTC().Format("20060102T150405Z")
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) string { return c.Rev }, func(c *vcard.Card, s string) { c.Rev = s })
	return g
}

func keywordsGroup() group {
	g := group{Name: "categories", Props: []string{"keywords"}}
	g.Enc = func(c *vcard.Card, out object) {
		if c.Categories == nil {
			return
		}
		set := object{}
		for _, k := range c.Categories {
			set[k] = true
		}
		out["keywords"] = set
	}
	g.Dec = func(in reader, c *vcard.Card) { c.Categories = in.Set("keywords") }
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []string { return c.Categories }, func(c *vcard.Card, s []string) { c.Categories = s })
	return g
}

// --- Emails, phones, URLs, IMPP ---

// contexts are the vCard types that are JSContact contexts.
var contexts = map[string]string{"WORK": "work", "HOME": "private"}

// features are the TEL types that are JSContact phone features.
var features = map[string]string{
	"VOICE": "voice", "FAX": "fax", "CELL": "mobile", "PAGER": "pager", "TEXT": "text", "VIDEO": "video",
	"TEXTPHONE": "textphone", "MAIN-NUMBER": "main-number",
}

// metaOut writes what children share: contexts (and a phone's features)
// from their types, the rest of the types, the preference, the label, the
// group and other parameters.
func metaOut(m *vcard.Meta, o object, phone bool, labelProp string) {
	ctxs, feats := object{}, object{}
	var rest []string
	for _, t := range m.Types {
		up := strings.ToUpper(t)
		switch {
		case contexts[up] != "":
			ctxs[contexts[up]] = true
		case phone && features[up] != "":
			feats[features[up]] = true
		default:
			rest = append(rest, t)
		}
	}
	jsobj.Set(o, "contexts", ctxs)
	jsobj.Set(o, "features", feats)
	if len(rest) > 0 {
		o[v+"types"] = rest
	}
	if m.Pref > 0 {
		o["pref"] = json.Number(strconv.Itoa(m.Pref))
	}
	jsobj.Set(o, labelProp, m.Label)
	jsobj.Set(o, v+"group", m.Group)
	if _, params := jsobj.TakeJSID(m.Params); len(params) > 0 {
		o[jsobj.ParamsKey] = params
	}
}

func metaIn(r reader, m *vcard.Meta, labelProp string) {
	upper := func(set []string, table map[string]string) {
		for _, s := range set {
			t := strings.ToUpper(s)
			for ic, js := range table {
				if js == s {
					t = ic
				}
			}
			m.Types = append(m.Types, t)
		}
	}
	upper(r.Set("contexts"), contexts)
	upper(r.Set("features"), features)
	m.Types = append(m.Types, r.Strings(v+"types")...)
	if p := r.Int("pref"); p != nil {
		m.Pref = *p
	}
	m.Label = r.String(labelProp)
	m.Group = r.String(v + "group")
	r.Decode(jsobj.ParamsKey, &m.Params)
}

func valueIDs(list []vcard.Value) []string {
	own := make([]string, len(list))
	for i := range list {
		own[i], _ = jsobj.TakeJSID(list[i].Params)
	}
	return jsobj.IDs(own)
}

// valuesGroup maps EMAIL, TEL, URL or IMPP to a map of objects of one
// type, the value in prop.
func (x *ctx) valuesGroup(name, typ, prop string, field func(*vcard.Card) *[]vcard.Value, phone bool) group {
	g := group{Name: name, Props: []string{name}}
	g.Enc = func(c *vcard.Card, out object) {
		list := *field(c)
		if len(list) == 0 {
			return
		}
		ids := valueIDs(list)
		m := object{}
		for i := range list {
			o := object{"@type": typ, prop: list[i].Value}
			metaOut(&list[i].Meta, o, phone, "label")
			m[ids[i]] = o
		}
		out[name] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		*field(c) = nil
		ids, items := in.Items(name)
		for i, it := range items {
			val := vcard.Value{Value: it.String(prop)}
			if val.Value == "" {
				it.Fail(prop)
				continue
			}
			metaIn(it, &val.Meta, "label")
			val.Params = jsobj.WithJSID(val.Params, jsobj.OwnID(ids[i], len(*field(c))))
			*field(c) = append(*field(c), val)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []vcard.Value { return *field(c) }, func(c *vcard.Card, l []vcard.Value) { *field(c) = l })
	return g
}

// --- Addresses ---

// addressKinds are the AddressComponent kinds of ADR's components, in
// ADR's order.
var addressKinds = []string{"postOfficeBox", "apartment", "name", "locality", "region", "postcode", "country"}

// addressKindsIn reads the kinds JSContact has and ADR does not into the
// component nearest in meaning.
var addressKindsIn = map[string]int{
	"postOfficeBox": 0, "apartment": 1, "room": 1, "floor": 1, "building": 1,
	"name": 2, "number": 2, "block": 2, "direction": 2, "landmark": 2,
	"locality": 3, "district": 3, "subdistrict": 3, "region": 4, "postcode": 5, "country": 6,
}

func addressComponents(a *vcard.Address) []any {
	comps := []any{}
	for k, list := range [][]string{a.POBox, a.Ext, a.Street, a.Locality, a.Region, a.Code, a.Country} {
		for _, val := range list {
			comps = append(comps, object{"@type": "AddressComponent", "kind": addressKinds[k], "value": val})
		}
	}
	return comps
}

// addressOf reads AddressComponents into an ADR's components.
func addressOf(list []any, r reader, a *vcard.Address) {
	fields := []*[]string{&a.POBox, &a.Ext, &a.Street, &a.Locality, &a.Region, &a.Code, &a.Country}
	for _, f := range fields {
		*f = nil
	}
	for j, item := range list {
		co, ok := item.(object)
		if !ok {
			r.Fail("components/" + strconv.Itoa(j))
			continue
		}
		cr := reader{Obj: co, Path: r.Path + "/components/" + strconv.Itoa(j), Bad: r.Bad}
		if k, ok := addressKindsIn[cr.String("kind")]; ok {
			*fields[k] = append(*fields[k], cr.String("value"))
		}
	}
}

func (x *ctx) addressesGroup() group {
	g := group{Name: "addresses", Props: []string{"addresses"}}
	g.Enc = func(c *vcard.Card, out object) {
		if len(c.Addresses) == 0 {
			return
		}
		own := make([]string, len(c.Addresses))
		for i := range c.Addresses {
			own[i], _ = jsobj.TakeJSID(c.Addresses[i].Params)
		}
		ids := jsobj.IDs(own)
		m := object{}
		for i := range c.Addresses {
			a := c.Addresses[i]
			comps := addressComponents(&a)
			if kept := stashed(a.Params); kept != nil {
				var probe vcard.Address
				addressOf(kept, reader{}, &probe)
				if jsobj.Equal(addressComponents(&probe), comps) {
					comps = kept
				}
			}
			a.Params = stash(a.Params, nil, nil)
			o := object{"@type": "Address", "components": comps}
			metaOut(&a.Meta, o, false, v+"label")
			m[ids[i]] = o
		}
		out["addresses"] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Addresses = nil
		ids, items := in.Items("addresses")
		for i, it := range items {
			var a vcard.Address
			list, _ := it.Obj["components"].([]any)
			addressOf(list, it, &a)
			metaIn(it, &a.Meta, v+"label")
			a.Params = stash(a.Params, list, addressComponents(&a))
			a.Params = jsobj.WithJSID(a.Params, jsobj.OwnID(ids[i], len(c.Addresses)))
			c.Addresses = append(c.Addresses, a)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []vcard.Address { return c.Addresses }, func(c *vcard.Card, l []vcard.Address) { c.Addresses = l })
	return g
}

// --- Related and members ---

// relatedGroup maps RELATED: its value is the key, its types the relation.
func (x *ctx) relatedGroup() group {
	g := group{Name: "related", Props: []string{"relatedTo"}}
	g.Enc = func(c *vcard.Card, out object) {
		if len(c.Related) == 0 {
			return
		}
		m := object{}
		for i := range c.Related {
			r := &c.Related[i]
			o := object{"@type": "Relation"}
			rel := object{}
			for _, t := range r.Types {
				rel[strings.ToLower(t)] = true
			}
			o["relation"] = rel
			if r.Pref > 0 {
				o[v+"pref"] = json.Number(strconv.Itoa(r.Pref))
			}
			jsobj.Set(o, v+"label", r.Label)
			jsobj.Set(o, v+"group", r.Group)
			if len(r.Params) > 0 {
				o[jsobj.ParamsKey] = r.Params
			}
			m[r.Value] = o
		}
		out["relatedTo"] = m
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Related = nil
		ids, items := in.Items("relatedTo")
		for i, it := range items {
			r := vcard.Value{Value: ids[i]}
			for _, t := range it.Set("relation") {
				r.Types = append(r.Types, strings.ToUpper(t))
			}
			if p := it.Int(v + "pref"); p != nil {
				r.Pref = *p
			}
			r.Label, r.Group = it.String(v+"label"), it.String(v+"group")
			it.Decode(jsobj.ParamsKey, &r.Params)
			c.Related = append(c.Related, r)
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []vcard.Value { return c.Related }, func(c *vcard.Card, l []vcard.Value) { c.Related = l })
	return g
}

func membersGroup() group {
	g := group{Name: "members", Props: []string{"members", v + "memberParams"}}
	g.Enc = func(c *vcard.Card, out object) {
		if len(c.Members) == 0 {
			return
		}
		set, params := object{}, object{}
		for _, m := range c.Members {
			set[m.URI] = true
			if len(m.Params) > 0 {
				params[m.URI] = m.Params
			}
		}
		out["members"] = set
		jsobj.Set(out, v+"memberParams", params)
	}
	g.Dec = func(in reader, c *vcard.Card) {
		c.Members = nil
		var params map[string][]contentline.Param
		in.Decode(v+"memberParams", &params)
		for _, uri := range in.Set("members") {
			c.Members = append(c.Members, vcard.Member{URI: uri, Params: params[uri]})
		}
	}
	g.Snap, g.Load = jsobj.Fields(func(c *vcard.Card) []vcard.Member { return c.Members }, func(c *vcard.Card, m []vcard.Member) { c.Members = m })
	return g
}

// --- Photo ---

// photoOut writes PHOTO as a Media of kind photo: a data: URI for an inline
// photo, the URL for one given by URL. How the card wrote its type and
// encoding is kept beside it, so the photo is written back the same way.
func photoOut(c *vcard.Card) object {
	ph := c.Photo
	m := object{"@type": "Media", "kind": "photo"}
	if ph.URI != "" {
		m["uri"] = ph.URI
	} else {
		mt := ph.MediaType()
		m["uri"] = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(ph.Data)
		if ph.Type != mt {
			jsobj.Set(m, v+"type", ph.Type)
		}
		jsobj.Set(m, v+"encoding", ph.Encoding)
	}
	if _, params := jsobj.TakeJSID(ph.Params); len(params) > 0 {
		m[jsobj.ParamsKey] = params
	}
	id, _ := jsobj.TakeJSID(ph.Params)
	return object{jsobj.IDs([]string{id})[0]: m}
}

// photoIn reads the first Media of kind photo as PHOTO; other media are
// kept as sent.
func (x *ctx) photoIn(in reader, c *vcard.Card) {
	c.Photo = nil
	ids, items := in.Items("media")
	for i, it := range items {
		uri := it.String("uri")
		if c.Photo != nil || it.String("kind") != "photo" || uri == "" {
			x.sink(it.Path, it.Obj)
			continue
		}
		ph := &vcard.Photo{}
		it.Decode(jsobj.ParamsKey, &ph.Params)
		if rest, ok := strings.CutPrefix(uri, "data:"); ok {
			typ, data, ok := strings.Cut(rest, ";base64,")
			raw, err := base64.StdEncoding.DecodeString(data)
			if !ok || err != nil {
				it.Fail("uri")
				continue
			}
			ph.Data = raw
			ph.Type = typ
			if c.Version != vcard.V40 {
				// 2.1 and 3.0 write the subtype: TYPE=JPEG.
				ph.Type = strings.ToUpper(strings.TrimPrefix(typ, "image/"))
			}
			if t := it.String(v + "type"); t != "" && (&vcard.Photo{Type: t}).MediaType() == typ {
				ph.Type = t
			}
			ph.Encoding = it.String(v + "encoding")
		} else {
			ph.URI = uri
		}
		ph.Params = jsobj.WithJSID(ph.Params, jsobj.OwnID(ids[i], 0))
		c.Photo = ph
	}
}
