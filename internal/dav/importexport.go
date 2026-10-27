package dav

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"github.com/google/uuid"
)

// backendsOf returns the enabled backends holding resources of c.
func (s *Server) backendsOf(c *Collection) []Backend {
	var out []Backend
	for _, b := range s.Backends() {
		if slices.ContainsFunc(b.Components(), c.Has) {
			out = append(out, b)
		}
	}
	return out
}

// ImportResult counts what an import did.
type ImportResult struct {
	Imported int      // new resources
	Replaced int      // resources whose UID was already in the collection
	Skipped  int      // components the collection does not hold
	Failed   int      // resources that could not be stored
	Reasons  []string // why, for the first few of them
}

// maxImportReasons bounds ImportResult.Reasons; Failed keeps counting.
const maxImportReasons = 5

func (r *ImportResult) fail(format string, args ...any) {
	r.Failed++
	if len(r.Reasons) < maxImportReasons {
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}
}

// ErrReadOnly is a write to a collection Cog fills itself.
var ErrReadOnly = errors.New("dav: the collection is read-only")

// Import splits an iCalendar or vCard file into resources the way a client
// would PUT them and stores each in c. A resource whose UID is already in
// the collection replaces it, so importing the same file twice is a no-op.
func (s *Server) Import(ownerUsername string, c *Collection, r io.Reader) (*ImportResult, error) {
	if c.ReadOnly() {
		return nil, ErrReadOnly
	}
	if c.Home == HomeAddressBooks {
		// The birthday calendar reads the imported cards in one pass.
		release := s.holdBirthdays(ownerUsername)
		defer func() {
			if err := release(); err != nil {
				s.Log.Error("dav: birthday calendar after import", "owner", ownerUsername, "err", err)
			}
		}()
	}
	comps, err := contentline.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("not a valid file: %w", err)
	}
	bes := s.backendsOf(c)
	stored := map[Component]bool{}
	for _, b := range bes {
		for _, comp := range b.Components() {
			stored[comp] = c.Has(comp)
		}
	}
	res := &ImportResult{}
	for _, top := range comps {
		if c, ok := ParseComponent(top.Name); ok && c != VCARD {
			// A component without its VCALENDAR, which servers before the
			// DAV storage rewrite stored as sent.
			top = &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{
				{Name: "VERSION", Value: "2.0"}, {Name: "PRODID", Value: "-//Cog//Import//EN"},
			}, Components: []*contentline.Component{top}}
		}
		switch {
		case top.Is("VCARD"):
			if !stored[VCARD] {
				res.Skipped++
				continue
			}
			s.importOne(ownerUsername, c, bes, top, VCARD, res)
		case top.Is("VCALENDAR"):
			for _, obj := range splitCalendar(top) {
				comp, _ := ParseComponent(obj.Components[len(obj.Components)-1].Name)
				if !stored[comp] {
					res.Skipped++
					continue
				}
				s.importOne(ownerUsername, c, bes, obj, comp, res)
			}
		default:
			res.Skipped++
		}
	}
	return res, nil
}

func (s *Server) importOne(owner string, c *Collection, bes []Backend, obj *contentline.Component, comp Component, res *ImportResult) {
	target := obj
	if comp != VCARD {
		target = obj.Components[len(obj.Components)-1]
	}
	uid := ""
	if p := target.Prop("UID"); p != nil {
		uid = p.Value
	}
	if uid == "" {
		// CalDAV and CardDAV need a UID; give the resource one rather than
		// drop it. Every component of a calendar object shares it.
		uid = uuid.NewString()
		for _, sub := range obj.Components {
			if !sub.Is("VTIMEZONE") {
				sub.Props = append(sub.Props, contentline.Property{Name: "UID", Value: uid})
			}
		}
		if comp == VCARD {
			obj.Props = append(obj.Props, contentline.Property{Name: "UID", Value: uid})
		}
	}
	if comp != VCARD {
		stripManaged(obj)
	}
	var buf bytes.Buffer
	if err := contentline.Write(&buf, []*contentline.Component{obj}); err != nil {
		res.fail("%s: %v", uid, err)
		return
	}
	uri := ""
	for _, b := range bes {
		found, err := b.FindUID(owner, c, uid)
		if err != nil {
			res.fail("%s: %v", uid, err)
			return
		}
		if found != "" {
			uri = found
			break
		}
	}
	if uri == "" {
		uri = uriFor(uid, comp)
	}
	put, err := s.put(bes, owner, c, &PutRequest{URI: uri, UID: uid, Component: comp, Data: buf.Bytes(), Object: obj})
	switch {
	case errors.Is(err, errResourceKind):
		res.fail("%s: a resource of another type has the same name", uid)
	case err != nil:
		res.fail("%s: %v", uid, err)
	case put.Created:
		res.Imported++
	default:
		res.Replaced++
	}
}

