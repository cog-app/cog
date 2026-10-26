// Package vcard maps vCards (vCard 2.1, RFC 2426 and RFC 6350) between the
// generic contentline tree and Cog's contact model: what Cog understands
// becomes typed fields, and everything else is kept as leftovers and written
// back in its original order. It holds no SQL; the contacts plugin stores the
// model.
//
// A card keeps the version it was received in and is written back in it:
// clients that sent 3.0 expect 3.0. The version decides how values are
// escaped, how parameters are written, and which name a property has
// (KIND or X-ADDRESSBOOKSERVER-KIND).
package vcard

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// ProdID names Cog in cards it creates.
const ProdID = "-//Cog//Cog Contacts//EN"

// Versions Cog knows how to write. Any other is kept and written back, and
// read as 3.0.
const (
	V21 = "2.1"
	V30 = "3.0"
	V40 = "4.0"
)

// Meta is what EMAIL, TEL, ADR, URL, IMPP and RELATED share: their types,
// preference, Apple's property group and the label paired with it, and the
// parameters Cog does not model.
type Meta struct {
	// Types are the TYPE values as written, in order ("INTERNET", "pref",
	// "work"); in vCard 2.1 the bare parameters ("TEL;HOME;VOICE").
	Types []string `json:"types,omitempty"`
	// Pref is the PREF parameter (1 most preferred), 0 when absent.
	Pref int `json:"pref,omitempty"`
	// Group is the property group: "item1" in "item1.EMAIL".
	Group string `json:"group,omitempty"`
	// Label is the X-ABLabel in the same group, when the group holds just
	// this property and its label. iOS shows it as the custom label.
	Label  string              `json:"label,omitempty"`
	Params []contentline.Param `json:"params,omitempty"` // others, in order
}

// HasType reports whether t is among the types, ignoring case.
func (m *Meta) HasType(t string) bool {
	return slices.ContainsFunc(m.Types, func(s string) bool { return strings.EqualFold(s, t) })
}

// Value is an EMAIL, TEL, URL, IMPP or RELATED property. Its value is kept
// as written: these are addresses and URIs, and TEXT escaping a URI breaks
// it.
type Value struct {
	Meta
	Value string `json:"value"`
}

// Address is an ADR property. Each component is a list of values (RFC 6350
// lets a component hold several, separated by commas); in vCard 2.1 a
// component is one value.
type Address struct {
	Meta
	POBox    []string
	Ext      []string
	Street   []string
	Locality []string
	Region   []string
	Code     []string
	Country  []string
}

func (a *Address) components() []*[]string {
	return []*[]string{&a.POBox, &a.Ext, &a.Street, &a.Locality, &a.Region, &a.Code, &a.Country}
}

// Name is the N property: lists of family names, given names, additional
// names, honorific prefixes and suffixes.
type Name struct {
	Family     []string
	Given      []string
	Additional []string
	Prefix     []string
	Suffix     []string
}

func (n *Name) components() []*[]string {
	return []*[]string{&n.Family, &n.Given, &n.Additional, &n.Prefix, &n.Suffix}
}

