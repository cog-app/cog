// Package jmap serves JMAP (RFC 8620) over the DAV core's collections: JMAP
// for Contacts (RFC 9610) and, behind a flag, JMAP for Calendars
// (draft-ietf-jmap-calendars). Cards and events are the same rows DAV
// serves, as JSContact and JSCalendar (internal/dav/jscontact,
// internal/dav/jscal); writes go through the same backends, access checks
// and scheduling, so a change made over one protocol is seen over the other
// (DESIGN/dav-storage.md section 9).
//
// Every user is an account; a user who shares collections with another is
// an account of theirs too, holding just those collections.
package jmap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/push"
)

// Capabilities.
const (
	CapCore      = "urn:ietf:params:jmap:core"
	CapContacts  = "urn:ietf:params:jmap:contacts"
	CapCalendars = "urn:ietf:params:jmap:calendars"
)

// Paths, under the server's base URL.
const (
	WellKnown   = "/.well-known/jmap"
	SessionPath = "/jmap/session"
	APIPath     = "/jmap/api"
	// No method takes or returns blobs (photos are data: URIs), so these
	// answer 404; the session must name them.
	DownloadPath = "/jmap/download/"
	UploadPath   = "/jmap/upload/"
	EventsPath   = "/jmap/eventsource"
)

// Limits (RFC 8620 section 2).
const (
	maxSizeRequest        = 10 << 20
	maxConcurrentRequests = 4
	maxCallsInRequest     = 64
	maxObjectsInGet       = 1000
	maxObjectsInSet       = 500
)

// Server answers JMAP requests.
type Server struct {
	DAV *dav.Server
	// Push keeps push subscriptions; nil offers no PushSubscription.
	Push   *push.Store
	Pusher *push.Deliverer
	// BaseURL is the server's external URL, for the session's URLs; the
	// request's host is used without it.
	BaseURL string
	// Calendars offers JMAP for Calendars, which follows a draft.
	Calendars bool
	Clock     clock.Clock
	Log       *slog.Logger

	// running counts each user's requests in progress, for
	// maxConcurrentRequests.
	runningMu sync.Mutex
	running   map[string]int
	hub       hub
}

// Handler serves the well-known URL, the session resource and the API,
// for authenticated users.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+WellKnown, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, SessionPath, http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET "+SessionPath, s.session)
	mux.HandleFunc("POST "+APIPath, s.api)
	mux.HandleFunc("GET "+EventsPath, s.eventSource)
	mux.HandleFunc(DownloadPath, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc(UploadPath, func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "about:blank", "this server stores no blobs")
	})
	return mux
}

func (s *Server) logger() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// user returns the authenticated user as the DAV core names users.
func user(r *http.Request) *dav.User {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		return nil
	}
	return &dav.User{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email}
}

// problem writes an RFC 7807 problem, as JMAP's request-level errors are.
func problem(w http.ResponseWriter, status int, typ, detail string, extra ...any) {
	body := map[string]any{"type": typ, "status": status, "detail": detail}
	for i := 0; i+1 < len(extra); i += 2 {
		body[extra[i].(string)] = extra[i+1]
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Default().Debug("jmap: write problem", "err", err)
	}
}

func writeJSON(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	return json.NewEncoder(w).Encode(v)
}

func (s *Server) base(r *http.Request) string {
	if s.BaseURL != "" {
		return strings.TrimSuffix(s.BaseURL, "/")
	}
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

// --- Requests ---

// methodError is a method-level error (RFC 8620 section 3.6.2).
type methodError struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	// Properties lists invalid arguments, for some types.
	Properties []string `json:"properties,omitempty"`
}

func (e *methodError) Error() string { return e.Type + ": " + e.Description }

func errorOf(typ, format string, args ...any) *methodError {
	return &methodError{Type: typ, Description: fmt.Sprintf(format, args...)}
}

func invalidArguments(format string, args ...any) *methodError {
	return errorOf("invalidArguments", format, args...)
}

// call is one method call being answered.
type call struct {
	s   *Server
	ctx context.Context
	req *request
	id  string // the call id
}

