package jmap

import (
	"slices"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/jscontact"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
)

// JMAP for Contacts (RFC 9610).

// defaultBook names the address book the core makes for every user.
const defaultBook = "contacts"

var addressBooks = &collType{
	name:       "AddressBook",
	capability: CapContacts,
	home:       dav.HomeAddressBooks,
	comps:      []dav.Component{dav.VCARD},
	colls:      func(a *account) []dav.Granted { return a.books },
	items:      contactCards,
	removeArg:  "onDestroyRemoveContents",
	toJSON: func(a *account, g *dav.Granted) jsobj.Object {
		r := g.Rights
		return jsobj.Object{
			"id":           collectionID(g.ID),
			"name":         g.Title(),
			"description":  nullable(g.Description),
			"sortOrder":    g.SortOrder,
			"isDefault":    a.own && g.Name == defaultBook,
			"isSubscribed": true,
			"shareWith":    nil,
			"myRights": jsobj.Object{
				"mayRead": r.Read, "mayWrite": r.Write, "mayShare": r.Owner, "mayDelete": r.Owner,
			},
		}
	},
	apply: applyBook,
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// applyBook sets an address book's properties a client may set. Being
// subscribed is not kept: every collection a user can see is listed.
func applyBook(obj jsobj.Object, c *dav.Collection) *setError {
	var bad []string
	for k, v := range obj {
		switch k {
		case "name":
			s, ok := v.(string)
			if !ok || s == "" {
				bad = append(bad, k)
				continue
			}
			c.DisplayName = s
		case "description":
			s, ok := v.(string)
			if !ok && v != nil {
				bad = append(bad, k)
				continue
			}
			c.Description = s
		case "sortOrder":
			n, ok := intOf(v)
			if !ok || n < 0 {
				bad = append(bad, k)
				continue
			}
			c.SortOrder = n
		case "isSubscribed":
			if _, ok := v.(bool); !ok {
				bad = append(bad, k)
			}
		case "id", "myRights", "isDefault", "shareWith":
			if v != nil {
				bad = append(bad, k)
			}
		default:
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return &setError{Type: "invalidProperties", Properties: bad}
	}
	return nil
}

func (c *call) addressBookGet(args jsobj.Object) (any, error) {
	return c.collectionsGet(addressBooks, args)
}

func (c *call) addressBookChanges(args jsobj.Object) (any, error) {
	return c.collectionsChanges(addressBooks, args)
}

func (c *call) addressBookSet(args jsobj.Object) (any, error) {
	return c.collectionsSet(addressBooks, args)
}

// --- ContactCard ---

var contactCards = &itemType{
	name:       "ContactCard",
	capability: CapContacts,
	comp:       dav.VCARD,
	ext:        ".vcf",
	collProp:   "addressBookIds",
	colls:      func(a *account) []dav.Granted { return a.books },
	codec:      cardCodec{},
}

func init() { addressBooks.items = contactCards }

type cardCodec struct{}

func (cardCodec) encode(m *dav.Model) (jsobj.Object, error) { return jscontact.Encode(m.Card) }

func (cardCodec) decode(obj jsobj.Object) (*dav.Model, error) {
	card, err := jscontact.Decode(obj)
	if err != nil {
		return nil, err
	}
	return &dav.Model{Card: card}, nil
}

func (cardCodec) uid(m *dav.Model) string           { return m.Card.UID }
func (cardCodec) setUID(m *dav.Model, uid string)   { m.Card.UID = uid }
func (cardCodec) created(m *dav.Model, t time.Time) { m.Card.Stamp(t) }

// edited records an edit as the web UI does: the vendor copies of a
// changed name or organisation go (section 3.2), and REV moves.
func (cardCodec) edited(old, cur *dav.Model, t time.Time) {
	o, n := old.Card, cur.Card
	if o.FN != n.FN {
		n.Changed("FN")
	}
	if !jsobj.Equal(o.N, n.N) {
		n.Changed("N")
	}
	if !slices.Equal(o.Org, n.Org) {
		n.Changed("ORG")
	}
	n.Stamp(t)
}

func (cardCodec) render(m *dav.Model) *contentline.Component {
	m.Card.AssignGroups()
	return m.Card.Encode()
}

func (c *call) contactGet(args jsobj.Object) (any, error) { return c.getItems(contactCards, args) }

func (c *call) contactChanges(args jsobj.Object) (any, error) {
	return c.itemChanges(contactCards, args)
}

func (c *call) contactSet(args jsobj.Object) (any, error) {
	return c.setItems(contactCards, args, setOptions{})
}

// --- ContactCard/query (RFC 9610 section 3.5) ---

type queryArgs struct {
	AccountID      string       `json:"accountId"`
	Filter         jsobj.Object `json:"filter"`
	Sort           []sortArg    `json:"sort"`
	Position       int          `json:"position"`
	Anchor         *string      `json:"anchor"`
	AnchorOffset   int          `json:"anchorOffset"`
	Limit          *int         `json:"limit"`
	CalculateTotal bool         `json:"calculateTotal"`
}

type sortArg struct {
	Property    string `json:"property"`
	IsAscending *bool  `json:"isAscending"`
	Collation   string `json:"collation"`
}

// fold compares text as i;unicode-casemap does (RFC 5051): case folded
// and compatibility decomposed.
type folder struct{}

func (folder) String(s string) string { return norm.NFKD.String(cases.Fold().String(s)) }

var fold folder

func contains(haystack, needle string) bool {
	return strings.Contains(fold.String(haystack), fold.String(needle))
}

func anyContains(vals []string, needle string) bool {
	return slices.ContainsFunc(vals, func(v string) bool { return contains(v, needle) })
}

// cardText returns the card's values a condition looks in.
func cardText(c *vcard.Card, what string) []string {
	var out []string
	values := func(list []vcard.Value) {
		for _, v := range list {
			out = append(out, v.Value, v.Label)
		}
	}
	n := c.N
	if n == nil {
		n = &vcard.Name{}
	}
	switch what {
	case "name":
		out = append(append(append(append(append([]string{c.FN}, n.Family...), n.Given...), n.Additional...), n.Prefix...), n.Suffix...)
	case "name/given":
		out = n.Given
	case "name/surname", "name/surname2":
		out = n.Family
	case "nickname":
		out = c.Nicknames
	case "organization":
		out = c.Org
	case "email":
		values(c.Emails)
	case "phone":
		values(c.Phones)
	case "onlineService":
		values(c.IMPPs)
	case "address":
		for _, a := range c.Addresses {
			for _, l := range [][]string{a.POBox, a.Ext, a.Street, a.Locality, a.Region, a.Code, a.Country} {
				out = append(out, l...)
			}
		}
	case "note":
		out = []string{c.Note}
	case "text":
		for _, w := range []string{"name", "nickname", "organization", "email", "phone", "onlineService", "address", "note"} {
			out = append(out, cardText(c, w)...)
		}
		out = append(out, c.Title, c.Role)
		out = append(out, c.Categories...)
		values(c.URLs)
	}
	return out
}

// revTime reads REV, for the updated filters and sort.
func revTime(rev string) time.Time {
	for _, layout := range []string{"20060102T150405Z", time.RFC3339, "2006-01-02T15:04:05Z0700", "20060102T150405Z0700"} {
		if t, err := time.Parse(layout, rev); err == nil {
			return t
		}
	}
	return time.Time{}
}

// cardMatches evaluates a FilterCondition or FilterOperator.
func cardMatches(f jsobj.Object, g *dav.Granted, card *vcard.Card) (bool, error) {
	if op, ok := f["operator"].(string); ok {
		conds, _ := f["conditions"].([]any)
		results := make([]bool, 0, len(conds))
		for _, x := range conds {
			sub, ok := x.(jsobj.Object)
			if !ok {
				return false, invalidArguments("a condition is not an object")
			}
			r, err := cardMatches(sub, g, card)
			if err != nil {
				return false, err
			}
			results = append(results, r)
		}
		switch op {
		case "AND":
			return !slices.Contains(results, false), nil
		case "OR":
			return slices.Contains(results, true), nil
		case "NOT":
			return !slices.Contains(results, true), nil
		}
		return false, invalidArguments("unknown operator %q", op)
	}
	for k, v := range f {
		s, isString := v.(string)
		var ok bool
		switch k {
		case "inAddressBook":
			ok = isString && s == collectionID(g.ID)
		case "uid":
			ok = isString && card.UID == s
		case "hasMember":
			ok = isString && slices.ContainsFunc(card.Members, func(m vcard.Member) bool { return m.URI == s })
		case "kind":
			kind := strings.ToLower(card.Kind)
			if kind == "" {
				kind = "individual"
			}
			ok = isString && kind == s
		case "updatedBefore", "updatedAfter":
			t, err := time.Parse(time.RFC3339, s)
			if !isString || err != nil {
				return false, invalidArguments("%s is a UTCDate", k)
			}
			rev := revTime(card.Rev)
			ok = !rev.IsZero() && (k == "updatedBefore" && rev.Before(t) || k == "updatedAfter" && !rev.Before(t))
		case "text", "name", "name/given", "name/surname", "name/surname2", "nickname", "organization", "email",
			"phone", "onlineService", "address", "note":
			ok = isString && anyContains(cardText(card, k), s)
		case "createdBefore", "createdAfter":
			return false, errorOf("unsupportedFilter", "vCards do not say when they were created")
		default:
			return false, errorOf("unsupportedFilter", "unknown condition %q", k)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func (c *call) contactQuery(args jsobj.Object) (any, error) {
	var in queryArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	a, err := c.account(in.AccountID, CapContacts)
	if err != nil {
		return nil, err
	}
	state, _, _, err := c.state(contactCards, a)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id   string
		card *vcard.Card
	}
	var hits []hit
	for _, g := range a.books {
		models, err := c.req.dav.Models(&g, dav.VCARD, nil)
		if err != nil {
			return nil, err
		}
		for i := range models {
			card := models[i].Card
			if in.Filter != nil {
				ok, err := cardMatches(in.Filter, &g, card)
				if err != nil {
					return nil, err
				}
				if !ok {
					continue
				}
			}
			hits = append(hits, hit{resourceID(g.ID, models[i].URI), card})
		}
	}
	var keys []func(h hit) string
	var asc []bool
	for _, s := range in.Sort {
		var key func(h hit) string
		switch s.Property {
		case "updated":
			key = func(h hit) string { return revTime(h.card.Rev).UTC().Format(time.RFC3339) }
		case "name/given":
			key = func(h hit) string { return fold.String(strings.Join(nameOf(h.card).Given, " ")) }
		case "name/surname", "name/surname2":
			key = func(h hit) string { return fold.String(strings.Join(nameOf(h.card).Family, " ")) }
		default:
			return nil, errorOf("unsupportedSort", "cannot sort by %q", s.Property)
		}
		keys = append(keys, key)
		asc = append(asc, s.IsAscending == nil || *s.IsAscending)
	}
	slices.SortStableFunc(hits, func(x, y hit) int {
		for i, k := range keys {
			if d := strings.Compare(k(x), k(y)); d != 0 {
				if !asc[i] {
					return -d
				}
				return d
			}
		}
		return strings.Compare(x.id, y.id)
	})
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	return window(a.id, state, ids, in)
}

func nameOf(c *vcard.Card) *vcard.Name {
	if c.N == nil {
		return &vcard.Name{}
	}
	return c.N
}

// window applies position, anchor and limit (RFC 8620 section 5.5) to the
// sorted ids of a query.
func window(accountID, state string, ids []string, in queryArgs) (any, error) {
	total := len(ids)
	pos := in.Position
	if in.Anchor != nil {
		i := slices.Index(ids, *in.Anchor)
		if i < 0 {
			return nil, errorOf("anchorNotFound", "the anchor is not in the results")
		}
		pos = max(0, i+in.AnchorOffset)
	} else if pos < 0 {
		pos = max(0, total+pos)
	}
	pos = min(pos, total)
	end := total
	limit := 0
	if in.Limit != nil {
		if *in.Limit < 0 {
			return nil, invalidArguments("limit is negative")
		}
		limit = *in.Limit
		end = min(total, pos+limit)
	}
	out := map[string]any{
		"accountId": accountID, "queryState": state, "canCalculateChanges": false,
		"position": pos, "ids": ids[pos:end],
	}
	if in.CalculateTotal {
		out["total"] = total
	}
	if in.Limit != nil {
		out["limit"] = limit
	}
	return out, nil
}

func intOf(v any) (int, bool) {
	n := jsobj.Reader{Obj: jsobj.Object{"v": v}}.Int("v")
	if n == nil {
		return 0, false
	}
	return *n, true
}