// Formatted returns the name as it is usually shown: prefix, given,
// additional, family, suffix.
func (n *Name) Formatted() string {
	var parts []string
	for _, c := range [][]string{n.Prefix, n.Given, n.Additional, n.Family, n.Suffix} {
		for _, v := range c {
			if v = strings.TrimSpace(v); v != "" {
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// Member is one member of a group card: MEMBER (4.0) or
// X-ADDRESSBOOKSERVER-MEMBER (Apple's, in 3.0).
type Member struct {
	URI    string              `json:"uri"`
	Params []contentline.Param `json:"params,omitempty"`
}

// Photo is an inline PHOTO: base64 with ENCODING in 2.1 and 3.0, a data: URI
// in 4.0. A photo given by URL is a leftover.
type Photo struct {
	Data []byte
	// Type is the media type as written: TYPE ("JPEG") in 2.1 and 3.0, the
	// data: URI's ("image/jpeg") in 4.0.
	Type string
	// Encoding is the ENCODING token as written ("b", "BASE64"); "" in 4.0.
	Encoding string
	Params   []contentline.Param // others, in order
}

// MediaType returns the photo's MIME type, or "" when it has none.
func (p *Photo) MediaType() string {
	switch t := strings.ToLower(p.Type); {
	case t == "":
		return ""
	case strings.Contains(t, "/"):
		t, _, _ = strings.Cut(t, ";")
		return t
	default:
		return "image/" + t
	}
}

// Card is one vCard. Fields Cog does not model stay among the leftovers.
type Card struct {
	// Version is VERSION as received ("2.1", "3.0", "4.0"); "" when absent.
	Version string
	UID     string
	// Kind is KIND in 4.0 and X-ADDRESSBOOKSERVER-KIND otherwise:
	// "individual", "group", "org" or "location".
	Kind string

	FN        string
	N         *Name
	Nicknames []string
	// Org holds ORG's components: the organisation, then its units.
	Org   []string
	Title string
	Role  string
	Note  string

	// Dates and GENDER as written; their syntax differs between versions.
	Birthday    string
	Anniversary string
	Gender      string
	Rev         string

	Categories []string
	Emails     []Value
	Phones     []Value
	Addresses  []Address
	URLs       []Value
	IMPPs      []Value
	Related    []Value
	Members    []Member
	Photo      *Photo

	Leftovers codec.Leftovers
}

// is21 reports whether the card is vCard 2.1, whose values are escaped and
// whose parameters are written differently.
func (c *Card) is21() bool { return c.Version == V21 }

// is40 reports whether the card is vCard 4.0.
func (c *Card) is40() bool { return c.Version == V40 }

func (c *Card) kindName() string {
	if c.is40() {
		return "KIND"
	}
	return "X-ADDRESSBOOKSERVER-KIND"
}

func (c *Card) memberName() string {
	if c.is40() {
		return "MEMBER"
	}
	return "X-ADDRESSBOOKSERVER-MEMBER"
}

// IsGroup reports whether the card is a group of other cards.
func (c *Card) IsGroup() bool { return strings.EqualFold(c.Kind, "group") }

// DisplayName returns FN, or the formatted N, or the organisation.
func (c *Card) DisplayName() string {
	switch {
	case c.FN != "":
		return c.FN
	case c.N != nil && c.N.Formatted() != "":
		return c.N.Formatted()
	case len(c.Org) > 0:
		return c.Org[0]
	}
	return ""
}

// --- Text values ---

// unescape decodes a TEXT value. vCard 2.1 escapes only semicolons, and
// carries line breaks in quoted-printable; 3.0 and 4.0 escape as iCalendar
// does, and Cog keeps their line breaks as "\n".
func (c *Card) unescape(s string) string {
	if c.is21() {
		if !strings.Contains(s, `\;`) {
			return s
		}
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) && s[i+1] == ';' {
				i++
			}
			b.WriteByte(s[i])
		}
		return b.String()
	}
	s = codec.UnescapeText(s)
	if strings.Contains(s, "\r") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	}
	return s
}

func (c *Card) escape(s string) string {
	if c.is21() {
		return strings.ReplaceAll(s, ";", `\;`)
	}
	return codec.EscapeText(s)
}

// list reads a list of TEXT values: comma-separated, except in 2.1, which
// has no lists. An empty value is an empty list.
func (c *Card) list(s string) []string {
	if s == "" {
		return nil
	}
	if c.is21() {
		return []string{c.unescape(s)}
	}
	parts := codec.Split(s, ',')
	for i, p := range parts {
		parts[i] = c.unescape(p)
	}
	return parts
}

func (c *Card) joinList(vals []string) string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = c.escape(v)
	}
	return strings.Join(out, ",")
}

