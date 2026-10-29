package jmap

import (
	"errors"
	"maps"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/jscal"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
)

// collType is a JMAP data type whose records are DAV collections:
// AddressBook or Calendar.
type collType struct {
	name       string
	capability string
	home       dav.Home
	// comps are what a collection the type creates holds.
	comps []dav.Component
	colls func(a *account) []dav.Granted
	items *itemType
	// toJSON writes a collection; apply sets the properties a client may
	// set, from obj, on a collection being created or changed.
	toJSON func(a *account, g *dav.Granted) jsobj.Object
	apply  func(obj jsobj.Object, c *dav.Collection) *setError
	// removeArg is the /set argument that destroys a collection with
	// records in it.
	removeArg string
}

func (c *call) collectionsGet(t *collType, args jsobj.Object) (any, error) {
	var in getArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	colls := t.colls(a)
	list, notFound := []any{}, []string{}
	if in.IDs == nil {
		for i := range colls {
			list = append(list, filterProps(t.toJSON(a, &colls[i]), in.Properties))
		}
	} else {
		for _, raw := range *in.IDs {
			id, ok := parseCollectionID(c.req.resolveID(raw))
			i := slices.IndexFunc(colls, func(g dav.Granted) bool { return g.ID == id })
			if !ok || i < 0 {
				notFound = append(notFound, raw)
				continue
			}
			list = append(list, filterProps(t.toJSON(a, &colls[i]), in.Properties))
		}
	}
	return map[string]any{"accountId": a.id, "state": collectionsState(colls), "list": list, "notFound": notFound}, nil
}

func (c *call) collectionsChanges(t *collType, args jsobj.Object) (any, error) {
	var in changesArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	old, ok := parseCollectionsState(in.SinceState)
	if !ok {
		return nil, errorOf("cannotCalculateChanges", "unknown state")
	}
	colls := t.colls(a)
	cur := map[dav.CollectionID]string{}
	for _, g := range colls {
		e, _ := parseCollectionsState(collectionsState([]dav.Granted{g}))
		cur[g.ID] = e[0].version
	}
	was := map[dav.CollectionID]string{}
	for _, e := range old {
		was[e.id] = e.version
	}
	limit := -1
	if in.MaxChanges != nil {
		if *in.MaxChanges <= 0 {
			return nil, invalidArguments("maxChanges must be positive")
		}
		limit = *in.MaxChanges
	}
	created, updated, destroyed := []string{}, []string{}, []string{}
	// What the client will know once it has these changes: its old list
	// with each change made, so a limit can stop anywhere.
	next := map[dav.CollectionID]string{}
	maps.Copy(next, was)
	n := 0
	more := false
	add := func(list *[]string, id dav.CollectionID, version string, gone bool) bool {
		if limit >= 0 && n == limit {
			more = true
			return false
		}
		n++
		*list = append(*list, collectionID(id))
		if gone {
			delete(next, id)
		} else {
			next[id] = version
		}
		return true
	}
	for _, g := range colls {
		v, had := was[g.ID]
		switch {
		case !had:
			add(&created, g.ID, cur[g.ID], false)
		case v != cur[g.ID]:
			add(&updated, g.ID, cur[g.ID], false)
		}
	}
	for _, e := range old {
		if _, ok := cur[e.id]; !ok {
			add(&destroyed, e.id, "", true)
		}
	}
	newState := collectionsState(colls)
	if more {
		newState = rebuildState(next)
	}
	return map[string]any{
		"accountId": a.id, "oldState": in.SinceState, "newState": newState, "hasMoreChanges": more,
		"created": created, "updated": updated, "destroyed": destroyed, "updatedProperties": nil,
	}, nil
}