var safeURI = regexp.MustCompile(`^[A-Za-z0-9._@+=-]{1,200}$`)

// uriFor names a new resource after its UID when that is safe in a path.
func uriFor(uid string, comp Component) string {
	ext := ".ics"
	if comp == VCARD {
		ext = ".vcf"
	}
	if safeURI.MatchString(uid) {
		return uid + ext
	}
	return uuid.NewString() + ext
}

// splitCalendar splits a VCALENDAR into calendar object resources: the
// components sharing a UID and type (a master and its overrides), each with
// the calendar's properties, except METHOD, and the VTIMEZONEs they use. Each
// returned VCALENDAR ends with one of its own components, which names its
// type.
//
// A component of a type no backend stores (X-EVENT-SERIES, VFREEBUSY) is not
// dropped: it goes with the resource of its UID, or with the first resource
// when it has no UID, where the codec keeps it among the object's other
// components, as it would from a PUT of the whole file. One with a UID no
// resource has is returned on its own, ending with itself, so callers count
// it as skipped.
func splitCalendar(cal *contentline.Component) []*contentline.Component {
	var props []contentline.Property
	for _, p := range cal.Props {
		if !p.Is("METHOD") {
			props = append(props, p)
		}
	}
	zones := map[string]*contentline.Component{}
	type key struct{ name, uid string }
	groups := map[key][]*contentline.Component{}
	var order []key
	type other struct {
		uid string
		c   *contentline.Component
	}
	var others []other
	for _, sub := range cal.Components {
		if sub.Is("VTIMEZONE") {
			if id := sub.Prop("TZID"); id != nil {
				zones[id.Value] = sub
			}
			continue
		}
		uid := ""
		if p := sub.Prop("UID"); p != nil {
			uid = p.Value
		}
		if _, ok := ParseComponent(sub.Name); !ok {
			others = append(others, other{uid, sub})
			continue
		}
		k := key{strings.ToUpper(sub.Name), uid}
		if uid == "" {
			// Without a UID each component is its own resource.
			k.uid = fmt.Sprintf("\x00%d", len(order))
		}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], sub)
	}
	attached := map[key][]*contentline.Component{}
	for _, o := range others {
		i := 0
		if o.uid != "" {
			i = slices.IndexFunc(order, func(k key) bool { return k.uid == o.uid })
		}
		if i < 0 || len(order) == 0 {
			k := key{strings.ToUpper(o.c.Name), fmt.Sprintf("\x00%d", len(order))}
			order = append(order, k)
			groups[k] = []*contentline.Component{o.c}
			continue
		}
		attached[order[i]] = append(attached[order[i]], o.c)
	}
	var out []*contentline.Component
	for _, k := range order {
		obj := &contentline.Component{Name: cal.Name, Props: slices.Clone(props)}
		used := map[string]bool{}
		for _, sub := range groups[k] {
			collectTZIDs(sub, used)
		}
		for _, sub := range attached[k] {
			collectTZIDs(sub, used)
		}
		for _, id := range slices.Sorted(func(yield func(string) bool) {
			for id := range used {
				if !yield(id) {
					return
				}
			}
		}) {
			if z, ok := zones[id]; ok {
				obj.Components = append(obj.Components, z)
			}
		}
		obj.Components = append(obj.Components, attached[k]...)
		obj.Components = append(obj.Components, groups[k]...)
		out = append(out, obj)
	}
	return out
}

func collectTZIDs(c *contentline.Component, ids map[string]bool) {
	for i := range c.Props {
		if id := c.Props[i].Param("TZID"); id != "" {
			ids[id] = true
		}
	}
	for _, sub := range c.Components {
		collectTZIDs(sub, ids)
	}
}

// Export writes every resource of c as one file: a single VCALENDAR, with
// each VTIMEZONE once, or the vCards one after another. It reads the stored
// resources through the same path as GET, so export and DAV agree.
func (s *Server) Export(w io.Writer, ownerUsername string, c *Collection) error {
	return s.export(w, ownerUsername, c, s.backendsOf(c), exportOptions{})
}

