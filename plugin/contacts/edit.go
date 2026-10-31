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
	// The fields below are applied only when the form has them (a form
	// without them leaves the card's as they are).
	birthday     *string
	urls         []formValue
	hasURLs      bool
	addresses    []formAddress
	hasAddresses bool
}

// formAddress is one address row as submitted.
type formAddress struct {
	index                                   int // into the card's list, or -1 for a new row
	typ                                     string
	street, locality, region, code, country string
}

func (a formAddress) empty() bool {
	return a.street == "" && a.locality == "" && a.region == "" && a.code == "" && a.country == ""
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
	if f.phones, err = formValues(r, "phone"); err != nil {
		return nil, err
	}
	if _, ok := r.Form["birthday"]; ok {
		b := strings.TrimSpace(r.FormValue("birthday"))
		f.birthday = &b
	}
	if _, f.hasURLs = r.Form["urls_shown"]; f.hasURLs {
		if f.urls, err = formValues(r, "url"); err != nil {
			return nil, err
		}
	}
	if _, f.hasAddresses = r.Form["addresses_shown"]; f.hasAddresses {
		if f.addresses, err = formAddresses(r); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// formAddresses reads the address rows: adr_index, adr_type and one of each
// address field per row.
func formAddresses(r *http.Request) ([]formAddress, error) {
	idx, types := r.Form["adr_index"], r.Form["adr_type"]
	fields := [][]string{r.Form["adr_street"], r.Form["adr_locality"], r.Form["adr_region"], r.Form["adr_code"], r.Form["adr_country"]}
	if len(idx) != len(types) {
		return nil, errors.New("malformed address rows")
	}
	for _, f := range fields {
		if len(f) != len(idx) {
			return nil, errors.New("malformed address rows")
		}
	}
	var out []formAddress
	for i := range idx {
		a := formAddress{index: -1, typ: types[i],
			street:   strings.TrimSpace(strings.ReplaceAll(fields[0][i], "\r\n", "\n")),
			locality: oneLine(fields[1][i]), region: oneLine(fields[2][i]), code: oneLine(fields[3][i]), country: oneLine(fields[4][i]),
		}
		if idx[i] != "" && idx[i] != "new" {
			n, err := strconv.Atoi(idx[i])
			if err != nil || n < 0 {
				return nil, errors.New("malformed address row")
			}
			a.index = n
		}
		if !a.empty() {
			out = append(out, a)
		}
	}
	return out, nil
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
	if f.birthday != nil && *f.birthday != shownBirthday(c.Birthday) {
		c.Birthday = *f.birthday
	}
	if f.hasURLs {
		c.URLs = applyValues(c.URLs, f.urls, nil)
	}
	if f.hasAddresses {
		c.Addresses = applyAddresses(c.Addresses, f.addresses)
	}
	c.Stamp(now)
}

// addressTypes are the address form's type menu.
var addressTypes = []typeOption{{"HOME", "Home"}, {"WORK", "Work"}}

// Street is how the form shows an address's street lines: one per line.
func streetOf(a *vcard.Address) string { return strings.Join(a.Street, "\n") }

// applyAddresses is applyValues for addresses: a row the form shows
// unchanged keeps everything it had, a changed field replaces only itself,
// and fields the form does not show (PO box, extended address) stay.
func applyAddresses(cur []vcard.Address, rows []formAddress) []vcard.Address {
	out := make([]vcard.Address, 0, len(rows))
	used := map[int]bool{}
	set := func(field *[]string, shown, v string) {
		if shown == v {
			return
		}
		*field = nil
		if v != "" {
			*field = []string{v}
		}
	}
	for _, r := range rows {
		a := vcard.Address{}
		if r.index >= 0 && r.index < len(cur) && !used[r.index] {
			used[r.index] = true
			a = cur[r.index]
			if shown := shownType(&a.Meta, addressTypes); shown != r.typ {
				a.Types = slices.DeleteFunc(slices.Clone(a.Types), func(t string) bool { return strings.EqualFold(t, shown) })
				if r.typ != "" {
					a.Types = append([]string{r.typ}, a.Types...)
				}
				if len(a.Types) == 0 {
					a.Types = nil
				}
				a.Label = ""
			}
		} else if r.typ != "" {
			a.Types = []string{r.typ}
		}
		if streetOf(&a) != r.street {
			a.Street = nil
			if r.street != "" {
				a.Street = strings.Split(r.street, "\n")
			}
		}
		set(&a.Locality, joined(a.Locality), r.locality)
		set(&a.Region, joined(a.Region), r.region)
		set(&a.Code, joined(a.Code), r.code)
		set(&a.Country, joined(a.Country), r.country)
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// shownBirthday is how the form shows a BDAY: as a date input's value when
// it is a full date, otherwise as written.
func shownBirthday(v string) string {
	if t, ok := birthdayDate(v); ok {
		return t.Format("2006-01-02")
	}
	return v
}

// birthdayDate parses a BDAY that is a full date (2006-01-02 or 20060102).
func birthdayDate(v string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// formatBirthday is how a BDAY reads: "29 Feb 1984 · 42 years" for a full
// date (the age as of now), "29 Feb" for one without a year, otherwise as
// written.
func formatBirthday(v string, now time.Time) string {
	if t, ok := birthdayDate(v); ok {
		age := now.Year() - t.Year()
		if now.Month() < t.Month() || (now.Month() == t.Month() && now.Day() < t.Day()) {
			age--
		}
		if age < 0 {
			return t.Format("2 Jan 2006")
		}
		return fmt.Sprintf("%s · %d years", t.Format("2 Jan 2006"), age)
	}
	for _, layout := range []string{"--0102", "--01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format("2 Jan")
		}
	}
	return v
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
