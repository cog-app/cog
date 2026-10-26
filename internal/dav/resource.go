package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// maxResourceSize bounds one calendar object or vCard.
const maxResourceSize = 1 << 20

func dataName(h Home) xml.Name {
	if h == HomeAddressBooks {
		return carddav("address-data")
	}
	return caldav("calendar-data")
}

func contentType(c Component) string {
	if c == VCARD {
		return "text/vcard; charset=utf-8"
	}
	return "text/calendar; charset=utf-8; component=" + strings.ToLower(string(c))
}

func (q *request) resource(home Home, owner *User, name, uri string) {
	c, err := q.s.Collections.GetOrDefault(owner.ID, home, name)
	if errors.Is(err, ErrNotFound) {
		if q.r.Method == http.MethodPut {
			http.Error(q.w, "Conflict: no such collection", http.StatusConflict)
			return
		}
		http.NotFound(q.w, q.r)
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
	case http.MethodGet, http.MethodHead:
		q.get(c, owner, a, uri)
	case http.MethodPut:
		if !a.write() {
			writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
			return
		}
		q.put(c, owner, a, uri)
	case http.MethodDelete:
		if !a.write() {
			writeError(q.w, http.StatusForbidden, dav("need-privileges"), "")
			return
		}
		q.delete(c, owner, a, uri)
	case "PROPFIND":
		q.propfindResource(c, owner, a, uri)
	case http.MethodOptions:
		q.options(allowResource)
	default:
		q.methodNotAllowed(allowResource)
	}
}

// find returns the resource at uri and the backend holding it, or ErrNotFound.
func (q *request) find(c *Collection, owner string, uri string, withData bool) (Backend, *Resource, error) {
	for _, b := range q.backends(c) {
		list, err := b.Get(owner, c, []string{uri}, withData)
		if err != nil {
			return nil, nil, err
		}
		if len(list) > 0 {
			return b, &list[0], nil
		}
	}
	return nil, nil, ErrNotFound
}

// visible prepares a resource for a user with access a: a free/busy-only
// sharee sees a redacted copy, or nothing when the backend cannot redact.
func visible(c *Collection, a access, b Backend, r *Resource) (bool, error) {
	if !a.busyOnly() {
		return true, nil
	}
	red, ok := b.(BusyRedactor)
	if !ok {
		return false, nil
	}
	if r.Data == nil {
		return true, nil
	}
	return true, red.Redact(c, r)
}

// byReference reports whether the client asked for time zones by reference
// (RFC 7809): calendar data without the VTIMEZONEs of standard zones, which
// it knows already.
func (q *request) byReference() bool {
	return strings.EqualFold(strings.TrimSpace(q.r.Header.Get("CalDAV-Timezones")), "F")
}

// served prepares a resource's data for this request: redacted for a
// free/busy-only sharee, and without standard VTIMEZONEs when the client
// asked for time zones by reference. It reports false when the user may not
// see the resource.
func (q *request) served(c *Collection, a access, b Backend, r *Resource) (bool, error) {
	ok, err := visible(c, a, b, r)
	if err != nil || !ok || r.Data == nil || r.Component == VCARD || !q.byReference() {
		return ok, err
	}
	r.Data, err = withoutStandardZones(r.Data)
	return true, err
}