// exportOptions change what an export shows, for feeds.
type exportOptions struct {
	// busyOnly shows only when the owner is busy, as a free/busy-only
	// share does: resources are redacted (BusyRedactor), and those of a
	// backend that cannot redact them are left out.
	busyOnly bool
	// attachment, when set, gives the URL a managed attachment of the
	// owner's is written with; otherwise it keeps the URL Cog serves it at.
	attachment func(id string) string
}

func (s *Server) export(w io.Writer, ownerUsername string, c *Collection, bes []Backend, opt exportOptions) error {
	var all []Resource
	for _, b := range bes {
		list, err := b.List(ownerUsername, c, true)
		if err != nil {
			return err
		}
		if opt.busyOnly {
			red, ok := b.(BusyRedactor)
			if !ok {
				continue
			}
			for i := range list {
				if err := red.Redact(c, &list[i]); err != nil {
					return err
				}
			}
		}
		all = append(all, list...)
	}
	slices.SortFunc(all, func(a, b Resource) int { return strings.Compare(a.URI, b.URI) })
	if c.Home == HomeAddressBooks {
		for _, r := range all {
			data := r.Data
			if _, err := w.Write(data); err != nil {
				return err
			}
			if !bytes.HasSuffix(data, []byte("\n")) {
				if _, err := io.WriteString(w, "\r\n"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	out := &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{
		{Name: "VERSION", Value: "2.0"},
		{Name: "PRODID", Value: "-//Cog//DAV//EN"},
		{Name: "X-WR-CALNAME", Value: escapeText(c.Title())},
	}}
	if c.Timezone != "" {
		out.Props = append(out.Props, contentline.Property{Name: "X-WR-TIMEZONE", Value: c.Timezone})
	}
	seenProp := map[string]bool{}
	seenZone := map[string]bool{}
	var objects []*contentline.Component
	for _, r := range all {
		comps, err := contentline.Parse(bytes.NewReader(r.Data))
		if err != nil || len(comps) != 1 {
			return fmt.Errorf("resource %s is not a calendar object: %v", r.URI, err)
		}
		for _, p := range comps[0].Props {
			if p.Is("VERSION") || p.Is("PRODID") || p.Is("METHOD") || p.Is("X-WR-CALNAME") || p.Is("X-WR-TIMEZONE") {
				continue
			}
			var line bytes.Buffer
			if err := contentline.Write(&line, []*contentline.Component{{Name: "X", Props: []contentline.Property{p}}}); err != nil {
				return err
			}
			if !seenProp[line.String()] {
				seenProp[line.String()] = true
				out.Props = append(out.Props, p)
			}
		}
		for _, sub := range comps[0].Components {
			if sub.Is("VTIMEZONE") {
				id := ""
				if p := sub.Prop("TZID"); p != nil {
					id = p.Value
				}
				if seenZone[id] {
					continue
				}
				seenZone[id] = true
				out.Components = append(out.Components, sub)
				continue
			}
			if opt.attachment != nil {
				relinkManaged(sub, s.AttachmentURL(ownerUsername, ""), opt.attachment)
			}
			stripManaged(sub)
			objects = append(objects, sub)
		}
	}
	out.Components = append(out.Components, objects...)
	return contentline.Write(w, []*contentline.Component{out})
}

// escapeText escapes an iCalendar TEXT value.
func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`).Replace(s)
}

// ComponentsIn returns the resource component types an iCalendar or vCard
// file holds, in a fixed order.
func ComponentsIn(r io.Reader) ([]Component, error) {
	comps, err := contentline.Parse(r)
	if err != nil {
		return nil, err
	}
	seen := map[Component]bool{}
	for _, top := range comps {
		if c, ok := ParseComponent(top.Name); ok {
			seen[c] = true // a VCARD, or a component without its VCALENDAR
			continue
		}
		for _, sub := range top.Components {
			if c, ok := ParseComponent(sub.Name); ok && c != VCARD {
				seen[c] = true
			}
		}
	}
	var out []Component
	for _, c := range []Component{VEVENT, VTODO, VJOURNAL, VCARD} {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// Stores reports whether an enabled backend stores comp.
func (s *Server) Stores(comp Component) bool {
	for _, b := range s.Backends() {
		if slices.Contains(b.Components(), comp) {
			return true
		}
	}
	return false
}