// request is one API request's state: the user's DAV session, the
// responses so far and the ids made.
type request struct {
	s         *Server
	user      *dav.User
	dav       *dav.Session
	using     []string
	responses [][]any
	created   map[string]string
	accounts  map[string]*account // loaded on first use
}

type method struct {
	capability string
	fn         func(c *call, args jsobj.Object) (any, error)
}

func (s *Server) methods() map[string]method {
	m := map[string]method{
		"Core/echo": {CapCore, func(_ *call, args jsobj.Object) (any, error) { return args, nil }},

		"AddressBook/get":          {CapContacts, (*call).addressBookGet},
		"AddressBook/changes":      {CapContacts, (*call).addressBookChanges},
		"AddressBook/set":          {CapContacts, (*call).addressBookSet},
		"ContactCard/get":          {CapContacts, (*call).contactGet},
		"ContactCard/changes":      {CapContacts, (*call).contactChanges},
		"ContactCard/query":        {CapContacts, (*call).contactQuery},
		"ContactCard/queryChanges": {CapContacts, cannotCalculate},
		"ContactCard/set":          {CapContacts, (*call).contactSet},
	}
	if s.Calendars {
		m["Calendar/get"] = method{CapCalendars, (*call).calendarGet}
		m["Calendar/changes"] = method{CapCalendars, (*call).calendarChanges}
		m["Calendar/set"] = method{CapCalendars, (*call).calendarSet}
		m["CalendarEvent/get"] = method{CapCalendars, (*call).eventGet}
		m["CalendarEvent/changes"] = method{CapCalendars, (*call).eventChanges}
		m["CalendarEvent/query"] = method{CapCalendars, (*call).eventQuery}
		m["CalendarEvent/queryChanges"] = method{CapCalendars, cannotCalculate}
		m["CalendarEvent/set"] = method{CapCalendars, (*call).eventSet}
	}
	if s.Push != nil {
		m["PushSubscription/get"] = method{CapCore, (*call).pushGet}
		m["PushSubscription/set"] = method{CapCore, (*call).pushSet}
	}
	return m
}

// cannotCalculate answers /queryChanges: query results are not tracked, so
// clients query again.
func cannotCalculate(*call, jsobj.Object) (any, error) {
	return nil, errorOf("cannotCalculateChanges", "query changes are not tracked; query again")
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if !s.enter(u.ID) {
		problem(w, http.StatusTooManyRequests, "urn:ietf:params:jmap:error:limit", "too many concurrent requests", "limit", "maxConcurrentRequests")
		return
	}
	defer s.leave(u.ID)
	// JSON only (RFC 8620 section 3.1): a browser cannot send it to
	// another site without asking, so a session cookie cannot be used by
	// a page elsewhere.
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "the request's content type is not application/json")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSizeRequest+1))
	if err != nil {
		problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "could not read the request")
		return
	}
	if len(body) > maxSizeRequest {
		problem(w, http.StatusRequestEntityTooLarge, "urn:ietf:params:jmap:error:limit", "request too large", "limit", "maxSizeRequest")
		return
	}
	parsed, err := jsobj.Parse(body)
	if err != nil {
		problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notJSON", "the request is not JSON")
		return
	}
	var in struct {
		Using       []string          `json:"using"`
		MethodCalls [][]any           `json:"methodCalls"`
		CreatedIDs  map[string]string `json:"createdIds"`
	}
	if b, err := json.Marshal(parsed); err != nil || decodeStrict(b, &in) != nil || in.Using == nil || in.MethodCalls == nil {
		problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notRequest", "the request is not a JMAP Request object")
		return
	}
	caps := s.capabilities()
	for _, c := range in.Using {
		if _, ok := caps[c]; !ok {
			problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:unknownCapability", "unknown capability "+c)
			return
		}
	}
	if len(in.MethodCalls) > maxCallsInRequest {
		problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:limit", "too many method calls", "limit", "maxCallsInRequest")
		return
	}
	sess, err := s.DAV.Session(u)
	if err != nil {
		s.logger().Error("jmap: session", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	q := &request{s: s, user: u, dav: sess, using: in.Using, created: map[string]string{}}
	maps.Copy(q.created, in.CreatedIDs)
	methods := s.methods()
	for _, mc := range in.MethodCalls {
		if len(mc) != 3 {
			problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notRequest", "an Invocation has three members")
			return
		}
		name, ok1 := mc[0].(string)
		args, ok2 := mc[1].(jsobj.Object)
		id, ok3 := mc[2].(string)
		if !ok1 || !ok2 || !ok3 {
			problem(w, http.StatusBadRequest, "urn:ietf:params:jmap:error:notRequest", "an Invocation is [name, arguments, id]")
			return
		}
		q.responses = append(q.responses, s.invoke(r.Context(), q, methods, name, args, id)...)
	}
	resp := map[string]any{"methodResponses": q.responses, "sessionState": s.sessionState(q)}
	if in.CreatedIDs != nil {
		resp["createdIds"] = q.created
	}
	if err := writeJSON(w, resp); err != nil {
		s.logger().Debug("jmap: write response", "err", err)
	}
}

// enter counts a request of a user's in, or reports false when they have
// maxConcurrentRequests running.
func (s *Server) enter(userID string) bool {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.running == nil {
		s.running = map[string]int{}
	}
	if s.running[userID] >= maxConcurrentRequests {
		return false
	}
	s.running[userID]++
	return true
}

func (s *Server) leave(userID string) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.running[userID]--; s.running[userID] <= 0 {
		delete(s.running, userID)
	}
}