// structured reads a structured value (N, ADR) of at most n components, each
// a list, or reports false when it has more that are not empty.
func (c *Card) structured(s string, n int) ([][]string, bool) {
	out, ok := c.splitStructured(s, n)
	if !ok || !c.is21() {
		return out, ok
	}
	// vCard 2.1 cannot escape a backslash, so a component ending in one
	// would escape the semicolon after it when written back.
	again, _ := c.splitStructured(c.joinStructured(pointers(out)), n)
	return out, slices.EqualFunc(out, again, slices.Equal)
}

func (c *Card) splitStructured(s string, n int) ([][]string, bool) {
	parts := codec.Split(s, ';')
	// Empty components past the last carry nothing (Tine 2.0 adds one).
	for len(parts) > n && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > n {
		return nil, false
	}
	out := make([][]string, n)
	for i, p := range parts {
		out[i] = c.list(p)
	}
	return out, true
}

func pointers(comps [][]string) []*[]string {
	out := make([]*[]string, len(comps))
	for i := range comps {
		out[i] = &comps[i]
	}
	return out
}

func (c *Card) joinStructured(comps []*[]string) string {
	parts := make([]string, len(comps))
	for i, p := range comps {
		parts[i] = c.joinList(*p)
	}
	return strings.Join(parts, ";")
}

// encodings are the bare 2.1 parameters that name a transfer encoding rather
// than a type.
var encodings = []string{"QUOTED-PRINTABLE", "BASE64", "B", "8BIT", "7BIT"}

func isEncoding(s string) bool {
	return slices.ContainsFunc(encodings, func(e string) bool { return strings.EqualFold(e, s) })
}

func isQP(params []contentline.Param) bool {
	for _, p := range params {
		if p.Name == "" || strings.EqualFold(p.Name, "ENCODING") {
			for _, v := range p.Values {
				if strings.EqualFold(v, "QUOTED-PRINTABLE") {
					return true
				}
			}
		}
	}
	return false
}

// readable returns a property's value with its quoted-printable encoding
// removed, or false when Cog cannot read it as UTF-8: a CHARSET other than
// UTF-8, or bytes that are not UTF-8. Such a property stays a leftover, so
// it is written back byte for byte.
func readable(p *contentline.Property) (string, bool) {
	v := p.Value
	if isQP(p.Params) {
		v = qpDecode(v)
	}
	if cs := p.Param("CHARSET"); cs != "" && !strings.EqualFold(cs, "UTF-8") {
		return "", false
	}
	return v, utf8.ValidString(v)
}

func unhex(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	}
	return 0, false
}

