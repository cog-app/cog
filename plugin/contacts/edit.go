package contacts

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

// typeOption is a choice in the edit form's type menu, by the TYPE value it
// writes.
type typeOption struct{ Type, Label string }

var (
	emailTypes = []typeOption{{"INTERNET", "Personal"}, {"WORK", "Work"}, {"HOME", "Home"}}
	phoneTypes = []typeOption{{"CELL", "Cell"}, {"WORK", "Work"}, {"HOME", "Home"}, {"FAX", "Fax"}}
)

// shownType is the menu choice the form shows for a value: the first of its
// types that is an option, or "" (Other).
func shownType(m *vcard.Meta, options []typeOption) string {
	for _, t := range m.Types {
		for _, o := range options {
			if strings.EqualFold(t, o.Type) {
				return o.Type
			}
		}
	}
	return ""
}

// displayLabel is how a value's label or types read in the UI. Apple writes
// its built-in labels as "_$!<Mobile>!$_".
func displayLabel(m *vcard.Meta) string {
	if m.Label != "" {
		if s, ok := strings.CutPrefix(m.Label, "_$!<"); ok {
			if s, ok := strings.CutSuffix(s, ">!$_"); ok {
				return s
			}
		}
		return m.Label
	}
	var types []string
	for _, t := range m.Types {
		if !strings.EqualFold(t, "pref") && !strings.EqualFold(t, "INTERNET") && !strings.EqualFold(t, "VOICE") {
			types = append(types, strings.ToLower(t))
		}
	}
	return strings.Join(types, ", ")
}

// joined is how the form shows a list of values.
func joined(vals []string) string { return strings.Join(vals, " ") }

// formValue is one email or phone row as submitted.
type formValue struct {
	index int // into the card's list, or -1 for a new row
	typ   string
	value string
}

// contactForm is the contact edit form as submitted.
type contactForm struct {
	fn, given, family string
	org, title, note  string
	emails, phones    []formValue
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(s))
}

func parseContactForm(r *http.Request) (*contactForm, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	f := &contactForm{
		fn:     oneLine(r.FormValue("fn")),
		given:  oneLine(r.FormValue("given")),
		family: oneLine(r.FormValue("family")),
		org:    oneLine(r.FormValue("org")),
		title:  oneLine(r.FormValue("title")),
		note:   strings.ReplaceAll(r.FormValue("note"), "\r\n", "\n"),
	}
	if f.fn == "" && f.given == "" && f.family == "" && f.org == "" {
		return nil, errors.New("a contact needs a name")
	}
	var err error
	if f.emails, err = formValues(r, "email"); err != nil {
		return nil, err
	}
	f.phones, err = formValues(r, "phone")
	return f, err
}

// formValues reads the rows of one list: kind_index, kind_type and
// kind_value, one of each per row.
func formValues(r *http.Request, kind string) ([]formValue, error) {
	idx, types, vals := r.Form[kind+"_index"], r.Form[kind+"_type"], r.Form[kind+"_value"]
	if len(types) != len(vals) || (len(idx) != 0 && len(idx) != len(vals)) {
		return nil, fmt.Errorf("malformed %s rows", kind)
	}
	var out []formValue
	for i, v := range vals {
		fv := formValue{index: -1, typ: types[i], value: oneLine(v)}
		if len(idx) > 0 && idx[i] != "" && idx[i] != "new" {
			n, err := strconv.Atoi(idx[i])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("malformed %s row", kind)
			}
			fv.index = n
		}
		if fv.value != "" {
			out = append(out, fv)
		}
	}
	return out, nil
}

