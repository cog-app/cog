// Package codec holds what the iCalendar (internal/dav/ical) and vCard
// (internal/dav/vcard) codecs share: TEXT value escaping, and the leftovers
// each modelled component keeps of what Cog does not model, so it can be
// written back (DESIGN/dav-storage.md section 3.2).
package codec

import (
	"maps"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// UnescapeText decodes a TEXT value (RFC 5545 §3.3.11, RFC 6350 §3.4). A
// backslash before anything else is kept, as lenient readers do.
func UnescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ',', ';':
			b.WriteByte(s[i+1])
		default:
			b.WriteByte(c)
			continue
		}
		i++
	}
	return b.String()
}

// EscapeText encodes a TEXT value.
func EscapeText(s string) string {
	if !strings.ContainsAny(s, "\\;,\n\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', ';', ',':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Split splits s on every sep that no backslash escapes, leaving the parts
// escaped: a structured value's components (sep ';') or a list's values
// (sep ',').
func Split(s string, sep byte) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case sep:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// SplitText splits a multi-valued TEXT property (CATEGORIES, NICKNAME) on
// unescaped commas and unescapes each value.
func SplitText(s string) []string {
	parts := Split(s, ',')
	for i, p := range parts {
		parts[i] = UnescapeText(p)
	}
	return parts
}

// TakeParams splits params into the first single-valued occurrence of each
// modelled name (upper case), and the rest in order. A modelled parameter
// given twice, or with several values, keeps its other copies among the
// rest.
func TakeParams(params []contentline.Param, modelled ...string) (map[string]string, []contentline.Param) {
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

// ParamEqual reports whether two parameters are the same.
func ParamEqual(a, b contentline.Param) bool {
	return a.Name == b.Name && slices.Equal(a.Values, b.Values)
}

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

// KeepParams keeps the parameters of a modelled property other than the
// modelled ones, by the property's upper-case name.
func (l *Leftovers) KeepParams(name string, params []contentline.Param, modelled ...string) {
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

// DropParams removes kept parameters of a modelled property by name.
func (l *Leftovers) DropParams(prop string, names ...string) {
	ps, ok := l.Params[prop]
	if !ok {
		return
	}
	ps = slices.DeleteFunc(slices.Clone(ps), func(p contentline.Param) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, p.Name) })
	})
	if len(ps) == 0 {
		delete(l.Params, prop)
		return
	}
	l.Params[prop] = ps
}

// Has reports whether a leftover property has the name.
func (l *Leftovers) Has(name string) bool {
	return slices.ContainsFunc(l.Props, func(p contentline.Property) bool { return p.Is(name) })
}

// Clone returns a copy whose lists and map can be changed without changing
// l.
func (l Leftovers) Clone() Leftovers {
	return Leftovers{Props: slices.Clone(l.Props), Comps: slices.Clone(l.Comps), Params: maps.Clone(l.Params)}
}