func decodeStrict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(v)
}

// invoke answers one method call.
func (s *Server) invoke(ctx context.Context, q *request, methods map[string]method, name string, args jsobj.Object, id string) [][]any {
	fail := func(e *methodError) [][]any { return [][]any{{"error", e, id}} }
	m, ok := methods[name]
	if !ok || !slices.Contains(q.using, m.capability) {
		return fail(errorOf("unknownMethod", "%s is not a method this server offers for the capabilities used", name))
	}
	args, merr := q.resolve(args)
	if merr != nil {
		return fail(merr)
	}
	c := &call{s: s, ctx: ctx, req: q, id: id}
	out, err := m.fn(c, args)
	var me *methodError
	switch {
	case errors.As(err, &me):
		return fail(me)
	case err != nil:
		s.logger().Error("jmap: method failed", "method", name, "user", q.user.Username, "err", err)
		return fail(errorOf("serverFail", "the server could not complete %s", name))
	}
	return [][]any{{name, out, id}}
}

// --- Back-references (RFC 8620 section 3.7) ---

// resolve replaces the "#name" arguments that refer to earlier results.
func (q *request) resolve(args jsobj.Object) (jsobj.Object, *methodError) {
	out := jsobj.Object{}
	for k, v := range args {
		name, ok := strings.CutPrefix(k, "#")
		if !ok {
			continue
		}
		if _, dup := args[name]; dup {
			return nil, invalidArguments("both %s and #%s are given", name, name)
		}
		ref, ok := v.(jsobj.Object)
		resultOf, _ := ref["resultOf"].(string)
		refName, _ := ref["name"].(string)
		path, _ := ref["path"].(string)
		if !ok || resultOf == "" || refName == "" {
			return nil, errorOf("invalidResultReference", "#%s is not a ResultReference", name)
		}
		var found any
		for _, r := range q.responses {
			if r[2] == resultOf && r[0] == refName {
				tree, err := jsobj.Tree(r[1])
				if err != nil {
					return nil, errorOf("invalidResultReference", "result of %s cannot be read", resultOf)
				}
				found = tree
			}
		}
		if found == nil {
			return nil, errorOf("invalidResultReference", "no %s response with id %s", refName, resultOf)
		}
		val, err := evaluate(found, path)
		if err != nil {
			return nil, errorOf("invalidResultReference", "#%s: %v", name, err)
		}
		out[name] = val
	}
	for k, v := range args {
		if !strings.HasPrefix(k, "#") {
			out[k] = v
		}
	}
	return out, nil
}

