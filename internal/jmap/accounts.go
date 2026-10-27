package jmap

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
)

// account is a user's collections as one user sees them: all of their own,
// or those another user shared with them.
type account struct {
	id    string
	owner *dav.User
	own   bool
	books []dav.Granted // address books
	cals  []dav.Granted // calendars holding events
}

func accountID(userID string) string { return "u" + userID }

// accountList returns the user's accounts: their own first, then one per
// user who shared something with them, by ID.
func (q *request) accountList() ([]*account, error) {
	if q.accounts != nil {
		return sortedAccounts(q.accounts), nil
	}
	comps := []dav.Component{dav.VCARD}
	if q.s.Calendars {
		comps = append(comps, dav.VEVENT)
	}
	cols, err := q.dav.Collections(comps...)
	if err != nil {
		return nil, err
	}
	q.accounts = map[string]*account{accountID(q.user.ID): {id: accountID(q.user.ID), owner: q.user, own: true}}
	for _, g := range cols {
		id := accountID(g.OwnerID)
		a := q.accounts[id]
		if a == nil {
			a = &account{id: id, owner: g.Owner}
			q.accounts[id] = a
		}
		switch {
		case g.Has(dav.VCARD):
			if g.Rights.Read {
				a.books = append(a.books, g)
			}
		case g.Has(dav.VEVENT):
			a.cals = append(a.cals, g)
		}
	}
	for id, a := range q.accounts {
		if !a.own && len(a.books) == 0 && len(a.cals) == 0 {
			delete(q.accounts, id)
		}
	}
	return sortedAccounts(q.accounts), nil
}

