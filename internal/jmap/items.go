package jmap

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/jscal"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/plugin"
)

// itemType is a JMAP data type whose records are DAV resources: ContactCard
// (vCards) or CalendarEvent (events).
type itemType struct {
	name       string // "ContactCard"
	capability string
	comp       dav.Component
	ext        string // of the URIs of resources it creates
	// collProp names the collections a record is in ("addressBookIds").
	collProp string
	// colls returns the account's collections of this type.
	colls func(a *account) []dav.Granted
	// serverSet are properties JMAP computes, which decoding ignores.
	serverSet []string
	codec     itemCodec
	// decorate adds what JMAP computes to a record.
	decorate func(c *call, g *dav.Granted, m *dav.Model, obj jsobj.Object)
}

// itemCodec maps a type's models to JSON and records Cog's edits.
type itemCodec interface {
	encode(m *dav.Model) (jsobj.Object, error)
	decode(obj jsobj.Object) (*dav.Model, error)
	uid(m *dav.Model) string
	setUID(m *dav.Model, uid string)
	// created records a new record's bookkeeping; edited an edit's,
	// compared with what it replaces.
	created(m *dav.Model, now time.Time)
	edited(old, cur *dav.Model, now time.Time)
	render(m *dav.Model) *contentline.Component
}

// state returns the type's state in an account.
func (c *call) state(t *itemType, a *account) (string, itemsState, map[dav.CollectionID]davsql.State, error) {
	colls := t.colls(a)
	states, err := c.req.dav.States(colls, t.comp)
	if err != nil {
		return "", nil, nil, err
	}
	st := itemsState{}
	for _, g := range colls {
		st[g.ID] = states[g.ID].Token
	}
	return st.String(c.req.dav.Key(t.comp)), st, states, nil
}

func (t *itemType) granted(a *account, id dav.CollectionID) *dav.Granted {
	for _, g := range t.colls(a) {
		if g.ID == id {
			return &g
		}
	}
	return nil
}

// record is a model as a JMAP record: its JSON with its id and
// collections.
func (c *call) record(t *itemType, g *dav.Granted, m *dav.Model) (jsobj.Object, error) {
	obj, err := t.codec.encode(m)
	if err != nil {
		return nil, err
	}
	obj["id"] = resourceID(g.ID, m.URI)
	obj[t.collProp] = jsobj.Object{collectionID(g.ID): true}
	if t.decorate != nil {
		t.decorate(c, g, m, obj)
	}
	return obj, nil
}

// lookup finds the records with these ids, by collection: the resource of
// each id that exists and the user may read.
func (c *call) lookup(t *itemType, a *account, ids []string) (map[string]*dav.Model, map[string]*dav.Granted, error) {
	byColl := map[dav.CollectionID][]string{}
	hashed := map[dav.CollectionID]bool{}
	refs := map[string]itemRef{}
	for _, id := range ids {
		ref, ok := parseResourceID(c.req.resolveID(id))
		if !ok {
			continue
		}
		refs[id] = ref
		if ref.hash != "" {
			hashed[ref.coll] = true
		} else {
			byColl[ref.coll] = append(byColl[ref.coll], ref.uri)
		}
	}
	found := map[dav.CollectionID]map[string]*dav.Model{}
	grants := map[dav.CollectionID]*dav.Granted{}
	for coll := range mergeKeys(byColl, hashed) {
		g := t.granted(a, coll)
		if g == nil {
			continue
		}
		grants[coll] = g
		var uris []string
		if !hashed[coll] {
			uris = byColl[coll]
		}
		list, err := c.req.dav.Models(g, t.comp, uris)
		if err != nil {
			return nil, nil, err
		}
		m := map[string]*dav.Model{}
		for i := range list {
			m[list[i].URI] = &list[i]
			if len(list[i].URI) > maxURIInID {
				m["#"+shortHash(list[i].URI)] = &list[i]
			}
		}
		found[coll] = m
	}
	out := map[string]*dav.Model{}
	gs := map[string]*dav.Granted{}
	for id, ref := range refs {
		key := ref.uri
		if ref.hash != "" {
			key = "#" + ref.hash
		}
		if m := found[ref.coll][key]; m != nil {
			out[id] = m
			gs[id] = grants[ref.coll]
		}
	}
	return out, gs, nil
}

