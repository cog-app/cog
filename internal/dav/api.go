package dav

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/vcard"
	"git.duckfam.us/jj-software/cog/plugin"
)

// The DAV core outside DAV requests: what JMAP (internal/jmap) and the agent
// API read and write. Everything goes through the same backends, access
// checks, scheduling and version counters as a DAV request, so a change
// made one way is a change seen every way (DESIGN/dav-storage.md section 9).

// Model is a resource with the model its text is generated from, for
// representations other than iCalendar and vCard (JSCalendar, JSContact).
type Model struct {
	Resource
	Object *ical.Object // calendar components
	Card   *vcard.Card  // vCards
}

// ModelReader is implemented by backends that hand out the models of their
// resources.
type ModelReader interface {
	// Models returns the models of the resources among uris that exist, or
	// of every resource in c when uris is nil.
	Models(owner string, c *Collection, uris []string) ([]Model, error)
}

// Models implements ModelReader for a table whose Load is set.
func (t *Table) Models(owner string, c *Collection, uris []string) ([]Model, error) {
	if t.Load == nil {
		return nil, fmt.Errorf("dav: %s has no models", t.Name)
	}
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	where, args := "collection_id = ?", []any{c.ID}
	if uris != nil {
		if len(uris) == 0 {
			return nil, nil
		}
		where += " AND uri IN (?" + repeat(",?", len(uris)-1) + ")"
		for _, u := range uris {
			args = append(args, u)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.Name, err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT `+t.columns()+` FROM `+t.Name+` WHERE `+where+` ORDER BY uri`, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", t.Name, err)
	}
	res, ids, err := t.scan(rows)
	if err != nil || len(res) == 0 {
		return nil, err
	}
	loaded, err := t.Load(tx, c, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(res))
	for i, r := range res {
		m, ok := loaded[ids[i]]
		if !ok {
			continue
		}
		m.Resource = r
		out = append(out, m)
	}
	return out, nil
}

func repeat(s string, n int) string { return string(bytes.Repeat([]byte(s), n)) }

// Rights are what a user may do with a collection.
type Rights struct {
	Owner    bool // the collection is theirs: they may change and delete it
	Read     bool // read its resources
	Write    bool // write its resources (never in a collection Cog fills)
	FreeBusy bool // see when its owner is busy
	Schedule bool // send invitations and replies in the owner's name
}

func (a access) rights() Rights {
	return Rights{Owner: a.owner, Read: a.read(), Write: a.write(), FreeBusy: a.freeBusy(), Schedule: a.schedule()}
}

// Granted is a collection a user may see, with what they may do in it.
type Granted struct {
	Collection
	Owner  *User
	Rights Rights
	access access
}

// Session is one user's access to the DAV core outside a DAV request. It
// reads the shares that reach the user once.
type Session struct {
	s      *Server
	User   *User
	bes    []Backend
	shared shareMap
	owners map[string]*User
}

// Session returns u's access to collections.
func (s *Server) Session(u *User) (*Session, error) {
	shared, err := sharesFor(s.Shares, u.ID)
	if err != nil {
		return nil, err
	}
	return &Session{s: s, User: u, bes: s.Backends(), shared: shared, owners: map[string]*User{u.ID: u}}, nil
}

func (v *Session) owner(id string) (*User, error) {
	if u, ok := v.owners[id]; ok {
		return u, nil
	}
	u, err := v.s.Users.ByID(id)
	if err != nil {
		return nil, err
	}
	v.owners[id] = u
	return u, nil
}

func (v *Session) grant(c *Collection) (*Granted, error) {
	a := accessTo(c, c.OwnerID == v.User.ID, 0)
	if !a.owner {
		a = accessTo(c, false, v.shared.perms(c))
	}
	if !a.freeBusy() {
		return nil, ErrNotFound
	}
	o, err := v.owner(c.OwnerID)
	if err != nil {
		return nil, err
	}
	return &Granted{Collection: *c, Owner: o, Rights: a.rights(), access: a}, nil
}

// Collections returns the collections the user may see that hold any of
// comps: their own (the home's default made if they have none) and those
// shared with them, by ID.
func (v *Session) Collections(comps ...Component) ([]Granted, error) {
	var out []Granted
	homes := map[Home]bool{}
	for _, comp := range comps {
		homes[comp.Home()] = true
	}
	for _, h := range []Home{HomeCalendars, HomeAddressBooks} {
		if !homes[h] {
			continue
		}
		own, err := v.s.Collections.EnsureDefault(v.User.ID, h)
		if err != nil {
			return nil, err
		}
		for i := range own {
			if !slices.ContainsFunc(comps, own[i].Has) {
				continue
			}
			g, err := v.grant(&own[i])
			if err != nil {
				return nil, err
			}
			out = append(out, *g)
		}
	}
	ids := make([]CollectionID, 0, len(v.shared))
	for id := range v.shared {
		ids = append(ids, id)
	}
	cols, err := v.s.Collections.ByIDs(ids)
	if err != nil {
		return nil, err
	}
	for i := range cols {
		c := &cols[i]
		if c.OwnerID == v.User.ID || !slices.ContainsFunc(comps, c.Has) {
			continue
		}
		g, err := v.grant(c)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b Granted) int { return int(a.ID - b.ID) })
	return out, nil
}

// Collection returns one collection the user may see, or ErrNotFound.
func (v *Session) Collection(id CollectionID) (*Granted, error) {
	c, err := v.s.Collections.ByID(id)
	if err != nil {
		return nil, err
	}
	return v.grant(c)
}

// backend returns the enabled backend that stores comp in g, or nil.
func (v *Session) backend(g *Granted, comp Component) Backend {
	if !g.Has(comp) {
		return nil
	}
	for _, b := range v.bes {
		if slices.Contains(b.Components(), comp) {
			return b
		}
	}
	return nil
}

// ErrNoBackend reports a component type no enabled plugin stores in a
// collection.
var ErrNoBackend = errors.New("dav: no backend for this component in this collection")

// Models returns the models of g's resources of a component type among
// uris (all when nil). A free/busy-only sharee sees none.
func (v *Session) Models(g *Granted, comp Component, uris []string) ([]Model, error) {
	b := v.backend(g, comp)
	if b == nil {
		return nil, ErrNoBackend
	}
	if !g.Rights.Read {
		return nil, nil
	}
	mr, ok := b.(ModelReader)
	if !ok {
		return nil, ErrNoBackend
	}
	list, err := mr.Models(g.Owner.Username, &g.Collection, uris)
	if err != nil {
		return nil, err
	}
	// A backend storing several types (tasks and journals) lists them all.
	return slices.DeleteFunc(list, func(m Model) bool { return m.Component != comp }), nil
}

// Changes returns what changed in g's resources of the backend storing
// comp since version since of that backend's counter, which is all of
// them for 0.
func (v *Session) Changes(g *Granted, comp Component, since int64) (*Changes, error) {
	b := v.backend(g, comp)
	if b == nil {
		return nil, ErrNoBackend
	}
	return b.Changes(g.Owner.Username, &g.Collection, since, false)
}

// Key returns the key of the backend that stores comp, which names its
// counter in sync tokens, or "".
func (v *Session) Key(comp Component) string {
	for _, b := range v.bes {
		if slices.Contains(b.Components(), comp) {
			return b.Key()
		}
	}
	return ""
}

// States returns the sync state of the backend storing comp in each of
// gs, one query per owner. Collections never written are absent.
func (v *Session) States(gs []Granted, comp Component) (map[CollectionID]davsql.State, error) {
	out := map[CollectionID]davsql.State{}
	byOwner := map[string][]CollectionID{}
	var b Backend
	for i := range gs {
		if bb := v.backend(&gs[i], comp); bb != nil {
			b = bb
			byOwner[gs[i].Owner.Username] = append(byOwner[gs[i].Owner.Username], gs[i].ID)
		}
	}
	for owner, ids := range byOwner {
		m, err := b.States(owner, ids)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, m)
	}
	return out, nil
}

// Errors of writes outside DAV, besides ErrNotFound, ErrPrecondition and
// *UIDConflict.
var (
	ErrForbidden   = errors.New("dav: not permitted")
	ErrTooLarge    = errors.New("dav: resource too large")
	ErrInvalidData = errors.New("dav: not a valid resource")
	ErrKindChange  = errResourceKind
)

// Put stores a resource as a DAV PUT would: validated, conditional on
// cond, and, for an event when send is set, scheduled. The resource's
// component must be one g holds.
func (v *Session) Put(g *Granted, uri string, obj *contentline.Component, cond davsql.Conditions, send bool) (*PutResult, error) {
	if !g.Rights.Write {
		return nil, ErrForbidden
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{obj}); err != nil {
		return nil, fmt.Errorf("write %s: %w", uri, err)
	}
	data := b.Bytes()
	if len(data) > maxResourceSize {
		return nil, ErrTooLarge
	}
	parsed, comp, uid, bad := parseObject(g.Home, data)
	if bad.Local != "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidData, bad.Local)
	}
	bes := v.s.backendsOf(&g.Collection)
	if v.backend(g, comp) == nil {
		return nil, ErrNoBackend
	}
	req := &PutRequest{URI: uri, UID: uid, Component: comp, Data: data, Object: parsed, Conditions: cond}
	if comp != VEVENT {
		return v.s.put(bes, g.Owner.Username, &g.Collection, req)
	}
	res, d, err := v.s.putScheduled(bes, &g.Collection, g.Owner, g.access, req, send)
	if err != nil {
		return nil, err
	}
	d.Send()
	return res, nil
}

// Delete removes a resource as a DAV DELETE would, and for an event, when
// send is set, schedules a cancellation or a reply.
func (v *Session) Delete(g *Granted, uri string, cond davsql.Conditions, send bool) error {
	if !g.Rights.Write {
		return ErrForbidden
	}
	d, err := v.s.deleteScheduled(v.s.backendsOf(&g.Collection), &g.Collection, g.Owner, g.access, uri, cond, false, send)
	if err != nil {
		return err
	}
	d.Send()
	return nil
}

// CreateCollection makes a collection in the user's own home, named after
// c.Name (or its display name), with a suffix when the name is taken.
func (v *Session) CreateCollection(c *Collection) error {
	c.OwnerID = v.User.ID
	c.Kind = KindOwn
	if len(c.Components) == 0 {
		return fmt.Errorf("%w: no components", ErrInvalidData)
	}
	for _, comp := range c.Components {
		if comp.Home() != c.Home || !slices.ContainsFunc(v.bes, func(b Backend) bool { return slices.Contains(b.Components(), comp) }) {
			return ErrNoBackend
		}
	}
	if c.Name == "" {
		c.Name = Slug(c.DisplayName, "collection")
	}
	if reserved(c.Home, c.Name) {
		c.Name += "-1"
	}
	return v.s.Collections.CreateNamed(c)
}

// UpdateCollection saves the properties of one of the user's own
// collections, re-deriving its resources' times when its zone changed.
func (v *Session) UpdateCollection(g *Granted, updated *Collection) error {
	if !g.Rights.Owner {
		return ErrForbidden
	}
	if err := v.s.Collections.Update(updated); err != nil {
		return err
	}
	if updated.Timezone == g.Timezone {
		return nil
	}
	for _, b := range v.s.backendsOf(updated) {
		if o, ok := b.(TimezoneObserver); ok {
			if err := o.TimezoneChanged(g.Owner.Username, updated); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteCollection removes one of the user's own collections and its
// resources.
func (v *Session) DeleteCollection(g *Granted) error {
	if !g.Rights.Owner {
		return ErrForbidden
	}
	return v.s.DeleteCollection(g.Owner.Username, &g.Collection)
}

// HTTPStatus maps an error of a write outside DAV to the status a DAV
// request would have had, for logs and messages.
func HTTPStatus(err error) int {
	var pe *PreconditionError
	switch {
	case errors.As(err, &pe):
		return pe.Status
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrPrecondition):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrForbidden), errors.Is(err, plugin.ErrQuotaExceeded):
		return http.StatusForbidden
	}
	return http.StatusInternalServerError
}