// evaluate applies a JSON pointer with JMAP's "*", which maps the rest of
// the path over an array and flattens arrays it gets.
func evaluate(v any, path string) (any, error) {
	if path == "" || path == "/" {
		return v, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("path %q does not start with /", path)
	}
	parts := strings.Split(path[1:], "/")
	return walk(v, parts)
}

func walk(v any, parts []string) (any, error) {
	if len(parts) == 0 {
		return v, nil
	}
	part := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	switch t := v.(type) {
	case jsobj.Object:
		next, ok := t[part]
		if !ok {
			return nil, fmt.Errorf("no property %q", part)
		}
		return walk(next, parts[1:])
	case []any:
		if part == "*" {
			var out []any
			for _, x := range t {
				r, err := walk(x, parts[1:])
				if err != nil {
					return nil, err
				}
				if list, ok := r.([]any); ok {
					out = append(out, list...)
				} else {
					out = append(out, r)
				}
			}
			if out == nil {
				out = []any{}
			}
			return out, nil
		}
		var i int
		if _, err := fmt.Sscan(part, &i); err != nil || i < 0 || i >= len(t) {
			return nil, fmt.Errorf("no element %q", part)
		}
		return walk(t[i], parts[1:])
	}
	return nil, fmt.Errorf("cannot descend into %q", part)
}

// --- Arguments ---

// decodeArgs reads arguments into a struct; unknown arguments are an error
// (RFC 8620 section 3.6.2, invalidArguments).
func decodeArgs(args jsobj.Object, v any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return invalidArguments("arguments: %v", err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return invalidArguments("%v", err)
	}
	return nil
}

// --- Session resource ---

func (s *Server) capabilities() map[string]any {
	caps := map[string]any{
		CapCore: map[string]any{
			"maxSizeUpload":         0,
			"maxConcurrentUpload":   1,
			"maxSizeRequest":        maxSizeRequest,
			"maxConcurrentRequests": maxConcurrentRequests,
			"maxCallsInRequest":     maxCallsInRequest,
			"maxObjectsInGet":       maxObjectsInGet,
			"maxObjectsInSet":       maxObjectsInSet,
			"collationAlgorithms":   []string{"i;ascii-casemap", "i;unicode-casemap"},
		},
		CapContacts: map[string]any{},
	}
	if s.Calendars {
		caps[CapCalendars] = map[string]any{}
	}
	return caps
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	sess, err := s.DAV.Session(u)
	if err != nil {
		s.logger().Error("jmap: session", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	q := &request{s: s, user: u, dav: sess}
	accounts, err := q.accountList()
	if err != nil {
		s.logger().Error("jmap: accounts", "user", u.Username, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	accs := map[string]any{}
	for _, a := range accounts {
		accs[a.id] = a.describe(s)
	}
	primary := map[string]string{CapContacts: accountID(u.ID)}
	if s.Calendars {
		primary[CapCalendars] = accountID(u.ID)
	}
	base := s.base(r)
	err = writeJSON(w, map[string]any{
		"capabilities":    s.capabilities(),
		"accounts":        accs,
		"primaryAccounts": primary,
		"username":        u.Username,
		"apiUrl":          base + APIPath,
		"downloadUrl":     base + DownloadPath + "{accountId}/{blobId}/{name}?accept={type}",
		"uploadUrl":       base + UploadPath + "{accountId}/",
		"eventSourceUrl":  base + EventsPath + "?types={types}&closeafter={closeafter}&ping={ping}",
		"state":           s.sessionState(q),
	})
	if err != nil {
		s.logger().Debug("jmap: write session", "err", err)
	}
}

// sessionState changes when the accounts the user sees change.
func (s *Server) sessionState(q *request) string {
	accounts, err := q.accountList()
	if err != nil {
		return "0"
	}
	var b strings.Builder
	for _, a := range accounts {
		b.WriteString(a.id)
		for _, cp := range a.caps(s) {
			b.WriteString("," + cp)
		}
		b.WriteString(";")
	}
	return shortHash(b.String())
}