func sortedAccounts(m map[string]*account) []*account {
	out := make([]*account, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b *account) int {
		switch {
		case a.own:
			return -1
		case b.own:
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
	return out
}

// caps lists the capabilities the account has data for.
func (a *account) caps(s *Server) []string {
	var out []string
	if a.own || len(a.books) > 0 {
		out = append(out, CapContacts)
	}
	if s.Calendars && (a.own || len(a.cals) > 0) {
		out = append(out, CapCalendars)
	}
	return out
}

func (a *account) writable() bool {
	if a.own {
		return true
	}
	for _, g := range append(slices.Clone(a.books), a.cals...) {
		if g.Rights.Write {
			return true
		}
	}
	return false
}

func (a *account) describe(s *Server) map[string]any {
	name := a.owner.DisplayName
	if name == "" {
		name = a.owner.Username
	}
	caps := map[string]any{}
	for _, c := range a.caps(s) {
		switch c {
		case CapContacts:
			caps[c] = map[string]any{"maxAddressBooksPerCard": 1, "mayCreateAddressBook": a.own}
		case CapCalendars:
			caps[c] = map[string]any{
				"maxCalendarsPerEvent":     1,
				"minDateTime":              "1900-01-01T00:00:00Z",
				"maxDateTime":              "2200-01-01T00:00:00Z",
				"maxExpandedQueryDuration": "P400D",
				"maxParticipantsPerEvent":  nil,
				"mayCreateCalendar":        a.own,
			}
		}
	}
	return map[string]any{"name": name, "isPersonal": a.own, "isReadOnly": !a.writable(), "accountCapabilities": caps}
}

// account returns the account a method names, if it has data for the
// method's capability.
func (c *call) account(id, capability string) (*account, error) {
	accounts, err := c.req.accountList()
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if a.id != id {
			continue
		}
		if !slices.Contains(a.caps(c.s), capability) {
			return nil, errorOf("accountNotSupportedByMethod", "account %s has no data for %s", id, capability)
		}
		return a, nil
	}
	return nil, errorOf("accountNotFound", "no account %s", id)
}

// --- Ids ---

// Collection ids are "c" and the collection's ID; resource ids "r", the
// collection's ID, "-", and the resource's URI in base64url. A URI too long
// for a 255-octet id is named by its hash ("h" instead of "r") and found by
// listing its collection.
const maxURIInID = 180

func collectionID(id dav.CollectionID) string { return "c" + id.String() }

func parseCollectionID(s string) (dav.CollectionID, bool) {
	rest, ok := strings.CutPrefix(s, "c")
	n, err := strconv.ParseInt(rest, 10, 64)
	if !ok || err != nil || n <= 0 {
		return 0, false
	}
	return dav.CollectionID(n), true
}

func resourceID(c dav.CollectionID, uri string) string {
	if len(uri) > maxURIInID {
		return "h" + c.String() + "-" + shortHash(uri)
	}
	return "r" + c.String() + "-" + base64.RawURLEncoding.EncodeToString([]byte(uri))
}

// itemRef is a resource id read back: its collection, and its URI or, for
// a long one, the hash it is named by.
type itemRef struct {
	coll dav.CollectionID
	uri  string
	hash string
}

func parseResourceID(s string) (itemRef, bool) {
	if len(s) < 2 || (s[0] != 'r' && s[0] != 'h') {
		return itemRef{}, false
	}
	num, rest, ok := strings.Cut(s[1:], "-")
	n, err := strconv.ParseInt(num, 10, 64)
	if !ok || err != nil || n <= 0 {
		return itemRef{}, false
	}
	ref := itemRef{coll: dav.CollectionID(n)}
	if s[0] == 'h' {
		ref.hash = rest
		return ref, true
	}
	uri, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(uri) == 0 {
		return itemRef{}, false
	}
	ref.uri = string(uri)
	return ref, true
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// --- States ---

// collectionsState is the state of an account's collections of one kind:
// each one's ID, properties version and the user's rights, so a rename or
// a changed share is a change.
func collectionsState(gs []dav.Granted) string {
	var b strings.Builder
	b.WriteString("v1")
	for _, g := range gs {
		fmt.Fprintf(&b, ";%d.%d.%d", g.ID, g.PropsVersion, rightsBits(g.Rights))
	}
	return b.String()
}

func rightsBits(r dav.Rights) int {
	n := 0
	for i, b := range []bool{r.Owner, r.Read, r.Write, r.FreeBusy, r.Schedule} {
		if b {
			n |= 1 << i
		}
	}
	return n
}

// collectionEntry is one collection in a collections state.
type collectionEntry struct {
	id      dav.CollectionID
	version string // properties version and rights
}

func parseCollectionsState(s string) ([]collectionEntry, bool) {
	parts := strings.Split(s, ";")
	if parts[0] != "v1" {
		return nil, false
	}
	var out []collectionEntry
	for _, p := range parts[1:] {
		id, rest, ok := strings.Cut(p, ".")
		n, err := strconv.ParseInt(id, 10, 64)
		if !ok || err != nil {
			return nil, false
		}
		out = append(out, collectionEntry{dav.CollectionID(n), rest})
	}
	return out, true
}

// itemsState is the state of the resources of one backend in an account's
// collections: the collection sync tokens' part for that backend (section 6
// of DESIGN/dav-storage.md), e.g. "12-c42,15-c3".
type itemsState map[dav.CollectionID]int64

func (st itemsState) String(key string) string {
	ids := make([]dav.CollectionID, 0, len(st))
	for id := range st {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = id.String() + "-" + key + strconv.FormatInt(st[id], 10)
	}
	return "v1:" + strings.Join(parts, ",")
}

func parseItemsState(s, key string) (itemsState, bool) {
	rest, ok := strings.CutPrefix(s, "v1:")
	if !ok {
		return nil, false
	}
	out := itemsState{}
	if rest == "" {
		return out, true
	}
	for p := range strings.SplitSeq(rest, ",") {
		id, tok, ok := strings.Cut(p, "-")
		n, err := strconv.ParseInt(id, 10, 64)
		v, ok2 := strings.CutPrefix(tok, key)
		ver, err2 := strconv.ParseInt(v, 10, 64)
		if !ok || !ok2 || err != nil || err2 != nil || ver < 0 {
			return nil, false
		}
		out[dav.CollectionID(n)] = ver
	}
	return out, true
}