// qpDecode decodes quoted-printable whose soft line breaks contentline has
// already joined. An "=" that starts no escape is kept.
func qpDecode(s string) string {
	if !strings.Contains(s, "=") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				b.WriteByte(hi<<4 | lo)
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// qpEncode encodes s as quoted-printable; contentline adds the soft line
// breaks.
func qpEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		literal := c >= 33 && c <= 126 && c != '='
		if (c == ' ' || c == '\t') && i+1 < len(s) && s[i+1] != '\r' && s[i+1] != '\n' {
			literal = true
		}
		if literal {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('=')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// --- Decoding ---

// labelName is Apple's label property, paired with a property in the same
// group.
const labelName = "X-ABLabel"

// Decode reads a VCARD.
func Decode(v *contentline.Component) *Card {
	c := &Card{}
	// The version decides how everything else is read.
	for i := range v.Props {
		if p := &v.Props[i]; p.Is("VERSION") && p.Group == "" {
			c.Version = strings.TrimSpace(p.Value)
			break
		}
	}
	l := &c.Leftovers
	claimed := map[string]bool{}
	// What each group holds, for pairing labels.
	type group struct {
		others int
		labels []int // indexes into l.Props
	}
	groups := map[string]*group{}
	groupOf := func(name string) *group {
		k := strings.ToLower(name)
		g := groups[k]
		if g == nil {
			g = &group{}
			groups[k] = g
		}
		return g
	}
	for i := range v.Props {
		p := &v.Props[i]
		if p.Group != "" && p.Is(labelName) {
			g := groupOf(p.Group)
			g.labels = append(g.labels, len(l.Props))
			l.Props = append(l.Props, *p)
			continue
		}
		if p.Group != "" {
			groupOf(p.Group).others++
		}
		if !c.claim(p, claimed) {
			l.Props = append(l.Props, *p)
		}
	}
	// A label pairs with the only other property in its group, when Cog
	// modelled it.
	var paired []int
	for _, m := range c.children() {
		if m.Group == "" {
			continue
		}
		g := groups[strings.ToLower(m.Group)]
		if g.others != 1 || len(g.labels) != 1 {
			continue
		}
		lp := &l.Props[g.labels[0]]
		text, ok := readable(lp)
		if !ok || len(lp.Params) > 0 || text == "" {
			continue
		}
		m.Label = c.unescape(text)
		paired = append(paired, g.labels[0])
	}
	if len(paired) > 0 {
		var props []contentline.Property
		for i, p := range l.Props {
			if !slices.Contains(paired, i) {
				props = append(props, p)
			}
		}
		l.Props = props
	}
	l.Comps = append(l.Comps, v.Components...)
	return c
}

// claim moves a property into c's fields and reports whether it did.
func (c *Card) claim(p *contentline.Property, claimed map[string]bool) bool {
	if p.Malformed() {
		return false
	}
	name := strings.ToUpper(p.Name)
	v, ok := readable(p)
	if !ok || v == "" {
		return false
	}
	l := &c.Leftovers
	once := func() bool {
		if claimed[name] {
			return false
		}
		claimed[name] = true
		return true
	}
	single := func(field *string, value string) bool {
		if p.Group != "" || !once() {
			return false
		}
		*field = value
		l.KeepParams(name, p.Params)
		return true
	}
	child := func(list *[]Value) bool {
		val := Value{Meta: c.meta(p.Params), Value: v}
		val.Group = p.Group
		*list = append(*list, val)
		return true
	}
	switch name {
	case "VERSION":
		return single(new(string), v)
	case "UID":
		return single(&c.UID, v)
	case "FN":
		return single(&c.FN, c.unescape(v))
	case "TITLE":
		return single(&c.Title, c.unescape(v))
	case "ROLE":
		return single(&c.Role, c.unescape(v))
	case "NOTE":
		return single(&c.Note, c.unescape(v))
	case "BDAY":
		return single(&c.Birthday, v)
	case "ANNIVERSARY":
		return single(&c.Anniversary, v)
	case "GENDER":
		return single(&c.Gender, v)
	case "REV":
		return single(&c.Rev, v)
	case "KIND", "X-ADDRESSBOOKSERVER-KIND":
		if !strings.EqualFold(name, c.kindName()) {
			return false
		}
		return single(&c.Kind, v)
	case "N":
		comps, ok := c.structured(v, 5)
		if !ok || p.Group != "" || !once() {
			return false
		}
		n := &Name{}
		for i, f := range n.components() {
			*f = comps[i]
		}
		c.N = n
		l.KeepParams(name, p.Params)
		return true
	case "ORG":
		if p.Group != "" || !once() {
			return false
		}
		for _, part := range codec.Split(v, ';') {
			c.Org = append(c.Org, c.unescape(part))
		}
		l.KeepParams(name, p.Params)
		return true
	case "NICKNAME":
		if p.Group != "" || !once() {
			return false
		}
		c.Nicknames = c.list(v)
		l.KeepParams(name, p.Params)
		return true
	case "CATEGORIES":
		if p.Group != "" || !once() {
			return false
		}
		c.Categories = c.list(v)
		l.KeepParams(name, p.Params)
		return true
	case "EMAIL":
		return child(&c.Emails)
	case "TEL":
		return child(&c.Phones)
	case "URL":
		return child(&c.URLs)
	case "IMPP":
		return child(&c.IMPPs)
	case "RELATED":
		return child(&c.Related)
	case "ADR":
		comps, ok := c.structured(v, 7)
		if !ok {
			return false
		}
		a := Address{Meta: c.meta(p.Params)}
		a.Group = p.Group
		for i, f := range a.components() {
			*f = comps[i]
		}
		c.Addresses = append(c.Addresses, a)
		return true
	case "MEMBER", "X-ADDRESSBOOKSERVER-MEMBER":
		if !strings.EqualFold(name, c.memberName()) || p.Group != "" {
			return false
		}
		c.Members = append(c.Members, Member{URI: v, Params: p.Params})
		return true
	case "PHOTO":
		if p.Group != "" || claimed[name] {
			return false
		}
		ph, ok := c.photo(p)
		if !ok {
			return false
		}
		claimed[name] = true
		c.Photo = ph
		return true
	}
	return false
}

// meta splits a child property's parameters into what Cog models and the
// rest. Types come from TYPE, and in 2.1 from bare parameters too.
func (c *Card) meta(params []contentline.Param) Meta {
	var m Meta
	for _, p := range params {
		switch {
		case strings.EqualFold(p.Name, "TYPE"):
			m.Types = append(m.Types, p.Values...)
			continue
		case p.Name == "" && c.is21() && len(p.Values) == 1 && !isEncoding(p.Values[0]):
			m.Types = append(m.Types, p.Values[0])
			continue
		case strings.EqualFold(p.Name, "PREF") && m.Pref == 0 && len(p.Values) == 1:
			if n, err := strconv.Atoi(p.Values[0]); err == nil && n >= 1 && n <= 100 && strconv.Itoa(n) == p.Values[0] {
				m.Pref = n
				continue
			}
		}
		m.Params = append(m.Params, p)
	}
	return m
}

// photo reads an inline PHOTO in the form the card's version writes it, or
// reports false.
func (c *Card) photo(p *contentline.Property) (*Photo, bool) {
	ph := &Photo{}
	if c.is40() {
		rest, ok := strings.CutPrefix(p.Value, "data:")
		if !ok {
			return nil, false
		}
		typ, data, ok := strings.Cut(rest, ";base64,")
		if !ok || strings.Contains(typ, ",") || p.Param("ENCODING") != "" {
			return nil, false
		}
		ph.Type = typ
		if ph.Data, ok = decodeBase64(data); !ok {
			return nil, false
		}
		ph.Params = p.Params
		return ph, true
	}
	typed := false
	for _, pa := range p.Params {
		switch {
		case ph.Encoding == "" && len(pa.Values) == 1 && (strings.EqualFold(pa.Name, "ENCODING") || pa.Name == "" && isEncoding(pa.Values[0])):
			ph.Encoding = pa.Values[0]
		case !typed && len(pa.Values) == 1 && (strings.EqualFold(pa.Name, "TYPE") || pa.Name == "" && !isEncoding(pa.Values[0])):
			ph.Type, typed = pa.Values[0], true
		default:
			ph.Params = append(ph.Params, pa)
		}
	}
	if !strings.EqualFold(ph.Encoding, "b") && !strings.EqualFold(ph.Encoding, "BASE64") {
		return nil, false
	}
	if strings.HasPrefix(p.Value, "data:") {
		return nil, false
	}
	var ok bool
	if ph.Data, ok = decodeBase64(p.Value); !ok {
		return nil, false
	}
	return ph, true
}

func decodeBase64(s string) ([]byte, bool) {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return nil, false
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, true
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return b, true
	}
	return nil, false
}

// --- Encoding ---

// Encode writes the card as a VCARD: VERSION, the modelled properties in a
// fixed order (each child followed by its label), then the leftovers in the
// order received.
func (c *Card) Encode() *contentline.Component {
	v := &contentline.Component{Name: "VCARD"}
	l := &c.Leftovers
	add := func(p contentline.Property) {
		if isQP(p.Params) {
			p.Value = qpEncode(p.Value)
		} else if c.is21() && strings.ContainsAny(p.Value, "\r\n") {
			// 2.1 has no escape for a line break; quoted-printable carries
			// it.
			p.Params = append(p.Params, contentline.Param{Name: "ENCODING", Values: []string{"QUOTED-PRINTABLE"}})
			if p.Param("CHARSET") == "" && !isASCII(p.Value) {
				p.Params = append(p.Params, contentline.Param{Name: "CHARSET", Values: []string{"UTF-8"}})
			}
			p.Value = qpEncode(p.Value)
		}
		v.Props = append(v.Props, p)
	}
	single := func(name, value string) {
		if value != "" {
			add(contentline.Property{Name: name, Params: l.Params[name], Value: value})
		}
	}
	text := func(name, value string) {
		if value != "" {
			single(name, c.escape(value))
		}
	}

	single("VERSION", c.Version)
	single("UID", c.UID)
	if c.Kind != "" {
		add(contentline.Property{Name: c.kindName(), Params: l.Params[strings.ToUpper(c.kindName())], Value: c.Kind})
	}
	text("FN", c.FN)
	if c.N != nil {
		add(contentline.Property{Name: "N", Params: l.Params["N"], Value: c.joinStructured(c.N.components())})
	}
	if c.Nicknames != nil {
		single("NICKNAME", c.joinList(c.Nicknames))
	}
	if c.Org != nil {
		parts := make([]string, len(c.Org))
		for i, o := range c.Org {
			parts[i] = c.escape(o)
		}
		single("ORG", strings.Join(parts, ";"))
	}
	text("TITLE", c.Title)
	text("ROLE", c.Role)
	single("BDAY", c.Birthday)
	single("ANNIVERSARY", c.Anniversary)
	single("GENDER", c.Gender)

	groups := c.newGroups()
	child := func(name string, m *Meta, value string) {
		group := m.Group
		if group == "" {
			group = groups[m]
		}
		add(contentline.Property{Group: group, Name: name, Params: c.metaParams(m), Value: value})
		if m.Label != "" {
			add(contentline.Property{Group: group, Name: labelName, Value: c.escape(m.Label)})
		}
	}
	for i := range c.Emails {
		child("EMAIL", &c.Emails[i].Meta, c.Emails[i].Value)
	}
	for i := range c.Phones {
		child("TEL", &c.Phones[i].Meta, c.Phones[i].Value)
	}
	for i := range c.Addresses {
		a := &c.Addresses[i]
		child("ADR", &a.Meta, c.joinStructured(a.components()))
	}
	for i := range c.URLs {
		child("URL", &c.URLs[i].Meta, c.URLs[i].Value)
	}
	for i := range c.IMPPs {
		child("IMPP", &c.IMPPs[i].Meta, c.IMPPs[i].Value)
	}
	for i := range c.Related {
		child("RELATED", &c.Related[i].Meta, c.Related[i].Value)
	}
	for _, m := range c.Members {
		add(contentline.Property{Name: c.memberName(), Params: m.Params, Value: m.URI})
	}
	if c.Categories != nil {
		single("CATEGORIES", c.joinList(c.Categories))
	}
	text("NOTE", c.Note)
	if ph := c.Photo; ph != nil {
		v.Props = append(v.Props, c.photoProperty(ph))
	}
	single("REV", c.Rev)
	v.Props = append(v.Props, l.Props...)
	v.Components = append(v.Components, l.Comps...)
	return v
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func (c *Card) metaParams(m *Meta) []contentline.Param {
	var params []contentline.Param
	if len(m.Types) > 0 {
		if c.is21() {
			for _, t := range m.Types {
				params = append(params, contentline.Param{Values: []string{t}})
			}
		} else {
			params = append(params, contentline.Param{Name: "TYPE", Values: m.Types})
		}
	}
	if m.Pref > 0 {
		params = append(params, contentline.Param{Name: "PREF", Values: []string{strconv.Itoa(m.Pref)}})
	}
	return append(params, m.Params...)
}

func (c *Card) photoProperty(ph *Photo) contentline.Property {
	data := base64.StdEncoding.EncodeToString(ph.Data)
	if c.is40() {
		return contentline.Property{Name: "PHOTO", Params: ph.Params, Value: "data:" + ph.Type + ";base64," + data}
	}
	enc := ph.Encoding
	if enc == "" {
		enc = "b"
		if c.is21() {
			enc = "BASE64"
		}
	}
	params := []contentline.Param{{Name: "ENCODING", Values: []string{enc}}}
	if ph.Type != "" {
		params = append(params, contentline.Param{Name: "TYPE", Values: []string{ph.Type}})
	}
	return contentline.Property{Name: "PHOTO", Params: append(params, ph.Params...), Value: data}
}

// children returns the Meta of every child property, in output order.
func (c *Card) children() []*Meta {
	var out []*Meta
	for _, list := range [][]Value{c.Emails, c.Phones} {
		for i := range list {
			out = append(out, &list[i].Meta)
		}
	}
	for i := range c.Addresses {
		out = append(out, &c.Addresses[i].Meta)
	}
	for _, list := range [][]Value{c.URLs, c.IMPPs, c.Related} {
		for i := range list {
			out = append(out, &list[i].Meta)
		}
	}
	return out
}

// newGroups names a group for every labelled child that has none: "itemN",
// numbered after the groups the card uses.
func (c *Card) newGroups() map[*Meta]string {
	var need []*Meta
	for _, m := range c.children() {
		if m.Label != "" && m.Group == "" {
			need = append(need, m)
		}
	}
	if len(need) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, m := range c.children() {
		used[strings.ToLower(m.Group)] = true
	}
	for _, p := range c.Leftovers.Props {
		used[strings.ToLower(p.Group)] = true
	}
	out := make(map[*Meta]string, len(need))
	n := 1
	for _, m := range need {
		for used["item"+strconv.Itoa(n)] {
			n++
		}
		out[m] = "item" + strconv.Itoa(n)
		used[out[m]] = true
	}
	return out
}

// AssignGroups gives every labelled child without a group the one Encode
// would write it in, so the stored card reads back the same.
func (c *Card) AssignGroups() {
	for m, g := range c.newGroups() {
		m.Group = g
	}
}

// --- Edits ---

// Dependent leftovers: vendor copies of a modelled field, which become wrong
// when Cog edits that field (DESIGN/dav-storage.md section 3.2). A DAV PUT
// replaces the whole card and does not need these.
var dependents = map[string]struct {
	props  []string
	params []string // on the field's own property
}{
	"FN":  {props: []string{"X-PHONETIC-FIRST-NAME", "X-PHONETIC-MIDDLE-NAME", "X-PHONETIC-LAST-NAME", "SORT-STRING"}},
	"N":   {props: []string{"X-PHONETIC-FIRST-NAME", "X-PHONETIC-MIDDLE-NAME", "X-PHONETIC-LAST-NAME", "SORT-STRING"}, params: []string{"SORT-AS"}},
	"ORG": {props: []string{"X-PHONETIC-ORG"}, params: []string{"SORT-AS"}},
}

// Changed drops the leftovers that copy a field Cog changed, named by its
// property.
func (c *Card) Changed(fields ...string) {
	for _, f := range fields {
		d := dependents[f]
		c.Leftovers.Drop(d.props...)
		c.Leftovers.DropParams(f, d.params...)
	}
}

// Stamp records an edit made by Cog: REV moves to now.
func (c *Card) Stamp(now time.Time) {
	c.Rev = now.UTC().Format("20060102T150405Z")
}
