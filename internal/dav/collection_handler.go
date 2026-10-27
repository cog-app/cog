package dav

import (
	"encoding/xml"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// TimezoneObserver is implemented by backends that derive data from a
// collection's timezone (floating times). The core calls it after the
// timezone changes.
type TimezoneObserver interface {
	TimezoneChanged(owner string, c *Collection) error
}

func (q *request) collection(home Home, owner *User, name string) {
	c, err := q.s.Collections.GetOrDefault(owner.ID, home, name)
	if errors.Is(err, ErrNotFound) {
		switch q.r.Method {
		case "MKCALENDAR", "MKCOL":
			q.create(home, owner, name)
		case http.MethodOptions:
			q.options(allowHome)
		default:
			http.NotFound(q.w, q.r)
		}
		return
	}
	if err != nil {
		q.fail(err)
		return
	}
	a, err := q.access(c)
	if err != nil {
		q.fail(err)
		return
	}
	if !a.freeBusy() {
		http.Error(q.w, "Forbidden", http.StatusForbidden)
		return
	}
	switch q.r.Method {
	case "PROPFIND":
		q.propfindCollection(c, owner, a)
	case "REPORT":
		q.report(c, owner, a)
	case "PROPPATCH":
		if !a.owner {
			writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
			return
		}
		if done, err := q.suppressPush(pushKey{collection: c.ID}); err != nil {
			q.fail(err)
			return
		} else if done != nil {
			defer done()
		}
		q.proppatch(c, owner)
	case http.MethodDelete:
		if !a.owner {
			writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
			return
		}
		q.deleteCollection(c, owner)
	case http.MethodPost:
		n, err := readXML(q.r)
		switch {
		case err == nil && n.is(pushName("push-register")):
			if !a.read() {
				writeError(q.w, http.StatusForbidden, pushName("push-not-available"), "")
				return
			}
			q.pushRegister(n, collectionResource(c.ID), true)
		case err == nil && n.is(cs("share")):
			if !a.owner {
				writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
				return
			}
			q.sharePost(c, owner, n)
		default:
			http.Error(q.w, "expected CS:share or push-register", http.StatusBadRequest)
		}
	case "MKCALENDAR", "MKCOL":
		writeError(q.w, http.StatusMethodNotAllowed, dav("resource-must-be-null"), "")
	case http.MethodOptions:
		q.options(allowCollection)
	default:
		q.methodNotAllowed(allowCollection)
	}
}

func (q *request) propfindCollection(c *Collection, owner *User, a access) {
	pf, err := q.propfindBody()
	if err != nil {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	tok, err := q.token(c, owner.Username)
	if err != nil {
		q.fail(err)
		return
	}
	props, extra, err := q.collectionProps(c, a, owner.Username, tok)
	if err != nil {
		q.fail(err)
		return
	}
	ms := multistatus{Responses: []response{{
		Href: collectionHref(c, owner.Username), Propstats: pf.evaluate(props, extra),
	}}}
	if q.depth() == 1 {
		withData := pf.wants(dataName(c.Home))
		for _, b := range q.backends(c) {
			list, err := b.List(owner.Username, c, withData)
			if err != nil {
				q.fail(err)
				return
			}
			for i := range list {
				if r, ok := q.resourceResponse(c, owner, a, b, &list[i], pf); ok {
					ms.Responses = append(ms.Responses, r)
				}
			}
		}
	}
	ms.write(q.w)
}

// create handles MKCALENDAR (RFC 4791 §5.3.1) and MKCOL, plain or extended
// (RFC 5689), in the owner's home.
func (q *request) create(home Home, owner *User, name string) {
	if owner.ID != q.user.ID {
		writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
		return
	}
	c := &Collection{OwnerID: owner.ID, Home: home, Name: name}
	n, err := readXML(q.r)
	if err != nil && !errors.Is(err, errEmptyBody) {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	var props []*node
	if n != nil {
		want := dav("mkcol")
		if q.r.Method == "MKCALENDAR" {
			want = caldav("mkcalendar")
		}
		if !n.is(want) {
			http.Error(q.w, "expected "+want.Local, http.StatusBadRequest)
			return
		}
		for _, set := range n.children() {
			if set.is(dav("set")) {
				props = append(props, set.child(dav("prop")).children()...)
			}
		}
	}

	// The resource type decides what an extended MKCOL creates; MKCALENDAR
	// always makes a calendar; a plain MKCOL makes an address book, as
	// CardDAV clients that predate RFC 5689 expect.
	kind := home
	if q.r.Method == "MKCOL" {
		if i := slices.IndexFunc(props, func(p *node) bool { return p.is(dav("resourcetype")) }); i >= 0 {
			switch rt := props[i]; {
			case rt.child(caldav("calendar")) != nil:
				kind = HomeCalendars
			case rt.child(carddav("addressbook")) != nil:
				kind = HomeAddressBooks
			default:
				kind = ""
			}
		} else if home == HomeCalendars {
			kind = ""
		}
	}
	if kind != home {
		writeError(q.w, http.StatusForbidden, dav("valid-resourcetype"), "")
		return
	}

	if home == HomeAddressBooks {
		c.Components = []Component{VCARD}
	} else {
		c.Components = []Component{VEVENT, VTODO}
		if i := slices.IndexFunc(props, func(p *node) bool { return p.is(caldav("supported-calendar-component-set")) }); i >= 0 {
			c.Components = nil
			for _, comp := range props[i].children() {
				cc, ok := ParseComponent(comp.attr("name"))
				if !ok || cc == VCARD || q.backendFor(cc) == nil {
					writeError(q.w, http.StatusForbidden, caldav("supported-calendar-component"), "")
					return
				}
				if !slices.Contains(c.Components, cc) {
					c.Components = append(c.Components, cc)
				}
			}
			if len(c.Components) == 0 {
				writeError(q.w, http.StatusForbidden, caldav("supported-calendar-component"), "")
				return
			}
		}
	}
	for _, p := range props {
		if p.is(dav("resourcetype")) || p.is(caldav("supported-calendar-component-set")) {
			continue
		}
		// Protected or invalid properties are ignored at creation rather
		// than failing it: the collection is still what the client asked for.
		if status, _ := applyProp(c, p, true); status != http.StatusOK {
			q.s.Log.Warn("dav: property not set on new collection", "name", p.Name.Local, "status", status)
		}
	}
	if err := q.s.Collections.Create(c); err != nil {
		if errors.Is(err, ErrExists) {
			writeError(q.w, http.StatusMethodNotAllowed, dav("resource-must-be-null"), "")
			return
		}
		q.fail(err)
		return
	}
	q.w.Header().Set("Location", collectionHref(c, owner.Username))
	q.w.WriteHeader(http.StatusCreated)
}

// protected are live properties a client cannot set.
var protected = []xml.Name{
	dav("resourcetype"), dav("getetag"), dav("sync-token"), dav("owner"), dav("current-user-principal"),
	dav("current-user-privilege-set"), dav("supported-report-set"), dav("getcontenttype"), dav("getcontentlength"),
	cs("getctag"), caldav("supported-calendar-component-set"), caldav("supported-calendar-data"),
	caldav("max-resource-size"), carddav("supported-address-data"), carddav("max-resource-size"),
}

// applyProp sets (or removes) one property on c and returns the status for
// the PROPPATCH response, and the precondition that failed, if any.
func applyProp(c *Collection, p *node, set bool) (int, xml.Name) {
	if slices.Contains(protected, p.Name) {
		return http.StatusForbidden, dav("cannot-modify-protected-property")
	}
	val := ""
	if set {
		val = p.text()
	}
	switch {
	case p.is(dav("displayname")):
		c.DisplayName = val
	case p.is(apple("calendar-color")):
		c.Color = val
	case p.is(caldav("calendar-description")) && c.Home == HomeCalendars,
		p.is(carddav("addressbook-description")) && c.Home == HomeAddressBooks:
		c.Description = val
	case p.is(apple("calendar-order")) && c.Home == HomeCalendars:
		if val == "" {
			c.SortOrder = 0
			break
		}
		n, err := strconv.Atoi(val)
		if err != nil {
			return http.StatusConflict, xml.Name{}
		}
		c.SortOrder = n
	case p.is(caldav("calendar-timezone-id")) && c.Home == HomeCalendars:
		if val == "" {
			c.Timezone = ""
			break
		}
		loc := tz.Resolve(val, nil)
		if loc == nil {
			return http.StatusForbidden, caldav("valid-timezone")
		}
		c.Timezone = loc.String()
	case p.is(caldav("calendar-timezone")) && c.Home == HomeCalendars:
		if val == "" {
			c.Timezone = ""
			break
		}
		comps, err := contentline.Parse(strings.NewReader(val))
		if err != nil {
			return http.StatusForbidden, caldav("valid-calendar-data")
		}
		loc := tz.FromVTimezone(comps)
		if loc == nil {
			// A zone Cog cannot resolve is not an error: keep the old zone
			// until the VTIMEZONE rule evaluator lands (section 4.1).
			return http.StatusOK, xml.Name{}
		}
		c.Timezone = loc.String()
	default:
		i := slices.IndexFunc(c.DeadProps, func(d DeadProp) bool { return d.Space == p.Name.Space && d.Local == p.Name.Local })
		if !set {
			if i >= 0 {
				c.DeadProps = slices.Delete(c.DeadProps, i, i+1)
			}
			return http.StatusOK, xml.Name{}
		}
		inner, err := p.innerXML()
		if err != nil {
			return http.StatusConflict, xml.Name{}
		}
		d := DeadProp{Space: p.Name.Space, Local: p.Name.Local, Inner: inner}
		if i >= 0 {
			c.DeadProps[i] = d
		} else {
			c.DeadProps = append(c.DeadProps, d)
		}
	}
	return http.StatusOK, xml.Name{}
}

// proppatch applies a PROPPATCH atomically (RFC 4918 §9.2): if any property
// fails, none is applied and the rest report 424.
func (q *request) proppatch(c *Collection, owner *User) {
	n, err := readXML(q.r)
	if err != nil || !n.is(dav("propertyupdate")) {
		http.Error(q.w, "expected DAV:propertyupdate", http.StatusBadRequest)
		return
	}
	updated := *c
	updated.DeadProps = slices.Clone(c.DeadProps)
	type result struct {
		name   xml.Name
		status int
		cond   xml.Name
	}
	var results []result
	failed := false
	for _, op := range n.children() {
		set := op.is(dav("set"))
		if !set && !op.is(dav("remove")) {
			continue
		}
		for _, p := range op.child(dav("prop")).children() {
			status, cond := applyProp(&updated, p, set)
			results = append(results, result{p.Name, status, cond})
			failed = failed || status != http.StatusOK
		}
	}
	if !failed {
		if err := q.s.Collections.Update(&updated); err != nil {
			q.fail(err)
			return
		}
		if updated.Timezone != c.Timezone {
			for _, b := range q.backends(&updated) {
				if o, ok := b.(TimezoneObserver); ok {
					if err := o.TimezoneChanged(owner.Username, &updated); err != nil {
						q.fail(err)
						return
					}
				}
			}
		}
	}
	byStatus := map[int]*propstat{}
	var order []int
	for _, r := range results {
		status := r.status
		if failed && status == http.StatusOK {
			status = http.StatusFailedDependency
		}
		ps, ok := byStatus[status]
		if !ok {
			ps = &propstat{Status: status}
			byStatus[status] = ps
			order = append(order, status)
		}
		ps.Props = append(ps.Props, prop{Name: r.name})
		if r.cond.Local != "" {
			ps.Error = el(r.cond, "")
		}
	}
	resp := response{Href: collectionHref(c, owner.Username)}
	for _, s := range order {
		resp.Propstats = append(resp.Propstats, *byStatus[s])
	}
	if len(resp.Propstats) == 0 {
		resp.Propstats = []propstat{{Status: http.StatusOK}}
	}
	ms := multistatus{Responses: []response{resp}}
	ms.write(q.w)
}

// deleteCollection removes the collection, then each backend's rows. The row
// goes first: clients discover collections through it, so they never see a
// half-deleted one. A backend that fails leaves orphans keyed by an ID that
// will never be reused; they are invisible.
func (q *request) deleteCollection(c *Collection, owner *User) {
	if err := q.s.DeleteCollection(owner.Username, c); err != nil {
		q.fail(err)
		return
	}
	q.w.WriteHeader(http.StatusNoContent)
}

// DeleteCollection removes a collection and asks every enabled backend to
// drop its rows. Backend failures are logged, not returned: the collection is
// already gone.
func (s *Server) DeleteCollection(ownerUsername string, c *Collection) error {
	if err := s.Collections.Delete(c.ID); err != nil {
		return err
	}
	for _, b := range s.Backends() {
		if !slices.ContainsFunc(b.Components(), c.Has) {
			continue
		}
		if err := b.Purge(ownerUsername, c.ID); err != nil {
			s.Log.Error("dav: purge deleted collection", "backend", b.Key(), "collection", c.ID, "err", err)
		}
	}
	return nil
}