// applyForm writes a submitted form into a card. Only what the form shows
// changes, and only when it differs from what the form was shown with:
// addresses, photos, labels and parameters of values the form did not
// change, and everything Cog does not model stay. Changed fields drop their
// dependent leftovers, and REV moves.
func applyForm(c *vcard.Card, f *contactForm, now time.Time) {
	oldFormatted := ""
	if c.N != nil {
		oldFormatted = c.N.Formatted()
	}
	nChanged := false
	setName := func(field *[]string, v string) {
		if joined(*field) == v {
			return
		}
		nChanged = true
		*field = nil
		if v != "" {
			*field = []string{v}
		}
	}
	if c.N != nil || f.given != "" || f.family != "" {
		n := vcard.Name{}
		if c.N != nil {
			n = *c.N
		}
		setName(&n.Given, f.given)
		setName(&n.Family, f.family)
		if nChanged {
			c.N = &n
			c.Changed("N")
		}
	}

	fn := f.fn
	switch {
	case fn == "" && c.N != nil:
		fn = c.N.Formatted()
	case fn == c.FN && nChanged && c.FN == oldFormatted:
		// The name shown followed N; keep it following.
		fn = c.N.Formatted()
	}
	if fn == "" && len(c.Org) > 0 {
		fn = c.Org[0]
	}
	if fn == "" {
		fn = f.org
	}
	if fn != c.FN {
		c.FN = fn
		c.Changed("FN")
	}

	shownOrg := ""
	if len(c.Org) > 0 {
		shownOrg = c.Org[0]
	}
	if f.org != shownOrg {
		switch {
		case len(c.Org) > 1:
			c.Org = slices.Clone(c.Org)
			c.Org[0] = f.org
		case f.org == "":
			c.Org = nil
		default:
			c.Org = []string{f.org}
		}
		c.Changed("ORG")
	}
	if f.title != c.Title {
		c.Title = f.title
	}
	if f.note != c.Note {
		c.Note = f.note
	}
	c.Emails = applyValues(c.Emails, f.emails, emailTypes)
	c.Phones = applyValues(c.Phones, f.phones, phoneTypes)
	c.Stamp(now)
}

// applyValues returns a list as the form left it: rows in the order
// submitted, rows the form no longer has gone. A row whose menu choice
// changed swaps that type for the new one and loses its label, which iOS
// would show instead; its other types and parameters stay.
func applyValues(cur []vcard.Value, rows []formValue, options []typeOption) []vcard.Value {
	out := make([]vcard.Value, 0, len(rows))
	used := map[int]bool{}
	for _, r := range rows {
		if r.index >= 0 && r.index < len(cur) && !used[r.index] {
			used[r.index] = true
			v := cur[r.index]
			v.Value = r.value
			if shown := shownType(&v.Meta, options); shown != r.typ {
				v.Types = slices.DeleteFunc(slices.Clone(v.Types), func(t string) bool { return strings.EqualFold(t, shown) })
				if r.typ != "" {
					v.Types = append([]string{r.typ}, v.Types...)
				}
				if len(v.Types) == 0 {
					v.Types = nil
				}
				v.Label = ""
			}
			out = append(out, v)
			continue
		}
		v := vcard.Value{Value: r.value}
		if r.typ != "" {
			v.Types = []string{r.typ}
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// nameFromFN gives a new card that has only a display name the N vCard 3.0
// requires, the way clients split one: the last word is the family name.
func nameFromFN(c *vcard.Card) {
	if c.N != nil || c.FN == "" {
		return
	}
	words := strings.Fields(c.FN)
	n := &vcard.Name{Family: words[len(words)-1:]}
	if len(words) > 1 {
		n.Given = []string{strings.Join(words[:len(words)-1], " ")}
	}
	c.N = n
}

// setPhoto makes data (a JPEG) the card's photo, in the form its version
// writes.
func setPhoto(c *vcard.Card, data []byte, now time.Time) {
	ph := &vcard.Photo{Data: data, Type: "JPEG", Encoding: "b"}
	switch c.Version {
	case vcard.V40:
		ph.Type, ph.Encoding = "image/jpeg", ""
	case vcard.V21:
		ph.Encoding = "BASE64"
	}
	c.Photo = ph
	c.Stamp(now)
}
