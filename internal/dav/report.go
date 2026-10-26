package dav

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

func (q *request) report(c *Collection, owner *User, a access) {
	n, err := readXML(q.r)
	if err != nil {
		http.Error(q.w, "invalid REPORT body", http.StatusBadRequest)
		return
	}
	switch {
	case n.is(caldav("calendar-multiget")) && c.Home == HomeCalendars,
		n.is(carddav("addressbook-multiget")) && c.Home == HomeAddressBooks:
		q.multiget(c, owner, a, n)
	case n.is(caldav("calendar-query")) && c.Home == HomeCalendars:
		q.calendarQuery(c, owner, a, n)
	case n.is(carddav("addressbook-query")) && c.Home == HomeAddressBooks:
		q.all(c, owner, a, propsFrom(n))
	case n.is(dav("sync-collection")):
		q.syncCollection(c, owner, a, n)
	default:
		writeError(q.w, http.StatusForbidden, dav("supported-report"), "")
	}
}

// uriFromHref returns the resource name an href names inside collection c,
// or "" when it is outside it.
func uriFromHref(href string, c *Collection, ownerName string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return ""
	}
	base, err := url.PathUnescape(collectionHref(c, ownerName))
	if err != nil {
		return ""
	}
	uri, ok := strings.CutPrefix(u.Path, base)
	if !ok || uri == "" || strings.Contains(uri, "/") {
		return ""
	}
	return uri
}

func (q *request) multiget(c *Collection, owner *User, a access, n *node) {
	pf := propsFrom(n)
	withData := pf.wants(dataName(c.Home))
	var hrefs, uris []string
	for _, h := range n.children() {
		if h.is(dav("href")) {
			href := h.text()
			hrefs = append(hrefs, href)
			uris = append(uris, uriFromHref(href, c, owner.Username))
		}
	}
	type found struct {
		b Backend
		r *Resource
	}
	byURI := map[string]found{}
	want := slices.DeleteFunc(slices.Clone(uris), func(s string) bool { return s == "" })
	for _, b := range q.backends(c) {
		if len(want) == 0 {
			break
		}
		list, err := b.Get(owner.Username, c, want, withData)
		if err != nil {
			q.fail(err)
			return
		}
		for i := range list {
			byURI[list[i].URI] = found{b, &list[i]}
		}
	}
	var ms multistatus
	for i, href := range hrefs {
		f, ok := byURI[uris[i]]
		if !ok {
			ms.Responses = append(ms.Responses, response{Href: href, Status: http.StatusNotFound})
			continue
		}
		resp, ok := q.resourceResponse(c, owner, a, f.b, f.r, pf)
		if !ok {
			ms.Responses = append(ms.Responses, response{Href: href, Status: http.StatusForbidden})
			continue
		}
		resp.Href = href
		ms.Responses = append(ms.Responses, resp)
	}
	ms.write(q.w)
}

// all answers a query with every resource; used for addressbook-query, whose
// filters Cog does not evaluate (clients filter the superset).
func (q *request) all(c *Collection, owner *User, a access, pf *propfind) {
	withData := pf.wants(dataName(c.Home))
	var ms multistatus
	for _, b := range q.backends(c) {
		list, err := b.List(owner.Username, c, withData)
		if err != nil {
			q.fail(err)
			return
		}
		for i := range list {
			if resp, ok := q.resourceResponse(c, owner, a, b, &list[i], pf); ok {
				ms.Responses = append(ms.Responses, resp)
			}
		}
	}
	ms.write(q.w)
}

// parseUTC parses a CalDAV time-range bound (RFC 4791 §9.9).
func parseUTC(s string) (time.Time, bool) {
	t, err := time.Parse("20060102T150405Z", s)
	return t, err == nil
}

// calendarQuery evaluates the component filter and its time range; other
// filters return a superset, which RFC 4791 clients tolerate.
func (q *request) calendarQuery(c *Collection, owner *User, a access, n *node) {
	pf := propsFrom(n)
	withData := pf.wants(dataName(c.Home))
	var comp Component
	var start, end time.Time
	if cal := n.child(caldav("filter")).child(caldav("comp-filter")); cal != nil {
		if inner := cal.child(caldav("comp-filter")); inner != nil {
			cc, ok := ParseComponent(inner.attr("name"))
			if !ok || !c.Has(cc) {
				(&multistatus{}).write(q.w)
				return
			}
			comp = cc
			if tr := inner.child(caldav("time-range")); tr != nil {
				start, _ = parseUTC(tr.attr("start"))
				end, _ = parseUTC(tr.attr("end"))
			}
		}
	}
	var ms multistatus
	for _, b := range q.backends(c) {
		if comp != "" && !slices.Contains(b.Components(), comp) {
			continue
		}
		var list []Resource
		var err error
		if tq, ok := b.(TimeRangeQuerier); ok && comp != "" && (!start.IsZero() || !end.IsZero()) {
			list, err = tq.InRange(owner.Username, c, comp, start, end, withData)
		} else {
			list, err = b.List(owner.Username, c, withData)
		}
		if err != nil {
			q.fail(err)
			return
		}
		for i := range list {
			if comp != "" && list[i].Component != comp {
				continue
			}
			if resp, ok := q.resourceResponse(c, owner, a, b, &list[i], pf); ok {
				ms.Responses = append(ms.Responses, resp)
			}
		}
	}
	ms.write(q.w)
}

// syncCollection answers RFC 6578 sync-collection with the composite token.
func (q *request) syncCollection(c *Collection, owner *User, a access, n *node) {
	tok, err := ParseSyncToken(n.child(dav("sync-token")).text())
	if err != nil {
		writeError(q.w, http.StatusForbidden, dav("valid-sync-token"), "")
		return
	}
	pf := propsFrom(n)
	withData := pf.wants(dataName(c.Home))
	cutoff := q.s.Clock().Add(-q.s.retention())
	next := SyncToken{}
	var ms multistatus
	changed := map[string]bool{}
	var deleted []string
	for _, b := range q.backends(c) {
		since := tok[b.Key()]
		if err := b.Prune(owner.Username, c, cutoff); err != nil {
			q.fail(err)
			return
		}
		ch, err := b.Changes(owner.Username, c, since, withData)
		if err != nil {
			q.fail(err)
			return
		}
		if since > ch.State.Token || (since > 0 && since < ch.State.Floor) {
			writeError(q.w, http.StatusForbidden, dav("valid-sync-token"), "")
			return
		}
		next[b.Key()] = ch.State.Token
		for i := range ch.Changed {
			changed[ch.Changed[i].URI] = true
			if resp, ok := q.resourceResponse(c, owner, a, b, &ch.Changed[i], pf); ok {
				ms.Responses = append(ms.Responses, resp)
			}
		}
		deleted = append(deleted, ch.Deleted...)
	}
	for _, uri := range deleted {
		// A URI deleted by one backend and recreated by another is a change.
		if !changed[uri] {
			changed[uri] = true
			ms.Responses = append(ms.Responses, response{Href: resourceHref(c, owner.Username, uri), Status: http.StatusNotFound})
		}
	}
	ms.SyncToken = next.String()
	ms.write(q.w)
}
