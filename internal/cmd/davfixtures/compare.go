package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// --- compare ---

// runCompare checks that what a server holds after an import is what was
// exported: the same resources (by UID), each with the same components (by
// RECURRENCE-ID) and the same properties. Values are compared as written, so
// a server that rewrites one shows up; the properties every write may change
// (DTSTAMP, PRODID, REV, ...) are left out.
func runCompare(args []string) error {
	fs := flag.NewFlagSet("compare", flag.ExitOnError)
	got := fs.String("got", "", "file or directory read back from the new server")
	verbose := fs.Bool("v", false, "list every differing property")
	fs.BoolVar(&exact, "exact", false, "compare properties as written, without making spellings that mean the same one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *got == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: davfixtures compare -got path want-path...")
	}
	want, err := readResources(fs.Args()...)
	if err != nil {
		return err
	}
	have, err := readResources(*got)
	if err != nil {
		return err
	}

	// A UID may be in more than one collection; each copy has to be
	// matched by one read back. Copies that match exactly go first, so the
	// rest are paired for their differences. One collection holds a UID
	// once, so copies of a UID in one collection (which servers before the
	// DAV storage rewrite allowed) become one: the import replaces each with
	// the next.
	var missing, extra, differ, collapsed []string
	nWant, nHave := 0, 0
	for uid, ws := range want {
		nWant += len(ws)
		hs := slices.Clone(have[uid])
		take := func(w resource) bool {
			i := slices.IndexFunc(hs, func(h placed) bool { return len(diffResource(w, h.r)) == 0 })
			if i >= 0 {
				hs = slices.Delete(hs, i, i+1)
			}
			return i >= 0
		}
		byColl := map[string][]placed{}
		var colls []string
		for _, w := range ws {
			if byColl[w.coll] == nil {
				colls = append(colls, w.coll)
			}
			byColl[w.coll] = append(byColl[w.coll], w)
		}
		var rest []placed
		for _, coll := range colls {
			copies := byColl[coll]
			for range copies[1:] {
				collapsed = append(collapsed, uid)
			}
			if !slices.ContainsFunc(copies, func(w placed) bool { return take(w.r) }) {
				rest = append(rest, copies[len(copies)-1])
			}
		}
		for _, w := range rest {
			if len(hs) == 0 {
				missing = append(missing, fmt.Sprintf("%q (%s)", uid, w.file))
				continue
			}
			differ = append(differ, uid)
			if *verbose {
				for _, line := range diffResource(w.r, hs[0].r) {
					fmt.Printf("  %s: %s\n", uid, line)
				}
			}
			hs = hs[1:]
		}
		for range hs {
			extra = append(extra, uid)
		}
	}
	for uid, hs := range have {
		nHave += len(hs)
		if _, ok := want[uid]; !ok {
			for range hs {
				extra = append(extra, uid)
			}
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	slices.Sort(differ)
	slices.Sort(collapsed)
	fmt.Printf("want %d resources, got %d: %d missing, %d extra, %d differ, %d duplicates in one collection\n", nWant, nHave, len(missing), len(extra), len(differ), len(collapsed))
	for _, uid := range missing {
		fmt.Println("missing:", uid)
	}
	for _, uid := range extra {
		fmt.Println("extra:", uid)
	}
	for _, uid := range differ {
		fmt.Println("differ:", uid)
	}
	for _, uid := range collapsed {
		fmt.Println("duplicate:", uid)
	}
	if len(missing) > 0 {
		return fmt.Errorf("%d resources missing", len(missing))
	}
	return nil
}

// resource is one calendar object or card: its components keyed by kind and
// RECURRENCE-ID, each a sorted list of property lines.
type resource map[string][]string

// placed is a resource and the collection it was read from: the directory
// of a file with one VCALENDAR or VCARD, as an export has them, or the file
// itself when it holds several, as an import file does.
type placed struct {
	coll, file string
	r          resource
}

// volatile properties change on every write or name the server.
var volatile = map[string]bool{"DTSTAMP": true, "PRODID": true, "REV": true, "LAST-MODIFIED": true}

// readResources reads every .ics and .vcf file under paths. Each VCARD, and
// each UID of each VCALENDAR, is one resource.
func readResources(paths ...string) (map[string][]placed, error) {
	out := map[string][]placed{}
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if ext := filepath.Ext(p); ext != ".ics" && ext != ".vcf" {
				return nil
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			comps, err := contentline.Parse(f)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			var found []placed
			uids := []string{}
			for _, top := range comps {
				if top.Is("VCARD") {
					uids = append(uids, uidOf(top))
					found = append(found, placed{r: resource{"VCARD ": propLines(top)}})
					continue
				}
				if top.Is("VEVENT") || top.Is("VTODO") || top.Is("VJOURNAL") {
					// Stored without its VCALENDAR; the import wraps it.
					top = &contentline.Component{Name: "VCALENDAR", Components: []*contentline.Component{top}}
				}
				byUID := map[string]resource{}
				var order []string
				var loose []*contentline.Component
				for _, c := range top.Components {
					if c.Is("VTIMEZONE") {
						continue
					}
					uid := uidOf(c)
					if uid == "" && !c.Is("VEVENT") && !c.Is("VTODO") && !c.Is("VJOURNAL") {
						// The import keeps it with the first resource.
						loose = append(loose, c)
						continue
					}
					if byUID[uid] == nil {
						byUID[uid] = resource{}
						order = append(order, uid)
					}
					rid := ""
					if r := c.Prop("RECURRENCE-ID"); r != nil {
						rid = r.Value
					}
					byUID[uid][strings.ToUpper(c.Name)+" "+rid] = propLines(c)
				}
				for _, c := range loose {
					if len(order) == 0 {
						order = append(order, "")
						byUID[""] = resource{}
					}
					byUID[order[0]][strings.ToUpper(c.Name)+" "] = propLines(c)
				}
				for _, uid := range order {
					uids = append(uids, uid)
					found = append(found, placed{r: byUID[uid]})
				}
			}
			coll := filepath.Dir(p)
			if len(comps) > 1 {
				coll = p
			}
			for i, f := range found {
				f.coll, f.file = coll, p
				out[uids[i]] = append(out[uids[i]], f)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func uidOf(c *contentline.Component) string {
	if p := c.Prop("UID"); p != nil {
		return p.Value
	}
	return ""
}

// exact turns off the normalisation in propLines.
var exact bool

// defaultValue is the value type each property has without VALUE.
var defaultValue = map[string]string{
	"DTSTART": "DATE-TIME", "DTEND": "DATE-TIME", "DUE": "DATE-TIME", "RECURRENCE-ID": "DATE-TIME",
	"EXDATE": "DATE-TIME", "RDATE": "DATE-TIME", "TRIGGER": "DURATION",
}

// textTyped properties hold TEXT, whose escaping varies: "a,b" and "a\\,b" are the
// same text.
var textTyped = map[string]bool{
	"SUMMARY": true, "DESCRIPTION": true, "LOCATION": true, "COMMENT": true, "CONTACT": true,
	"FN": true, "NOTE": true, "TITLE": true, "ROLE": true,
}

// listProps may be written as one line with a list of values or as several
// lines; both mean the same.
var listProps = map[string]bool{"EXDATE": true, "RDATE": true, "CATEGORIES": true}

// propLines are a component's properties, sub-components included, one
// line each with parameters sorted, in sorted order. Unless exact is set,
// spellings that mean the same are made one, so only what a server changed
// is reported: repeated parameters (TYPE=A;TYPE=B and TYPE=A,B), a VALUE
// that is the default, a date written without VALUE=DATE or with a TZID it
// cannot have, lists written over several lines, TEXT escaped differently
// and a TZID Cog resolves to an IANA name (DESIGN/dav-storage.md 4.1).
func propLines(c *contentline.Component) []string {
	var lines []string
	for _, p := range c.Props {
		name := strings.ToUpper(p.Name)
		if volatile[name] {
			continue
		}
		vals := map[string][]string{}
		for _, pa := range p.Params {
			n := strings.ToUpper(pa.Name)
			vals[n] = append(vals[n], pa.Values...)
		}
		values := []string{p.Value}
		if !exact {
			if dv, ok := defaultValue[name]; ok {
				isDate := len(p.Value) == 8 || (strings.Contains(p.Value, ",") && len(strings.SplitN(p.Value, ",", 2)[0]) == 8)
				if isDate && len(vals["VALUE"]) == 0 {
					vals["VALUE"] = []string{"DATE"}
				}
				if slices.Equal(vals["VALUE"], []string{"DATE"}) {
					delete(vals, "TZID")
				}
				if slices.Equal(vals["VALUE"], []string{dv}) {
					delete(vals, "VALUE")
				}
			}
			if listProps[name] {
				values = splitList(p.Value)
			}
			if textTyped[name] {
				values = []string{codec.EscapeText(codec.UnescapeText(p.Value))}
			}
			for i, id := range vals["TZID"] {
				if loc := tz.Resolve(id, nil); loc != nil {
					vals["TZID"][i] = loc.String()
				}
			}
		}
		var params []string
		for n, v := range vals {
			params = append(params, n+"="+strings.Join(v, ","))
		}
		slices.Sort(params)
		line := name
		if p.Group != "" {
			line = strings.ToLower(p.Group) + "." + line
		}
		if len(params) > 0 {
			line += ";" + strings.Join(params, ";")
		}
		for _, v := range values {
			lines = append(lines, line+":"+v)
		}
	}
	for _, sub := range c.Components {
		for _, l := range propLines(sub) {
			lines = append(lines, sub.Name+"/"+l)
		}
	}
	slices.Sort(lines)
	return lines
}

// splitList splits a value at commas that are not escaped.
func splitList(v string) []string {
	var out []string
	start := 0
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case ',':
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

func diffResource(want, have resource) []string {
	var d []string
	for key, w := range want {
		h, ok := have[key]
		if !ok {
			d = append(d, "component missing: "+key)
			continue
		}
		for _, l := range w {
			if !slices.Contains(h, l) {
				d = append(d, key+" lost "+l)
			}
		}
		for _, l := range h {
			if !slices.Contains(w, l) {
				d = append(d, key+" gained "+l)
			}
		}
	}
	for key := range have {
		if _, ok := want[key]; !ok {
			d = append(d, "component extra: "+key)
		}
	}
	return d
}
