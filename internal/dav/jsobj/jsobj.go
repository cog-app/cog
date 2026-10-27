// Package jsobj holds what the JSCalendar (internal/dav/jscal) and JSContact
// (internal/dav/jscontact) codecs share: JSON objects as plain trees, typed
// reads that collect invalid properties, the ordering of map ids, and the
// two ways nothing is lost in either direction (DESIGN/dav-storage.md
// section 9):
//
//   - what the iCalendar or vCard model holds that JSON has no property for
//     travels in vendor properties ("cog:leftovers", "cog:params", ...);
//   - what a JSON client sends that the model has no field for is kept as a
//     JSPROP property (draft-ietf-calext-jscalendar-icalendar, RFC 9555
//     section 3.3) whose JSPTR parameter is a JSON pointer to it, and put
//     back at that pointer on the way out.
//
// A third, Exact, keeps how a value was written when JSON can only say what
// it means (a TZID spelled one way, parameters in an order), for as long as
// the client leaves that value alone.
package jsobj

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// Object is a JSON object as a tree of plain values: Object, []any, string,
// json.Number, bool and nil.
type Object = map[string]any

// Vendor prefixes Cog's own properties.
const Vendor = "cog:"

// ExactKey holds the exact forms of a component's or card's groups.
const ExactKey = Vendor + "exact"

// LeftoversKey holds what the codec did not model (codec.Leftovers).
const LeftoversKey = Vendor + "leftovers"

// ParamsKey holds a property's parameters Cog does not model, on the object
// the property became.
const ParamsKey = Vendor + "params"

