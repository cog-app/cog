package jmap

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/webpush"
)

// Push (RFC 8620 section 7): PushSubscription over the push store
// (internal/push, protocol "jmap", resource the user), and EventSource.
// Both tell a client the states of its accounts when something it can see
// changed; the DAV core says whose collections changed (dav.UserListener).

// Bits owed to a JMAP subscription.
const (
	pushState  uint = 1 << iota // a state the user sees changed
	pushVerify                  // the verification code is owed
)

// Subscription lifetimes.
const (
	pushDefaultExpiry = 7 * 24 * time.Hour
	pushMaxExpiry     = 30 * 24 * time.Hour
	pushTTL           = 24 * time.Hour
)

// pushOptions is what a JMAP subscription keeps besides its endpoint.
type pushOptions struct {
	DeviceClientID string   `json:"deviceClientId"`
	Types          []string `json:"types,omitempty"` // nil: every type
	Code           string   `json:"code"`            // the verification code sent
	Verified       bool     `json:"verified,omitempty"`
}

func options(s *push.Subscription) (pushOptions, error) {
	var o pushOptions
	if err := json.Unmarshal([]byte(s.Options), &o); err != nil {
		return o, fmt.Errorf("push subscription %s options: %w", s.ID, err)
	}
	return o, nil
}

// typeNames are the data types whose states a push reports.
func (s *Server) typeNames() []string {
	names := []string{"AddressBook", "ContactCard"}
	if s.Calendars {
		names = append(names, "Calendar", "CalendarEvent")
	}
	return names
}

// --- PushSubscription/get and /set ---

func (c *call) pushObject(sub *push.Subscription) (jsobj.Object, error) {
	o, err := options(sub)
	if err != nil {
		return nil, err
	}
	obj := jsobj.Object{
		"id":               sub.ID,
		"deviceClientId":   o.DeviceClientID,
		"expires":          sub.Expires.UTC().Format(time.RFC3339),
		"types":            nil,
		"verificationCode": nil,
	}
	if o.Types != nil {
		obj["types"] = o.Types
	}
	if o.Verified {
		obj["verificationCode"] = o.Code
	}
	return obj, nil
}

