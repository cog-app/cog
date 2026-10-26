package dav

import (
	"errors"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// CardFilter is an addressbook-query filter (RFC 6352 §10.5). It is
// evaluated against each card's properties, so it reaches everything a card
// holds, leftovers included. SQL cannot compare text the way the default
// collation, i;unicode-casemap, does, so backends load cards and call Match
// rather than translating the filter.
type CardFilter struct {
	AllOf bool // test="allof"; the default is anyof
	Props []PropFilter
	// Limit is CARDDAV:limit/nresults, 0 for none.
	Limit int
}

// PropFilter matches cards by one property, by name.
type PropFilter struct {
	Name         string
	AllOf        bool
	IsNotDefined bool
	TextMatches  []TextMatch
	Params       []ParamFilter
}

// ParamFilter matches a property by one of its parameters.
type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

// TextMatch compares text with a collation (RFC 4790) and a match type.
type TextMatch struct {
	Text      string
	Collation string // i;unicode-casemap, i;ascii-casemap or i;octet
	MatchType string // equals, contains, starts-with or ends-with
	Negate    bool

	folded string // Text folded for the collation
}

// errCollation reports a collation Cog does not support; the request fails
// the CARDDAV:supported-collation precondition.
var errCollation = errors.New("dav: unsupported collation")

// errFilter reports a filter Cog cannot read; it fails
// CARDDAV:supported-filter.
var errFilter = errors.New("dav: unsupported filter")

func parseCardFilter(q *node) (*CardFilter, error) {
	f := &CardFilter{}
	if l := q.child(carddav("limit")).child(carddav("nresults")); l != nil {
		n, err := strconv.Atoi(strings.TrimSpace(l.text()))
		if err != nil || n < 0 {
			return nil, errFilter
		}
		f.Limit = n
	}
	fn := q.child(carddav("filter"))
	if fn == nil {
		return f, nil
	}
	f.AllOf = strings.EqualFold(fn.attr("test"), "allof")
	for _, pn := range fn.children() {
		if !pn.is(carddav("prop-filter")) {
			continue
		}
		pf := PropFilter{Name: pn.attr("name"), AllOf: strings.EqualFold(pn.attr("test"), "allof")}
		if pf.Name == "" {
			return nil, errFilter
		}
		for _, c := range pn.children() {
			switch {
			case c.is(carddav("is-not-defined")):
				pf.IsNotDefined = true
			case c.is(carddav("text-match")):
				tm, err := parseTextMatch(c)
				if err != nil {
					return nil, err
				}
				pf.TextMatches = append(pf.TextMatches, *tm)
			case c.is(carddav("param-filter")):
				pa := ParamFilter{Name: c.attr("name")}
				if pa.Name == "" {
					return nil, errFilter
				}
				if c.child(carddav("is-not-defined")) != nil {
					pa.IsNotDefined = true
				} else if t := c.child(carddav("text-match")); t != nil {
					tm, err := parseTextMatch(t)
					if err != nil {
						return nil, err
					}
					pa.TextMatch = tm
				}
				pf.Params = append(pf.Params, pa)
			}
		}
		f.Props = append(f.Props, pf)
	}
	return f, nil
}

func parseTextMatch(n *node) (*TextMatch, error) {
	tm := &TextMatch{
		Text:      n.text(),
		Collation: n.attr("collation"),
		MatchType: n.attr("match-type"),
		Negate:    strings.EqualFold(n.attr("negate-condition"), "yes"),
	}
	if tm.Collation == "" {
		tm.Collation = "i;unicode-casemap"
	}
	if tm.MatchType == "" {
		tm.MatchType = "contains"
	}
	switch tm.MatchType {
	case "equals", "contains", "starts-with", "ends-with":
	default:
		return nil, errFilter
	}
	switch tm.Collation {
	case "i;unicode-casemap", "i;ascii-casemap", "i;octet":
	default:
		return nil, errCollation
	}
	tm.folded = fold(tm.Collation, tm.Text)
	return tm, nil
}

// fold maps s so that comparing the results is comparing s in the collation:
// RFC 5051's i;unicode-casemap (case folding and compatibility
// decomposition), ASCII case folding, or the octets themselves.
func fold(collation, s string) string {
	switch collation {
	case "i;unicode-casemap":
		// A Caser keeps state, so each call gets its own.
		return norm.NFKD.String(cases.Fold().String(s))
	case "i;ascii-casemap":
		b := []byte(s)
		for i, c := range b {
			if c >= 'A' && c <= 'Z' {
				b[i] = c + 'a' - 'A'
			}
		}
		return string(b)
	}
	return s
}

func (tm *TextMatch) match(value string) bool {
	v := fold(tm.Collation, value)
	var ok bool
	switch tm.MatchType {
	case "equals":
		ok = v == tm.folded
	case "starts-with":
		ok = strings.HasPrefix(v, tm.folded)
	case "ends-with":
		ok = strings.HasSuffix(v, tm.folded)
	default:
		ok = strings.Contains(v, tm.folded)
	}
	return ok != tm.Negate
}

// Match reports whether a VCARD passes the filter. A filter without
// prop-filters matches every card.
func (f *CardFilter) Match(card *contentline.Component) bool {
	if len(f.Props) == 0 {
		return true
	}
	for i := range f.Props {
		ok := f.Props[i].match(card)
		if ok && !f.AllOf {
			return true
		}
		if !ok && f.AllOf {
			return false
		}
	}
	return f.AllOf
}

func (pf *PropFilter) match(card *contentline.Component) bool {
	found := false
	for i := range card.Props {
		p := &card.Props[i]
		if !p.Is(pf.Name) {
			continue
		}
		found = true
		if !pf.IsNotDefined && pf.matchProp(p) {
			return true
		}
	}
	if pf.IsNotDefined {
		return !found
	}
	return false
}

// matchProp combines the property's tests with the prop-filter's test; with
// none, the property's presence is enough.
func (pf *PropFilter) matchProp(p *contentline.Property) bool {
	if len(pf.TextMatches) == 0 && len(pf.Params) == 0 {
		return true
	}
	results := make([]bool, 0, len(pf.TextMatches)+len(pf.Params))
	if len(pf.TextMatches) > 0 {
		v := propertyText(p)
		for i := range pf.TextMatches {
			results = append(results, pf.TextMatches[i].match(v))
		}
	}
	for i := range pf.Params {
		results = append(results, pf.Params[i].match(p))
	}
	for _, ok := range results {
		if ok && !pf.AllOf {
			return true
		}
		if !ok && pf.AllOf {
			return false
		}
	}
	return pf.AllOf
}

func (pa *ParamFilter) match(p *contentline.Property) bool {
	var vals []string
	found := false
	for _, x := range p.Params {
		if strings.EqualFold(x.Name, pa.Name) {
			found = true
			vals = append(vals, x.Values...)
		}
	}
	if pa.IsNotDefined {
		return !found
	}
	if !found {
		return false
	}
	if pa.TextMatch == nil {
		return true
	}
	for _, v := range vals {
		if pa.TextMatch.match(v) {
			return true
		}
	}
	return false
}

// propertyText is the text a text-match compares with: the value with its
// TEXT escapes removed, so "Smith\, Jr" matches "Smith, Jr".
func propertyText(p *contentline.Property) string {
	return codec.UnescapeText(p.Value)
}