// Tree turns v into a plain tree, numbers kept as written.
func Tree(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads JSON into a plain tree, numbers kept as written.
func Parse(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var out any
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ParseObject reads a JSON object into a plain tree.
func ParseObject(b []byte) (Object, error) {
	v, err := Parse(b)
	if err != nil {
		return nil, err
	}
	o, ok := v.(Object)
	if !ok {
		return nil, fmt.Errorf("not a JSON object")
	}
	return o, nil
}

// Equal reports whether two values are the same JSON.
func Equal(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

// Clone returns a deep copy of a plain tree.
func Clone(v any) any {
	switch t := v.(type) {
	case Object:
		out := make(Object, len(t))
		for k, x := range t {
			out[k] = Clone(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = Clone(x)
		}
		return out
	}
	return v
}

// Pick returns the properties of o among keys that o has.
func Pick(o Object, keys ...string) Object {
	out := Object{}
	for _, k := range keys {
		if v, ok := o[k]; ok {
			out[k] = v
		}
	}
	return out
}

// Set puts v at key unless it is the zero value of its type, which JSON
// leaves out.
func Set(o Object, key string, v any) {
	switch t := v.(type) {
	case nil:
		return
	case string:
		if t == "" {
			return
		}
	case bool:
		if !t {
			return
		}
	case Object:
		if len(t) == 0 {
			return
		}
	case []any:
		if len(t) == 0 {
			return
		}
	case *int:
		if t == nil {
			return
		}
		o[key] = json.Number(strconv.Itoa(*t))
		return
	}
	o[key] = v
}

// --- Reading ---

// Reader reads the properties of one JSON object and records those of the
// wrong type, by JSON pointer.
type Reader struct {
	Obj  Object
	Path string // pointer to Obj
	Bad  *[]string
}

// At returns a reader of a property's object, or of an empty one.
func (r Reader) At(key string) Reader {
	o, _ := r.Object(key)
	return Reader{Obj: o, Path: r.Path + "/" + Escape(key), Bad: r.Bad}
}

// Fail records key as invalid.
func (r Reader) Fail(key string) {
	if r.Bad != nil {
		*r.Bad = append(*r.Bad, strings.TrimPrefix(r.Path+"/"+Escape(key), "/"))
	}
}

// Has reports whether the property is present and not null.
func (r Reader) Has(key string) bool { return r.Obj[key] != nil }

func (r Reader) String(key string) string {
	switch v := r.Obj[key].(type) {
	case nil:
	case string:
		return v
	default:
		r.Fail(key)
	}
	return ""
}

func (r Reader) Bool(key string) bool {
	switch v := r.Obj[key].(type) {
	case nil:
	case bool:
		return v
	default:
		r.Fail(key)
	}
	return false
}

// Int reads an integer, nil when absent.
func (r Reader) Int(key string) *int {
	switch v := r.Obj[key].(type) {
	case nil:
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err == nil {
			return &n
		}
		r.Fail(key)
	case float64:
		if n := int(v); float64(n) == v {
			return &n
		}
		r.Fail(key)
	case int:
		return &v
	default:
		r.Fail(key)
	}
	return nil
}

// Object reads an object property; absent is (nil, false).
func (r Reader) Object(key string) (Object, bool) {
	switch v := r.Obj[key].(type) {
	case nil:
	case Object:
		return v, true
	default:
		r.Fail(key)
	}
	return nil, false
}

// Strings reads an array of strings.
func (r Reader) Strings(key string) []string {
	switch v := r.Obj[key].(type) {
	case nil:
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			s, ok := x.(string)
			if !ok {
				r.Fail(key)
				return nil
			}
			out = append(out, s)
		}
		return out
	default:
		r.Fail(key)
	}
	return nil
}

// Set reads a String[Boolean] set: its keys whose value is true, in id
// order.
func (r Reader) Set(key string) []string {
	o, ok := r.Object(key)
	if !ok {
		return nil
	}
	var out []string
	for _, k := range SortedIDs(o) {
		switch o[k] {
		case true:
			out = append(out, k)
		case false, nil:
		default:
			r.Fail(key)
		}
	}
	return out
}

// Decode unmarshals a vendor property into v; absent leaves v alone.
func (r Reader) Decode(key string, v any) {
	raw, ok := r.Obj[key]
	if !ok || raw == nil {
		return
	}
	b, err := json.Marshal(raw)
	if err == nil {
		err = json.Unmarshal(b, v)
	}
	if err != nil {
		r.Fail(key)
	}
}

// Items returns the ids of a map of objects in order, with readers of
// each.
func (r Reader) Items(key string) ([]string, []Reader) {
	o, ok := r.Object(key)
	if !ok {
		return nil, nil
	}
	ids := SortedIDs(o)
	out := make([]Reader, 0, len(ids))
	keep := ids[:0]
	base := r.Path + "/" + Escape(key)
	for _, id := range ids {
		item, ok := o[id].(Object)
		if !ok {
			if o[id] != nil {
				r.Fail(key + "/" + id)
			}
			continue
		}
		keep = append(keep, id)
		out = append(out, Reader{Obj: item, Path: base + "/" + Escape(id), Bad: r.Bad})
	}
	return keep, out
}

// Invalid reports properties a codec could not read, by JSON pointer
// without the leading slash, as JMAP's invalidProperties lists them.
type Invalid struct{ Properties []string }

func (e *Invalid) Error() string { return "invalid properties: " + strings.Join(e.Properties, ", ") }

// --- Ids ---

// SortedIDs returns the keys of a map in id order: numbers by value, before
// other ids, which sort as strings. Codecs number the ids they make, so
// this is the order the items were in.
func SortedIDs(o Object) []string {
	keys := make([]string, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, CompareIDs)
	return keys
}

// CompareIDs orders ids as SortedIDs does.
func CompareIDs(a, b string) int {
	na, ea := strconv.Atoi(a)
	nb, eb := strconv.Atoi(b)
	switch {
	case ea == nil && eb == nil && na != nb:
		return na - nb
	case ea == nil && eb != nil:
		return -1
	case ea != nil && eb == nil:
		return 1
	}
	return strings.Compare(a, b)
}

// IDs names the items of a list: an item's own id (from its JSID parameter)
// when it has one no other item took, its position from 1 otherwise.
func IDs(own []string) []string {
	used := map[string]bool{}
	for _, id := range own {
		if id != "" {
			used[id] = true
		}
	}
	out := make([]string, len(own))
	seen := map[string]bool{}
	for i, id := range own {
		if id != "" && !seen[id] {
			out[i] = id
			seen[id] = true
			continue
		}
		n := i + 1
		for used[strconv.Itoa(n)] || seen[strconv.Itoa(n)] {
			n++
		}
		out[i] = strconv.Itoa(n)
		seen[out[i]] = true
	}
	return out
}

// OwnID returns the id to keep for the item at position i (from 0) of a
// decoded map: "" when IDs would give it that id anyway.
func OwnID(id string, i int) string {
	if id == strconv.Itoa(i+1) {
		return ""
	}
	return id
}

// JSID is the parameter that keeps a JSON item's id on the property it
// became.
const JSID = "JSID"

// TakeJSID removes a JSID parameter and returns its value.
func TakeJSID(params []contentline.Param) (string, []contentline.Param) {
	for i, p := range params {
		if strings.EqualFold(p.Name, JSID) && len(p.Values) == 1 {
			return p.Values[0], slices.Delete(slices.Clone(params), i, i+1)
		}
	}
	return "", params
}

// WithJSID adds a JSID parameter when id is not empty.
func WithJSID(params []contentline.Param, id string) []contentline.Param {
	if id == "" {
		return params
	}
	return append(slices.Clone(params), contentline.Param{Name: JSID, Values: []string{id}})
}

// --- JSPROP ---

// JSProp names the property that keeps a JSON value Cog does not model.
const JSProp = "JSPROP"

// JSPtr is JSPROP's parameter: where the value goes.
const JSPtr = "JSPTR"

// Escape escapes a JSON pointer reference token (RFC 6901).
func Escape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

func unescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// Sink keeps a value at a pointer as a JSPROP property.
func Sink(ptr string, v any) (contentline.Property, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return contentline.Property{}, err
	}
	return contentline.Property{Name: JSProp, Params: []contentline.Param{{Name: JSPtr, Values: []string{ptr}}}, Value: codec.EscapeText(string(b))}, nil
}

// Lift puts the values of JSPROP properties back at their pointers in o and
// returns the properties it did not: one that is not exactly what Sink
// writes, whose value is not JSON, or whose place is taken or has no
// parent. Those stay leftovers.
func Lift(props []contentline.Property, o Object) []contentline.Property {
	// A value may go inside another one lifted later (a property of an
	// object kept whole), so lift until nothing more can be.
	rest := props
	for {
		var next []contentline.Property
		for _, p := range rest {
			if !lift(p, o) {
				next = append(next, p)
			}
		}
		if len(next) == len(rest) {
			return next
		}
		rest = next
	}
}

func lift(p contentline.Property, o Object) bool {
	if !p.Is(JSProp) || p.Group != "" || len(p.Params) != 1 || !strings.EqualFold(p.Params[0].Name, JSPtr) || len(p.Params[0].Values) != 1 {
		return false
	}
	ptr := p.Params[0].Values[0]
	v, err := Parse([]byte(codec.UnescapeText(p.Value)))
	if err != nil {
		return false
	}
	if again, err := Sink(ptr, v); err != nil || again.Value != p.Value {
		return false
	}
	if !strings.HasPrefix(ptr, "/") {
		return false
	}
	parts := strings.Split(ptr[1:], "/")
	var cur any = o
	for _, part := range parts[:len(parts)-1] {
		switch c := cur.(type) {
		case Object:
			cur = c[unescape(part)]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(c) {
				return false
			}
			cur = c[i]
		default:
			return false
		}
	}
	parent, ok := cur.(Object)
	last := unescape(parts[len(parts)-1])
	if !ok || last == "" {
		return false
	}
	if _, taken := parent[last]; taken {
		return false
	}
	parent[last] = v
	return true
}

// Strip removes from o, recursively, the properties a schema does not
// know, and returns them as JSPROP properties. o is changed in place; pass
// a clone.
func Strip(o Object, s *Schema) ([]contentline.Property, error) {
	var out []contentline.Property
	err := strip(o, s, "", &out)
	return out, err
}

// Schema lists the properties a codec reads in one kind of object, and the
// schemas of the objects inside maps of them.
type Schema struct {
	Known map[string]bool
	// Maps holds, by property, the schema of the objects of a map of
	// id to object.
	Maps map[string]*Schema
	// Objects holds, by property, the schema of an object property.
	Objects map[string]*Schema
	// Arrays holds, by property, the schema of the objects of an array.
	Arrays map[string]*Schema
}

func strip(o Object, s *Schema, path string, out *[]contentline.Property) error {
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range o {
			if !yield(k) {
				return
			}
		}
	}) {
		v := o[k]
		ptr := path + "/" + Escape(k)
		if strings.HasPrefix(k, Vendor) {
			continue
		}
		if !s.Known[k] {
			p, err := Sink(ptr, v)
			if err != nil {
				return err
			}
			*out = append(*out, p)
			delete(o, k)
			continue
		}
		if sub := s.Maps[k]; sub != nil {
			if m, ok := v.(Object); ok {
				for _, id := range SortedIDs(m) {
					if item, ok := m[id].(Object); ok {
						if err := strip(item, sub, ptr+"/"+Escape(id), out); err != nil {
							return err
						}
					}
				}
			}
		}
		if sub := s.Objects[k]; sub != nil {
			if item, ok := v.(Object); ok {
				if err := strip(item, sub, ptr, out); err != nil {
					return err
				}
			}
		}
		if sub := s.Arrays[k]; sub != nil {
			if list, ok := v.([]any); ok {
				for i, x := range list {
					if item, ok := x.(Object); ok {
						if err := strip(item, sub, ptr+"/"+strconv.Itoa(i), out); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// Known returns a set of property names.
func Known(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// --- Exact forms ---

// Group is part of a model that maps to some JSON properties as a unit.
// Encoders write it in its standard form, and also its exact form (a
// snapshot of the model's fields) when reading the standard form back would
// not give the same fields. Decoders use the exact form only while the
// group's properties are still what it encodes to, so a client that
// changed them is read for what it sent.
type Group[M any] struct {
	Name  string
	Props []string
	Enc   func(m *M, out Object)
	Dec   func(in Reader, m *M)
	// Snap returns the group's fields; Load sets them from Snap's JSON.
	Snap func(m *M) any
	Load func(m *M, raw []byte) error
}

// Fields returns Snap and Load for a group of fields copied as one value.
func Fields[M, T any](get func(*M) T, put func(*M, T)) (func(*M) any, func(*M, []byte) error) {
	snap := func(m *M) any { return get(m) }
	load := func(m *M, raw []byte) error {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		put(m, v)
		return nil
	}
	return snap, load
}

// EncodeGroups writes m's groups to out, and the exact forms that need
// keeping to out[ExactKey]. clone copies a model to read a group back into:
// a group may depend on fields of the groups before it, and Dec sets every
// field of its own.
func EncodeGroups[M any](groups []Group[M], m *M, out Object, clone func(*M) *M) error {
	exact := Object{}
	for _, g := range groups {
		part := Object{}
		g.Enc(m, part)
		maps.Copy(out, part)
		tree, err := Tree(part)
		if err != nil {
			return fmt.Errorf("%s: %w", g.Name, err)
		}
		var bad []string
		probe := clone(m)
		g.Dec(Reader{Obj: tree.(Object), Bad: &bad}, probe)
		if len(bad) > 0 || !Equal(g.Snap(probe), g.Snap(m)) {
			exact[g.Name] = g.Snap(m)
		}
	}
	if len(exact) > 0 {
		out[ExactKey] = exact
	}
	return nil
}

// DecodeGroups reads m's groups from in, in order, using an exact form while
// the group's properties are what it encodes to.
func DecodeGroups[M any](groups []Group[M], in Reader, m *M, clone func(*M) *M) {
	exact, _ := in.Obj[ExactKey].(Object)
	for _, g := range groups {
		if raw, ok := exact[g.Name]; ok && sameAs(g, raw, in.Obj, m, clone) {
			b, _ := json.Marshal(raw)
			if err := g.Load(m, b); err == nil {
				continue
			}
		}
		g.Dec(in, m)
	}
}

func sameAs[M any](g Group[M], raw any, in Object, m *M, clone func(*M) *M) bool {
	b, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	probe := clone(m)
	if err := g.Load(probe, b); err != nil {
		return false
	}
	out := Object{}
	g.Enc(probe, out)
	return Equal(Pick(out, g.Props...), Pick(in, g.Props...))
}