func (c *call) pushGet(args jsobj.Object) (any, error) {
	var in struct {
		IDs        *[]string `json:"ids"`
		Properties *[]string `json:"properties"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	subs, err := c.s.Push.OfUser(push.JMAP, c.req.user.ID)
	if err != nil {
		return nil, err
	}
	list, notFound := []any{}, []string{}
	for _, sub := range subs {
		if in.IDs != nil && !slices.Contains(*in.IDs, sub.ID) {
			continue
		}
		obj, err := c.pushObject(&sub)
		if err != nil {
			return nil, err
		}
		if in.Properties != nil {
			// The URL and keys are never returned (RFC 8620 section 7.2).
			obj = filterProps(obj, in.Properties)
		}
		list = append(list, obj)
	}
	if in.IDs != nil {
		for _, id := range *in.IDs {
			if !slices.ContainsFunc(subs, func(s push.Subscription) bool { return s.ID == id }) {
				notFound = append(notFound, id)
			}
		}
	}
	return map[string]any{"list": list, "notFound": notFound}, nil
}

// expiry reads a client's expires, within what the server allows.
func (c *call) expiry(v any) (time.Time, bool) {
	now := c.s.Clock()
	if v == nil {
		return now.Add(pushDefaultExpiry).Truncate(time.Second), true
	}
	s, ok := v.(string)
	t, err := time.Parse(time.RFC3339, s)
	if !ok || err != nil {
		return time.Time{}, false
	}
	if hi := now.Add(pushMaxExpiry); t.After(hi) {
		t = hi
	}
	return t.Truncate(time.Second), true
}

func (s *Server) knownTypes(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []string{}
	for _, x := range list {
		t, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, true
}

func (c *call) pushSet(args jsobj.Object) (any, error) {
	var in struct {
		Create  map[string]jsobj.Object `json:"create"`
		Update  map[string]jsobj.Object `json:"update"`
		Destroy []string                `json:"destroy"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	created, notCreated := map[string]any{}, map[string]any{}
	updated, notUpdated := map[string]any{}, map[string]any{}
	destroyed, notDestroyed := []string{}, map[string]any{}
	woke := false

	for _, cid := range sortedKeys(in.Create) {
		obj := in.Create[cid]
		var bad []string
		r := jsobj.Reader{Obj: obj, Bad: &bad}
		deviceID := r.String("deviceClientId")
		endpoint := r.String("url")
		u, err := url.Parse(endpoint)
		if deviceID == "" {
			bad = append(bad, "deviceClientId")
		}
		// Web Push is over TLS only (RFC 8030 section 8).
		if err != nil || u.Scheme != "https" || u.Host == "" {
			bad = append(bad, "url")
		}
		var keys webpush.Keys
		k, _ := r.Object("keys")
		kr := jsobj.Reader{Obj: k}
		pub, errPub := webpush.DecodeKey(kr.String("p256dh"))
		auth, errAuth := webpush.DecodeKey(kr.String("auth"))
		keys = webpush.Keys{P256DH: pub, Auth: auth}
		if errPub != nil || errAuth != nil || keys.Validate() != nil {
			// Pushes are always encrypted; a subscription without keys
			// could only carry them in the clear.
			bad = append(bad, "keys")
		}
		if obj["verificationCode"] != nil || obj["id"] != nil {
			bad = append(bad, "verificationCode")
		}
		expires, ok := c.expiry(obj["expires"])
		if !ok {
			bad = append(bad, "expires")
		}
		types, ok := c.s.knownTypes(obj["types"])
		if !ok {
			bad = append(bad, "types")
		}
		for k := range obj {
			switch k {
			case "deviceClientId", "url", "keys", "expires", "types", "verificationCode", "id":
			default:
				bad = append(bad, k)
			}
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			notCreated[cid] = &setError{Type: "invalidProperties", Properties: slices.Compact(bad)}
			continue
		}
		opts, err := json.Marshal(pushOptions{DeviceClientID: deviceID, Types: types, Code: strings.ToLower(rand.Text())})
		if err != nil {
			return nil, err
		}
		sub := &push.Subscription{UserID: c.req.user.ID, Protocol: push.JMAP, Resource: push.UserResource(c.req.user.ID),
			Endpoint: endpoint, Keys: keys, Options: string(opts), Expires: expires}
		if _, err := c.s.Push.Register(sub); errors.Is(err, push.ErrTaken) {
			notCreated[cid] = &setError{Type: "invalidProperties", Properties: []string{"url"}}
			continue
		} else if err != nil {
			return nil, err
		}
		// The verification code goes to the URL; the client sets it on
		// the subscription to prove it can read what is pushed there.
		if err := c.s.Push.Mark(pushVerify, sub.ID); err != nil {
			return nil, err
		}
		woke = true
		c.req.created[cid] = sub.ID
		created[cid] = jsobj.Object{"id": sub.ID, "expires": expires.UTC().Format(time.RFC3339)}
	}

	for _, id := range sortedKeys(in.Update) {
		patch := in.Update[id]
		sub, err := c.s.Push.Get(c.req.resolveID(id))
		if errors.Is(err, push.ErrNotFound) || err == nil && sub.UserID != c.req.user.ID {
			notUpdated[id] = &setError{Type: "notFound"}
			continue
		}
		if err != nil {
			return nil, err
		}
		o, err := options(sub)
		if err != nil {
			return nil, err
		}
		var bad []string
		var result jsobj.Object
		for k, v := range patch {
			switch k {
			case "verificationCode":
				code, _ := v.(string)
				if code == "" || code != o.Code {
					bad = append(bad, k)
					continue
				}
				o.Verified = true
			case "expires":
				t, ok := c.expiry(v)
				if !ok {
					bad = append(bad, k)
					continue
				}
				sub.Expires = t
				if v != nil && v.(string) != t.UTC().Format(time.RFC3339) {
					result = jsobj.Object{"expires": t.UTC().Format(time.RFC3339)}
				}
			case "types":
				types, ok := c.s.knownTypes(v)
				if !ok {
					bad = append(bad, k)
					continue
				}
				o.Types = types
			default:
				bad = append(bad, k)
			}
		}
		if len(bad) > 0 {
			slices.Sort(bad)
			notUpdated[id] = &setError{Type: "invalidProperties", Properties: bad}
			continue
		}
		b, err := json.Marshal(o)
		if err != nil {
			return nil, err
		}
		sub.Options = string(b)
		if err := c.s.Push.Update(sub); err != nil {
			return nil, err
		}
		if result != nil {
			updated[sub.ID] = result
		} else {
			updated[sub.ID] = nil
		}
	}

	for _, id := range in.Destroy {
		err := c.s.Push.Unregister(c.req.resolveID(id), c.req.user.ID)
		if errors.Is(err, push.ErrNotFound) {
			notDestroyed[id] = &setError{Type: "notFound"}
			continue
		}
		if err != nil {
			return nil, err
		}
		destroyed = append(destroyed, id)
	}
	if woke && c.s.Pusher != nil {
		c.s.Pusher.Wake()
	}
	out := map[string]any{}
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

// --- Noticing changes ---

// UsersChanged marks the JMAP subscriptions of the users whose
// collections changed, and wakes their EventSource connections. It is a
// dav.UserListener.
func (s *Server) UsersChanged(_ context.Context, users map[string]bool, all bool) error {
	s.hub.wake(users, all)
	if s.Push == nil {
		return nil
	}
	subs, err := s.Push.ForPrefix(push.JMAP, "user/")
	if err != nil {
		return err
	}
	var ids []string
	for _, sub := range subs {
		if all || users[strings.TrimPrefix(sub.Resource, "user/")] {
			ids = append(ids, sub.ID)
		}
	}
	return s.Push.Mark(pushState, ids...)
}

// stateChange is the StateChange object (RFC 8620 section 7.1) of
// everything a user sees, for the types asked for (nil: all).
func (s *Server) stateChange(userID string, types []string) (map[string]any, error) {
	u, err := s.DAV.Users.ByID(userID)
	if err != nil {
		return nil, err
	}
	sess, err := s.DAV.Session(u)
	if err != nil {
		return nil, err
	}
	q := &request{s: s, user: u, dav: sess}
	c := &call{s: s, req: q}
	accounts, err := q.accountList()
	if err != nil {
		return nil, err
	}
	want := func(t string) bool { return types == nil || slices.Contains(types, t) }
	changed := map[string]any{}
	for _, a := range accounts {
		st := map[string]string{}
		caps := a.caps(s)
		if slices.Contains(caps, CapContacts) {
			if want("AddressBook") {
				st["AddressBook"] = collectionsState(a.books)
			}
			if want("ContactCard") {
				if st["ContactCard"], _, _, err = c.state(contactCards, a); err != nil {
					return nil, err
				}
			}
		}
		if slices.Contains(caps, CapCalendars) {
			if want("Calendar") {
				st["Calendar"] = collectionsState(a.cals)
			}
			if want("CalendarEvent") {
				if st["CalendarEvent"], _, _, err = c.state(calendarEvents, a); err != nil {
					return nil, err
				}
			}
		}
		if len(st) > 0 {
			changed[a.id] = st
		}
	}
	return map[string]any{"@type": "StateChange", "changed": changed}, nil
}

// PushMessage writes what a JMAP subscription is owed: its verification
// first, then, once verified, the states of everything its user sees. It
// is the deliverer's messenger for push.JMAP.
func (s *Server) PushMessage(sub *push.Subscription) (webpush.Message, error) {
	o, err := options(sub)
	if err != nil {
		return webpush.Message{}, err
	}
	var body any
	switch {
	case sub.Pending&pushVerify != 0:
		body = map[string]any{"@type": "PushVerification", "pushSubscriptionId": sub.ID, "verificationCode": o.Code}
	case !o.Verified:
		return webpush.Message{}, push.ErrSkip
	default:
		if body, err = s.stateChange(sub.UserID, o.Types); err != nil {
			return webpush.Message{}, err
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return webpush.Message{}, err
	}
	return webpush.Message{
		Body: b, ContentType: "application/json; charset=utf-8", TTL: pushTTL,
		// A newer state replaces an undelivered one.
		Topic: shortHash("jmap " + sub.ID),
	}, nil
}

// --- EventSource (RFC 8620 section 7.3) ---

// hub wakes a user's EventSource connections.
type hub struct {
	mu    sync.Mutex
	conns map[string]map[chan struct{}]bool
}

func (h *hub) subscribe(userID string) (chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns == nil {
		h.conns = map[string]map[chan struct{}]bool{}
	}
	if h.conns[userID] == nil {
		h.conns[userID] = map[chan struct{}]bool{}
	}
	h.conns[userID][ch] = true
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.conns[userID], ch)
		if len(h.conns[userID]) == 0 {
			delete(h.conns, userID)
		}
	}
}

func (h *hub) wake(users map[string]bool, all bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, chans := range h.conns {
		if !all && !users[id] {
			continue
		}
		for ch := range chans {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Server) eventSource(w http.ResponseWriter, r *http.Request) {
	u := user(r)
	if u == nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}
	var types []string
	if t := r.URL.Query().Get("types"); t != "" && t != "*" {
		types = strings.Split(t, ",")
	}
	closeAfter := r.URL.Query().Get("closeafter") == "state"
	var ping time.Duration
	if p, err := strconv.Atoi(r.URL.Query().Get("ping")); err == nil && p > 0 {
		ping = time.Duration(max(p, 30)) * time.Second
	}
	ch, cancel := s.hub.subscribe(u.ID)
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	last := ""
	send := func() bool {
		st, err := s.stateChange(u.ID, types)
		if err != nil {
			s.logger().Error("jmap: event source state", "user", u.Username, "err", err)
			return false
		}
		b, err := json.Marshal(st)
		if err != nil {
			return false
		}
		if string(b) == last {
			return true
		}
		last = string(b)
		if _, err := fmt.Fprintf(w, "event: state\ndata: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return !closeAfter
	}
	if !send() {
		return
	}
	var tick <-chan time.Time
	if ping > 0 {
		t := time.NewTicker(ping)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send() {
				return
			}
		case <-tick:
			if _, err := fmt.Fprintf(w, "event: ping\ndata: {\"interval\":%d}\n\n", int(ping/time.Second)); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