// rebuildState writes a collections state from what a client knows.
func rebuildState(m map[dav.CollectionID]string) string {
	ids := make([]dav.CollectionID, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var s strings.Builder
	s.WriteString("v1")
	for _, id := range ids {
		s.WriteString(";" + id.String() + "." + m[id])
	}
	return s.String()
}

func (c *call) collectionsSet(t *collType, args jsobj.Object) (any, error) {
	remove := false
	if v, ok := args[t.removeArg]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return nil, invalidArguments("%s is a boolean", t.removeArg)
		}
		remove = b
		args = jsobj.Clone(args).(jsobj.Object)
		delete(args, t.removeArg)
	}
	// onSuccessSetIsDefault: the default is the user's first collection,
	// which a client cannot change.
	if _, ok := args["onSuccessSetIsDefault"]; ok {
		args = jsobj.Clone(args).(jsobj.Object)
		delete(args, "onSuccessSetIsDefault")
	}
	var in setArgs
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	a, err := c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	oldState := collectionsState(t.colls(a))
	if in.IfInState != nil && *in.IfInState != oldState {
		return nil, errorOf("stateMismatch", "the state is %s", oldState)
	}
	created, notCreated := map[string]any{}, map[string]any{}
	updated, notUpdated := map[string]any{}, map[string]any{}
	destroyed, notDestroyed := []string{}, map[string]any{}
	find := func(raw string) *dav.Granted {
		id, ok := parseCollectionID(c.req.resolveID(raw))
		if !ok {
			return nil
		}
		for _, g := range t.colls(a) {
			if g.ID == id {
				return &g
			}
		}
		// Made earlier in this request.
		if g, err := c.req.dav.Collection(id); err == nil && g.OwnerID == a.owner.ID && g.Home == t.home {
			return g
		}
		return nil
	}

	for _, cid := range sortedKeys(in.Create) {
		obj := in.Create[cid]
		if !a.own {
			notCreated[cid] = &setError{Type: "forbidden", Description: "collections are created in your own account"}
			continue
		}
		if _, ok := obj["id"]; ok {
			notCreated[cid] = &setError{Type: "invalidProperties", Properties: []string{"id"}}
			continue
		}
		col := &dav.Collection{Home: t.home, Components: t.comps}
		if se := t.apply(obj, col); se != nil {
			notCreated[cid] = se
			continue
		}
		if err := c.req.dav.CreateCollection(col); err != nil {
			se, err := setErrorOf(err)
			if err != nil {
				return nil, err
			}
			notCreated[cid] = se
			continue
		}
		id := collectionID(col.ID)
		c.req.created[cid] = id
		g, err := c.req.dav.Collection(col.ID)
		if err != nil {
			return nil, err
		}
		result := jsobj.Object{"id": id}
		for k, v := range t.toJSON(a, g) {
			if _, sent := obj[k]; !sent {
				result[k] = v
			}
		}
		created[cid] = result
	}

	for _, raw := range sortedKeys(in.Update) {
		patch := in.Update[raw]
		g := find(raw)
		if g == nil {
			notUpdated[raw] = &setError{Type: "notFound"}
			continue
		}
		if !g.Rights.Owner {
			notUpdated[raw] = &setError{Type: "forbidden", Description: "only the owner changes a collection"}
			continue
		}
		cur := t.toJSON(a, g)
		patched := jsobj.Clone(cur).(jsobj.Object)
		if err := applyPatchObject(patched, patch); err != nil {
			notUpdated[raw] = &setError{Type: "invalidPatch", Description: err.Error()}
			continue
		}
		col := g.Collection
		if se := t.apply(changedOnly(cur, patched), &col); se != nil {
			notUpdated[raw] = se
			continue
		}
		if err := c.req.dav.UpdateCollection(g, &col); err != nil {
			return nil, err
		}
		updated[collectionID(g.ID)] = nil
	}

	for _, raw := range in.Destroy {
		g := find(raw)
		if g == nil {
			notDestroyed[raw] = &setError{Type: "notFound"}
			continue
		}
		if !g.Rights.Owner {
			notDestroyed[raw] = &setError{Type: "forbidden", Description: "only the owner deletes a collection"}
			continue
		}
		if !remove {
			has, err := c.hasItems(t.items, g)
			if err != nil {
				return nil, err
			}
			if has {
				typ := "addressBookHasContents"
				if t.home == dav.HomeCalendars {
					typ = "calendarHasEvent"
				}
				notDestroyed[raw] = &setError{Type: typ}
				continue
			}
		}
		if err := c.req.dav.DeleteCollection(g); err != nil {
			if errors.Is(err, dav.ErrNotFound) {
				notDestroyed[raw] = &setError{Type: "notFound"}
				continue
			}
			return nil, err
		}
		destroyed = append(destroyed, collectionID(g.ID))
	}

	// The account's collections changed: read them again.
	c.req.accounts = nil
	a, err = c.account(in.AccountID, t.capability)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"accountId": a.id, "oldState": oldState, "newState": collectionsState(t.colls(a))}
	for k, m := range map[string]map[string]any{"created": created, "notCreated": notCreated, "updated": updated,
		"notUpdated": notUpdated, "notDestroyed": notDestroyed} {
		if len(m) > 0 {
			out[k] = m
		} else {
			out[k] = nil
		}
	}
	if len(destroyed) > 0 {
		out["destroyed"] = destroyed
	} else {
		out["destroyed"] = nil
	}
	return out, nil
}

// changedOnly returns the properties of next that differ from cur, so a
// patch applies only what it changes.
func changedOnly(cur, next jsobj.Object) jsobj.Object {
	out := jsobj.Object{}
	for k, v := range next {
		if !jsobj.Equal(cur[k], v) {
			out[k] = v
		}
	}
	for k := range cur {
		if _, ok := next[k]; !ok {
			out[k] = nil
		}
	}
	return out
}

func (c *call) hasItems(t *itemType, g *dav.Granted) (bool, error) {
	if t == nil {
		return false, nil
	}
	ch, err := c.req.dav.Changes(g, t.comp, 0)
	if errors.Is(err, dav.ErrNoBackend) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, r := range ch.Changed {
		if r.Component == t.comp {
			return true, nil
		}
	}
	return false, nil
}

// applyPatchObject applies a JMAP PatchObject (RFC 8620 section 5.3), the
// same form as JSCalendar's.
func applyPatchObject(o, patch jsobj.Object) error {
	return jscal.ApplyPatch(o, patch)
}