func mergeKeys(a map[dav.CollectionID][]string, b map[dav.CollectionID]bool) map[dav.CollectionID]bool {
	out := map[dav.CollectionID]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// resolveID turns "#creationId" into the id it made in this request.
func (q *request) resolveID(id string) string {
	if cid, ok := strings.CutPrefix(id, "#"); ok {
		if made, ok := q.created[cid]; ok {
			return made
		}
	}
	return id
}

// --- /get ---

type getArgs struct {
	AccountID  string    `json:"accountId"`
	IDs        *[]string `json:"ids"`
	Properties *[]string `json:"properties"`
}

// filterProps keeps the properties asked for, and always the id.
func filterProps(obj jsobj.Object, props *[]string) jsobj.Object {
	if props == nil {
		return obj
	}
	out := jsobj.Object{"id": obj["id"]}
	for _, p := range *props {
		if v, ok := obj[p]; ok {
			out[p] = v
		}
	}
	return out
}

// getItems answers /get. ignored are the type's own arguments, accepted
// and not acted on.
func (c *call) getItems(t *itemType, args jsobj.Object, ignored ...string) (any, error) {
	var in getArgs
	args = jsobj.Clone(args).(jsobj.Object)
	for _, k := range ignored {
		delete(args, k)
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	state, _, _, err := c.state(t, a)
	if err != nil {
		return nil, err
	}
	list := []any{}
	notFound := []string{}
	if in.IDs == nil {
		n := 0
		for _, g := range t.colls(a) {
			models, err := c.req.dav.Models(&g, t.comp, nil)
			if err != nil {
				return nil, err
			}
			n += len(models)
			if n > maxObjectsInGet {
				return nil, errorOf("requestTooLarge", "more than %d records; ask for ids", maxObjectsInGet)
			}
			for i := range models {
				obj, err := c.record(t, &g, &models[i])
				if err != nil {
					return nil, err
				}
				list = append(list, filterProps(obj, in.Properties))
			}
		}
	} else {
		ids := *in.IDs
		if len(ids) > maxObjectsInGet {
			return nil, errorOf("requestTooLarge", "more than %d ids", maxObjectsInGet)
		}
		models, grants, err := c.lookup(t, a, ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			m := models[id]
			if m == nil {
				notFound = append(notFound, id)
				continue
			}
			obj, err := c.record(t, grants[id], m)
			if err != nil {
				return nil, err
			}
			list = append(list, filterProps(obj, in.Properties))
		}
	}
	return map[string]any{"accountId": a.id, "state": state, "list": list, "notFound": notFound}, nil
}

// --- /changes ---

type changesArgs struct {
	AccountID  string `json:"accountId"`
	SinceState string `json:"sinceState"`
	MaxChanges *int   `json:"maxChanges"`
}

type change struct {
	version int64
	id      string
	kind    int // 0 created, 1 updated, 2 destroyed
}

func (c *call) itemChanges(t *itemType, args jsobj.Object) (any, error) {
	var in changesArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if in.MaxChanges != nil && *in.MaxChanges <= 0 {
		return nil, invalidArguments("maxChanges must be positive")
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	key := c.req.dav.Key(t.comp)
	old, ok := parseItemsState(in.SinceState, key)
	if !ok {
		return nil, errorOf("cannotCalculateChanges", "unknown state")
	}
	cur, _, states, err := c.stateWithMap(t, a)
	if err != nil {
		return nil, err
	}
	for id := range old {
		if _, ok := cur[id]; !ok {
			return nil, errorOf("cannotCalculateChanges", "a collection is gone; get everything again")
		}
	}
	limit := maxObjectsInGet
	if in.MaxChanges != nil {
		limit = *in.MaxChanges
	}
	next := itemsState{}
	maps.Copy(next, old)
	var out []change
	more := false
	for _, g := range t.colls(a) {
		since := old[g.ID]
		st := states[g.ID]
		if since > st.Token || since > 0 && since < st.Floor {
			return nil, errorOf("cannotCalculateChanges", "the state is too old or unknown")
		}
		if since == st.Token {
			next[g.ID] = since
			continue
		}
		if more {
			continue
		}
		ch, err := c.req.dav.Changes(&g, t.comp, since)
		if err != nil {
			return nil, err
		}
		var list []change
		for _, r := range ch.Changed {
			if r.Component != t.comp {
				continue
			}
			kind := 1
			if r.Created > since || since == 0 {
				kind = 0
			}
			list = append(list, change{r.Version, resourceID(g.ID, r.URI), kind})
		}
		for i, uri := range ch.Deleted {
			v := ch.State.Token
			if i < len(ch.DeletedAt) {
				v = ch.DeletedAt[i]
			}
			list = append(list, change{v, resourceID(g.ID, uri), 2})
		}
		slices.SortStableFunc(list, func(x, y change) int { return int(x.version - y.version) })
		room := limit - len(out)
		if len(list) <= room {
			out = append(out, list...)
			next[g.ID] = ch.State.Token
			continue
		}
		// Only part of this collection fits: stop at a version, never
		// inside one, and leave the rest for the next call.
		n := room
		for n > 0 && n < len(list) && list[n].version == list[n-1].version {
			n--
		}
		if n == 0 {
			if len(out) == 0 {
				return nil, errorOf("cannotCalculateChanges", "more changes at one version than maxChanges")
			}
		} else {
			out = append(out, list[:n]...)
			next[g.ID] = list[n-1].version
		}
		more = true
	}
	if !more {
		next = cur
	}
	created, updated, destroyed := []string{}, []string{}, []string{}
	for _, ch := range out {
		switch ch.kind {
		case 0:
			created = append(created, ch.id)
		case 1:
			updated = append(updated, ch.id)
		default:
			destroyed = append(destroyed, ch.id)
		}
	}
	return map[string]any{
		"accountId": a.id, "oldState": in.SinceState, "newState": next.String(key), "hasMoreChanges": more,
		"created": created, "updated": updated, "destroyed": destroyed,
	}, nil
}

func (c *call) stateWithMap(t *itemType, a *account) (itemsState, string, map[dav.CollectionID]davsql.State, error) {
	s, st, states, err := c.state(t, a)
	return st, s, states, err
}

// --- /set ---

type setArgs struct {
	AccountID string                  `json:"accountId"`
	IfInState *string                 `json:"ifInState"`
	Create    map[string]jsobj.Object `json:"create"`
	Update    map[string]jsobj.Object `json:"update"`
	Destroy   []string                `json:"destroy"`
}

// setError is a SetError (RFC 8620 section 5.3).
type setError struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Properties  []string `json:"properties,omitempty"`
}

func setErrorOf(err error) (*setError, error) {
	var inv *jsobj.Invalid
	var pe *dav.PreconditionError
	var se *setError
	switch {
	case errors.As(err, &se):
		return se, nil
	case errors.As(err, &inv):
		return &setError{Type: "invalidProperties", Properties: inv.Properties}, nil
	case errors.Is(err, dav.ErrForbidden):
		return &setError{Type: "forbidden", Description: "the collection may not be written"}, nil
	case errors.Is(err, dav.ErrNotFound):
		return &setError{Type: "notFound"}, nil
	case errors.Is(err, dav.ErrTooLarge):
		return &setError{Type: "tooLarge"}, nil
	case errors.Is(err, plugin.ErrQuotaExceeded):
		return &setError{Type: "overQuota"}, nil
	case errors.Is(err, dav.ErrInvalidData), errors.Is(err, dav.ErrNoBackend), errors.Is(err, dav.ErrKindChange):
		return &setError{Type: "invalidProperties", Description: err.Error()}, nil
	case errors.As(err, &pe):
		return &setError{Type: "invalidProperties", Description: "precondition " + pe.Name.Local}, nil
	}
	if _, ok := dav.IsUIDConflict(err); ok {
		return &setError{Type: "invalidProperties", Properties: []string{"uid"}, Description: "another record in the collection has this uid"}, nil
	}
	return nil, err
}

// target reads the collection a record is to be in: exactly one, by id or
// by the creation id of one made earlier in the request.
func (c *call) target(t *itemType, a *account, obj jsobj.Object) (*dav.Granted, *setError) {
	set, _ := obj[t.collProp].(jsobj.Object)
	var ids []string
	for k, v := range set {
		if v == true {
			ids = append(ids, k)
		}
	}
	if len(ids) != 1 {
		return nil, &setError{Type: "invalidProperties", Properties: []string{t.collProp}, Description: "a record is in exactly one collection"}
	}
	id, ok := parseCollectionID(c.req.resolveID(ids[0]))
	if !ok {
		return nil, &setError{Type: "invalidProperties", Properties: []string{t.collProp}}
	}
	g := t.granted(a, id)
	if g == nil {
		// Made earlier in this request: the account's list predates it.
		if fresh, err := c.req.dav.Collection(id); err == nil && fresh.OwnerID == a.owner.ID && fresh.Has(t.comp) {
			g = fresh
		}
	}
	if g == nil {
		return nil, &setError{Type: "invalidProperties", Properties: []string{t.collProp}, Description: "no such collection"}
	}
	return g, nil
}

// clientJSON is a record as the codec reads it: without what JMAP adds.
func (t *itemType) clientJSON(obj jsobj.Object) jsobj.Object {
	out := jsobj.Clone(obj).(jsobj.Object)
	delete(out, "id")
	delete(out, t.collProp)
	for _, p := range t.serverSet {
		delete(out, p)
	}
	return out
}

// resourceName names a new resource after its UID when that is safe in a
// path, as clients name theirs, and a UUID otherwise.
func resourceName(uid, ext string) string {
	ok := uid != "" && len(uid) <= 120
	for _, r := range uid {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.@", r)) {
			ok = false
			break
		}
	}
	if !ok {
		return uuid.NewString() + ext
	}
	return uid + ext
}

// sendArg is the calendars' sendSchedulingMessages, false for contacts.
type setOptions struct {
	send bool
}

func (c *call) setItems(t *itemType, args jsobj.Object, opts setOptions) (any, error) {
	var in setArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	if len(in.Create)+len(in.Update)+len(in.Destroy) > maxObjectsInSet {
		return nil, errorOf("requestTooLarge", "more than %d changes", maxObjectsInSet)
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	oldState, _, _, err := c.state(t, a)
	if err != nil {
		return nil, err
	}
	if in.IfInState != nil && *in.IfInState != oldState {
		return nil, errorOf("stateMismatch", "the state is %s", oldState)
	}
	now := c.s.Clock()
	created, notCreated := map[string]any{}, map[string]any{}
	updated, notUpdated := map[string]any{}, map[string]any{}
	destroyed, notDestroyed := []string{}, map[string]any{}

	fail := func(m map[string]any, id string, err error) error {
		se, err := setErrorOf(err)
		if err != nil {
			return err
		}
		m[id] = se
		return nil
	}

	for _, cid := range sortedKeys(in.Create) {
		obj := in.Create[cid]
		if _, ok := obj["id"]; ok {
			notCreated[cid] = &setError{Type: "invalidProperties", Properties: []string{"id"}}
			continue
		}
		g, se := c.target(t, a, obj)
		if se != nil {
			notCreated[cid] = se
			continue
		}
		body := t.clientJSON(obj)
		generated := false
		if u, _ := body["uid"].(string); u == "" {
			body["uid"] = uuid.NewString()
			generated = true
		}
		m, err := t.codec.decode(body)
		if err != nil {
			if err := fail(notCreated, cid, err); err != nil {
				return nil, err
			}
			continue
		}
		t.codec.created(m, now)
		uri := resourceName(t.codec.uid(m), t.ext)
		if _, err := c.req.dav.Put(g, uri, t.codec.render(m), davsql.Conditions{IfNoneMatchAny: true}, opts.send); errors.Is(err, dav.ErrPrecondition) {
			// The name is taken by another resource: use a fresh one.
			uri = uuid.NewString() + t.ext
			_, err = c.req.dav.Put(g, uri, t.codec.render(m), davsql.Conditions{IfNoneMatchAny: true}, opts.send)
			if err != nil {
				if err := fail(notCreated, cid, err); err != nil {
					return nil, err
				}
				continue
			}
		} else if err != nil {
			if err := fail(notCreated, cid, err); err != nil {
				return nil, err
			}
			continue
		}
		id := resourceID(g.ID, uri)
		c.req.created[cid] = id
		stored, err := c.reread(t, g, uri)
		if err != nil {
			return nil, err
		}
		result := jsobj.Object{"id": id}
		if stored != nil {
			for k, v := range stored {
				if k == "id" || strings.HasPrefix(k, jsobj.Vendor) {
					continue
				}
				if sent, ok := obj[k]; !ok || !jsobj.Equal(sent, v) {
					result[k] = v
				}
			}
		}
		if generated {
			result["uid"] = t.codec.uid(m)
		}
		created[cid] = result
	}

	for _, rawID := range sortedKeys(in.Update) {
		patch := in.Update[rawID]
		id := c.req.resolveID(rawID)
		res, err := c.updateItem(t, a, id, patch, now, opts)
		if err != nil {
			if err := fail(notUpdated, rawID, err); err != nil {
				return nil, err
			}
			continue
		}
		updated[id] = res
	}

	for _, rawID := range in.Destroy {
		id := c.req.resolveID(rawID)
		models, grants, err := c.lookup(t, a, []string{id})
		if err != nil {
			return nil, err
		}
		m := models[id]
		if m == nil {
			notDestroyed[rawID] = &setError{Type: "notFound"}
			continue
		}
		if err := c.req.dav.Delete(grants[id], m.URI, davsql.Conditions{}, opts.send); err != nil {
			if err := fail(notDestroyed, rawID, err); err != nil {
				return nil, err
			}
			continue
		}
		destroyed = append(destroyed, id)
	}

	newState, _, _, err := c.state(t, a)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"accountId": a.id, "oldState": oldState, "newState": newState}
	put := func(k string, m map[string]any) {
		if len(m) > 0 {
			out[k] = m
		} else {
			out[k] = nil
		}
	}
	put("created", created)
	put("notCreated", notCreated)
	put("updated", updated)
	put("notUpdated", notUpdated)
	put("notDestroyed", notDestroyed)
	if len(destroyed) > 0 {
		out["destroyed"] = destroyed
	} else {
		out["destroyed"] = nil
	}
	return out, nil
}

// reread returns a stored record as JSON, or nil when it is gone.
func (c *call) reread(t *itemType, g *dav.Granted, uri string) (jsobj.Object, error) {
	list, err := c.req.dav.Models(g, t.comp, []string{uri})
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return c.record(t, g, &list[0])
}

// updateItem applies a patch to a record: read, patched as JSON, read
// back, stored on condition that nothing changed it meanwhile; tried again
// when something did.
func (c *call) updateItem(t *itemType, a *account, id string, patch jsobj.Object, now time.Time, opts setOptions) (any, error) {
	for attempt := 0; ; attempt++ {
		models, grants, err := c.lookup(t, a, []string{id})
		if err != nil {
			return nil, err
		}
		old := models[id]
		if old == nil {
			return nil, &setError{Type: "notFound"}
		}
		g := grants[id]
		cur, err := c.record(t, g, old)
		if err != nil {
			return nil, err
		}
		for k := range patch {
			if top, _, _ := strings.Cut(k, "/"); top == "id" {
				return nil, &setError{Type: "invalidProperties", Properties: []string{k}, Description: "set by the server"}
			}
		}
		patched := jsobj.Clone(cur).(jsobj.Object)
		if err := jscal.ApplyPatch(patched, patch); err != nil {
			return nil, &setError{Type: "invalidPatch", Description: err.Error()}
		}
		if !jsobj.Equal(patched[t.collProp], cur[t.collProp]) {
			return nil, &setError{Type: "invalidProperties", Properties: []string{t.collProp},
				Description: "moving a record to another collection is not supported; create it there and destroy this one"}
		}
		if u, _ := patched["uid"].(string); u != t.codec.uid(old) {
			return nil, &setError{Type: "invalidProperties", Properties: []string{"uid"}, Description: "uid cannot change"}
		}
		m, err := t.codec.decode(t.clientJSON(patched))
		if err != nil {
			return nil, err
		}
		t.codec.edited(old, m, now)
		_, err = c.req.dav.Put(g, old.URI, t.codec.render(m), davsql.Conditions{IfMatch: []int64{old.Version}}, opts.send)
		if errors.Is(err, dav.ErrPrecondition) && attempt < 2 {
			continue
		}
		if err != nil {
			return nil, err
		}
		stored, err := c.reread(t, g, old.URI)
		if err != nil {
			return nil, err
		}
		changed := jsobj.Object{}
		for k, v := range stored {
			if strings.HasPrefix(k, jsobj.Vendor) {
				continue
			}
			if p, ok := patched[k]; !ok || !jsobj.Equal(p, v) {
				changed[k] = v
			}
		}
		if len(changed) == 0 {
			return nil, nil
		}
		return changed, nil
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		na, ea := strconv.Atoi(a)
		nb, eb := strconv.Atoi(b)
		if ea == nil && eb == nil {
			return na - nb
		}
		return strings.Compare(a, b)
	})
	return keys
}

func (e *setError) Error() string { return e.Type + ": " + e.Description }