// withoutStandardZones removes the VTIMEZONEs of zones a client can look up
// by name; a client's own definitions stay, since nothing else defines them.
func withoutStandardZones(data []byte) ([]byte, error) {
	comps, err := contentline.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for _, cal := range comps {
		cal.Components = slices.DeleteFunc(cal.Components, func(sub *contentline.Component) bool {
			id := sub.Prop("TZID")
			return sub.Is("VTIMEZONE") && id != nil && tz.Load(id.Value) != nil
		})
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, comps); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (q *request) get(c *Collection, owner *User, a access, uri string) {
	b, r, err := q.find(c, owner.Username, uri, true)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(q.w, q.r)
		return
	}
	if err != nil {
		q.fail(err)
		return
	}
	ok, err := q.served(c, a, b, r)
	if err != nil {
		q.fail(err)
		return
	}
	if !ok {
		http.Error(q.w, "Forbidden", http.StatusForbidden)
		return
	}
	etag := r.ETag(c.ID)
	q.w.Header().Set("ETag", etag)
	q.w.Header().Set("Vary", "CalDAV-Timezones")
	q.w.Header().Set("Content-Type", contentType(r.Component))
	if inm := q.r.Header.Get("If-None-Match"); inm != "" {
		for t := range strings.SplitSeq(inm, ",") {
			if t = strings.TrimSpace(t); t == "*" || t == etag {
				q.w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	q.w.Header().Set("Content-Length", strconv.Itoa(len(r.Data)))
	q.w.WriteHeader(http.StatusOK)
	if q.r.Method != http.MethodHead {
		q.w.Write(r.Data)
	}
}

// conditions reduces If-Match and If-None-Match to versions in collection c.
func conditions(r *http.Request, c CollectionID) davsql.Conditions {
	var cond davsql.Conditions
	if im := strings.TrimSpace(r.Header.Get("If-Match")); im != "" {
		if im == "*" {
			cond.IfMatchAny = true
		} else {
			for t := range strings.SplitSeq(im, ",") {
				if v, ok := parseETag(t, c); ok {
					cond.IfMatch = append(cond.IfMatch, v)
				}
			}
			cond.IfMatchNone = len(cond.IfMatch) == 0
		}
	}
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == "*" {
		cond.IfNoneMatchAny = true
	}
	return cond
}

// errResourceKind reports a PUT that would change a resource's component
// type, which would move it to another backend.
var errResourceKind = errors.New("dav: a resource of another component type exists at this URI")

func (q *request) put(c *Collection, owner *User, a access, uri string) {
	data, err := io.ReadAll(io.LimitReader(q.r.Body, maxResourceSize+1))
	if err != nil {
		http.Error(q.w, "read body", http.StatusBadRequest)
		return
	}
	ns := caldav
	if c.Home == HomeAddressBooks {
		ns = carddav
	}
	if len(data) > maxResourceSize {
		writeError(q.w, http.StatusForbidden, ns("max-resource-size"), "")
		return
	}
	obj, comp, uid, cond := parseObject(c.Home, data)
	if cond.Local != "" {
		writeError(q.w, http.StatusForbidden, cond, "")
		return
	}
	if q.backendFor(comp) == nil || !c.Has(comp) {
		writeError(q.w, http.StatusForbidden, caldav("supported-calendar-component"), "")
		return
	}
	req := &PutRequest{URI: uri, UID: uid, Component: comp, Data: data, Object: obj, Conditions: conditions(q.r, c.ID)}
	var res *PutResult
	var delivery *Delivery
	if comp == VEVENT {
		res, delivery, err = q.putScheduled(c, owner, a, req)
	} else {
		res, err = q.s.put(q.backends(c), owner.Username, c, req)
	}
	if conflict, ok := IsUIDConflict(err); ok {
		writeError(q.w, http.StatusForbidden, ns("no-uid-conflict"), hrefEl(resourceHref(c, owner.Username, conflict.URI)))
		return
	}
	switch {
	case errors.Is(err, ErrPrecondition):
		http.Error(q.w, "Precondition Failed", http.StatusPreconditionFailed)
		return
	case errors.Is(err, errResourceKind):
		http.Error(q.w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		q.fail(err)
		return
	}
	// Return an ETag only for the bytes that will be served (RFC 4791
	// §5.3.4); otherwise the client fetches the stored version. The request
	// is compared with its folding and line endings normalised, which a
	// client cannot rely on anyway.
	if bytes.Equal(res.Data, data) || bytes.Equal(res.Data, normalise(obj)) {
		q.w.Header().Set("ETag", ETag(c.ID, res.Version))
	}
	delivery.Send()
	if res.Created {
		q.w.WriteHeader(http.StatusCreated)
	} else {
		q.w.WriteHeader(http.StatusNoContent)
	}
}

// unconditional reports whether a request named no ETag to match.
func unconditional(c davsql.Conditions) bool {
	return len(c.IfMatch) == 0 && !c.IfMatchAny && !c.IfMatchNone && !c.IfNoneMatchAny
}

// putScheduled stores an event and works out what the change sends
// (Server.Plan), from the stored copy it replaces. When the client named no
// ETag, the write is made conditional on that copy, and retried if another
// write came between, so the messages always describe the change made.
func (q *request) putScheduled(c *Collection, owner *User, a access, req *PutRequest) (*PutResult, *Delivery, error) {
	o, err := ical.Decode(req.Object, ical.VEVENT)
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		cond := req.Conditions
		var old *ical.Object
		_, r, err := q.find(c, owner.Username, req.URI, true)
		switch {
		case errors.Is(err, ErrNotFound):
			if unconditional(cond) {
				cond = davsql.Conditions{IfNoneMatchAny: true}
			}
		case err != nil:
			return nil, nil, err
		case r.Component == VEVENT:
			if old, err = decodeEvent(r.Data); err != nil {
				return nil, nil, fmt.Errorf("read %s: %w", r.URI, err)
			}
			if unconditional(cond) {
				cond = davsql.Conditions{IfMatch: []int64{r.Version}}
			}
		}
		cur := o.Clone()
		_, before, err := encodeObject(cur)
		if err != nil {
			return nil, nil, err
		}
		d, err := q.s.Plan(ScheduleChange{Owner: owner, Old: old, New: cur, MaySend: a.schedule()})
		if err != nil {
			return nil, nil, err
		}
		rq := *req
		rq.Conditions = cond
		obj, data, err := encodeObject(cur)
		if err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(before, data) {
			rq.Object, rq.Data = obj, data
		}
		res, err := q.s.put(q.backends(c), owner.Username, c, &rq)
		if errors.Is(err, ErrPrecondition) && unconditional(req.Conditions) && attempt < 2 {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		return res, d, nil
	}
}

// normalise writes a parsed request back out, with Cog's folding and line
// endings.
func normalise(obj *contentline.Component) []byte {
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{obj}); err != nil {
		return nil
	}
	return b.Bytes()
}

// put stores a resource through the backend for its component, after
// checking that no other backend of the collection holds its URI or UID.
func (s *Server) put(bes []Backend, owner string, c *Collection, req *PutRequest) (*PutResult, error) {
	var target Backend
	for _, b := range bes {
		for _, comp := range b.Components() {
			if comp == req.Component {
				target = b
			}
		}
	}
	if target == nil {
		return nil, fmt.Errorf("no backend stores %s", req.Component)
	}
	for _, b := range bes {
		if b == target {
			continue
		}
		existing, err := b.Get(owner, c, []string{req.URI}, false)
		if err != nil {
			return nil, err
		}
		if len(existing) > 0 {
			return nil, errResourceKind
		}
		other, err := b.FindUID(owner, c, req.UID)
		if err != nil {
			return nil, err
		}
		if other != "" {
			return nil, &UIDConflict{URI: other}
		}
	}
	return target.Put(owner, c, req)
}

// parseObject validates a PUT body and returns it parsed, with its component
// type and UID. On failure it returns the precondition that failed.
func parseObject(home Home, data []byte) (*contentline.Component, Component, string, xml.Name) {
	comps, err := contentline.Parse(bytes.NewReader(data))
	if home == HomeAddressBooks {
		bad := carddav("valid-address-data")
		if err != nil || len(comps) != 1 || !comps[0].Is("VCARD") {
			return nil, "", "", bad
		}
		uid := comps[0].Prop("UID")
		if uid == nil || uid.Value == "" {
			return nil, "", "", bad
		}
		return comps[0], VCARD, uid.Value, xml.Name{}
	}
	bad := caldav("valid-calendar-data")
	if err != nil || len(comps) != 1 || !comps[0].Is("VCALENDAR") {
		return nil, "", "", bad
	}
	cal := comps[0]
	var comp Component
	var uid string
	for _, sub := range cal.Components {
		if sub.Is("VTIMEZONE") {
			continue
		}
		cc, ok := ParseComponent(sub.Name)
		if !ok || cc == VCARD {
			return nil, "", "", caldav("supported-calendar-component")
		}
		u := sub.Prop("UID")
		if u == nil || u.Value == "" {
			return nil, "", "", caldav("valid-calendar-object-resource")
		}
		if comp == "" {
			comp, uid = cc, u.Value
		} else if cc != comp || u.Value != uid {
			// One resource holds one component type and one UID (RFC 4791 §4.1).
			return nil, "", "", caldav("valid-calendar-object-resource")
		}
	}
	if comp == "" {
		return nil, "", "", caldav("valid-calendar-object-resource")
	}
	return cal, comp, uid, xml.Name{}
}

func (q *request) delete(c *Collection, owner *User, a access, uri string) {
	d, err := q.deleteScheduled(c, owner, a, uri)
	switch {
	case errors.Is(err, ErrNotFound):
		http.NotFound(q.w, q.r)
	case errors.Is(err, ErrPrecondition):
		http.Error(q.w, "Precondition Failed", http.StatusPreconditionFailed)
	case err != nil:
		q.fail(err)
	default:
		d.Send()
		q.w.WriteHeader(http.StatusNoContent)
	}
}

// deleteScheduled deletes a resource; for an event, it works out what the
// deletion sends, as putScheduled does for a write.
func (q *request) deleteScheduled(c *Collection, owner *User, a access, uri string) (*Delivery, error) {
	cal := c.Home == HomeCalendars && c.ID != InboxID
	noReply := strings.EqualFold(strings.TrimSpace(q.r.Header.Get("Schedule-Reply")), "F")
	for attempt := 0; ; attempt++ {
		b, r, err := q.find(c, owner.Username, uri, cal)
		if err != nil {
			return nil, err
		}
		cond := conditions(q.r, c.ID)
		var d *Delivery
		if cal && r.Component == VEVENT {
			old, err := decodeEvent(r.Data)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", r.URI, err)
			}
			if d, err = q.s.Plan(ScheduleChange{Owner: owner, Old: old, MaySend: a.schedule(), NoReply: noReply}); err != nil {
				return nil, err
			}
			if unconditional(cond) {
				cond = davsql.Conditions{IfMatch: []int64{r.Version}}
			}
		}
		err = b.Delete(owner.Username, c, uri, cond)
		if errors.Is(err, ErrPrecondition) && d != nil && unconditional(conditions(q.r, c.ID)) && attempt < 2 {
			continue
		}
		return d, err
	}
}

func (q *request) propfindResource(c *Collection, owner *User, a access, uri string) {
	pf, err := q.propfindBody()
	if err != nil {
		http.Error(q.w, err.Error(), http.StatusBadRequest)
		return
	}
	b, r, err := q.find(c, owner.Username, uri, pf.wants(dataName(c.Home)))
	if errors.Is(err, ErrNotFound) {
		http.NotFound(q.w, q.r)
		return
	}
	if err != nil {
		q.fail(err)
		return
	}
	resp, ok := q.resourceResponse(c, owner, a, b, r, pf)
	if !ok {
		http.Error(q.w, "Forbidden", http.StatusForbidden)
		return
	}
	ms := multistatus{Responses: []response{resp}}
	ms.write(q.w)
}

// resourceResponse answers pf for one resource, or reports false when the
// user may not see it.
func (q *request) resourceResponse(c *Collection, owner *User, a access, b Backend, r *Resource, pf *propfind) (response, bool) {
	ok, err := q.served(c, a, b, r)
	if err != nil {
		q.s.Log.Error("dav: redact resource", "uri", r.URI, "err", err)
		return response{}, false
	}
	if !ok {
		return response{}, false
	}
	props := []prop{
		{dav("resourcetype"), ""},
		{dav("getetag"), escape(r.ETag(c.ID))},
		{dav("getcontenttype"), escape(contentType(r.Component))},
	}
	if r.Data != nil {
		props = append(props, prop{dav("getcontentlength"), strconv.Itoa(len(r.Data))})
	} else {
		props = append(props, prop{dav("getcontentlength"), strconv.FormatInt(r.Size, 10)})
	}
	var extra []prop
	if r.Data != nil {
		extra = append(extra, prop{dataName(c.Home), escape(string(r.Data))})
	}
	return response{Href: resourceHref(c, owner.Username, r.URI), Propstats: pf.evaluate(props, extra)}, true
}
