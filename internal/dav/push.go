package dav

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/webpush"
)

// WebDAV-Push (draft-bitfire-webdav-push, as DAVx5 implements it): clients
// register a Web Push subscription on a collection, an inbox or a home,
// and are sent a push when it changes, instead of polling.
//
// Changes are noticed where they happen, so no write path can forget them:
// a resource write bumps its collection's row in dav_sync, which a
// connection observer (SyncObserver) sees and reports when the transaction
// commits; collections created, changed and deleted are reported by the
// Store; shares by ShareChanged. Reports gather in pushQueue until the
// deliverer's next round (push.Debounce after the first), which turns them
// into marks on subscriptions; a mark is a bit owed, so a burst of writes
// to a collection is one push to each subscriber.

const nsPush = "https://bitfire.at/webdav-push"

func pushName(local string) xml.Name { return xml.Name{Space: nsPush, Local: local} }

// pushPath is where registrations live, under Root: DELETE one to
// unsubscribe.
const pushPath = "push"

// Triggers, as bits owed to a subscription.
const (
	pushContent  uint = 1 << iota // a member was written or deleted, or the collection deleted
	pushProperty                  // the collection's own properties changed
)

// pushOptions are a WebDAV subscription's triggers, as stored.
type pushOptions struct {
	Content  bool `json:"content,omitempty"`
	Property bool `json:"property,omitempty"`
}

func (o pushOptions) bits() uint {
	var b uint
	if o.Content {
		b |= pushContent
	}
	if o.Property {
		b |= pushProperty
	}
	return b
}

// Subscription lifetimes. The draft asks for at least three days; DAVx5
// renews daily.
const (
	pushDefaultExpiry = 7 * 24 * time.Hour
	pushMaxExpiry     = 7 * 24 * time.Hour
	pushMinExpiry     = time.Hour
	// pushTTL is how long a push service keeps an undelivered push. A
	// device that was away longer will sync when it is back anyway.
	pushTTL = 24 * time.Hour
)

// What a subscription watches, as push.Subscription.Resource.
func collectionResource(id CollectionID) string { return "collection/" + id.String() }
func inboxResource(userID string) string        { return "inbox/" + userID }
func homeResource(userID string, h Home) string { return "home/" + userID + "/" + string(h) }

// EnablePush offers WebDAV-Push: subscriptions are kept in store and
// delivered by d, which must have MarkPush among its sources and
// PushMessage as its WebDAV messenger, signed with v. Call it before
// serving; the Store reports collection changes from then on.
func (s *Server) EnablePush(store *push.Store, d *push.Deliverer, v *webpush.VAPID) {
	s.Push, s.Pusher, s.VAPID = store, d, v
	s.Collections.observe = s.collectionChanged
}

// pushEnabled reports whether the server can take subscriptions.
func (s *Server) pushEnabled() bool { return s.Push != nil && s.VAPID != nil && s.Pusher != nil }

// pushTopic is a resource's topic: server-wide unique, stable, and opaque,
// since push services see it in the Topic header. It is short enough for
// that header (RFC 8030 section 5.4).
func (s *Server) pushTopic(resource string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte("webdav-push topic\x00" + resource))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// pushProps are the push properties of a resource that takes
// subscriptions, given by name only (section 3.1 of the draft).
func (s *Server) pushProps(resource string, property bool) []prop {
	if !s.pushEnabled() {
		return nil
	}
	triggers := el(pushName("content-update"), el(dav("depth"), "1"))
	if property {
		triggers += el(pushName("property-update"), el(dav("depth"), "0"))
	}
	return []prop{
		{pushName("transports"), el(pushName("web-push"),
			`<vapid-public-key xmlns="`+nsPush+`" type="p256ecdsa">`+escape(s.VAPID.PublicKey())+`</vapid-public-key>`)},
		{pushName("topic"), escape(s.pushTopic(resource))},
		{pushName("supported-triggers"), triggers},
	}
}

// --- Registration ---

// pushRegister answers a push-register POST to a resource the user may
// read (section 4.1 of the draft).
func (q *request) pushRegister(n *node, resource string, property bool) {
	if !q.s.pushEnabled() {
		writeError(q.w, http.StatusForbidden, pushName("push-not-available"), "")
		return
	}
	sub := n.child(pushName("subscription")).child(pushName("web-push-subscription"))
	endpoint := sub.child(pushName("push-resource")).text()
	u, err := url.Parse(endpoint)
	// Web Push is over TLS only (RFC 8030 section 8).
	if sub == nil || err != nil || u.Scheme != "https" || u.Host == "" {
		writeError(q.w, http.StatusForbidden, pushName("invalid-subscription"), "")
		return
	}
	if enc := sub.child(pushName("content-encoding")).text(); enc != "" && enc != "aes128gcm" {
		writeError(q.w, http.StatusForbidden, pushName("invalid-subscription"), "")
		return
	}
	// Messages are always encrypted (section 6.3 of the draft), so the
	// keys are required.
	var keys webpush.Keys
	pub, errPub := webpush.DecodeKey(sub.child(pushName("subscription-public-key")).text())
	auth, errAuth := webpush.DecodeKey(sub.child(pushName("auth-secret")).text())
	keys = webpush.Keys{P256DH: pub, Auth: auth}
	if errPub != nil || errAuth != nil || keys.Validate() != nil {
		writeError(q.w, http.StatusForbidden, pushName("invalid-subscription"), "")
		return
	}
	// Triggers are downgraded rather than refused: content updates are
	// supported at depth 1 (which is everything, collections holding no
	// collections), property updates at depth 0 and only on collections.
	trigger := n.child(pushName("trigger"))
	opts := pushOptions{
		Content:  trigger.child(pushName("content-update")) != nil,
		Property: property && trigger.child(pushName("property-update")) != nil,
	}
	if !opts.Content && !opts.Property {
		writeError(q.w, http.StatusForbidden, pushName("no-supported-trigger"), "")
		return
	}
	now := q.s.Clock()
	expires := now.Add(pushDefaultExpiry)
	if e := n.child(pushName("expires")); e != nil {
		t, err := http.ParseTime(e.text())
		if err != nil {
			http.Error(q.w, "expires is not an HTTP date", http.StatusBadRequest)
			return
		}
		expires = t
	}
	if lo := now.Add(pushMinExpiry); expires.Before(lo) {
		expires = lo
	}
	if hi := now.Add(pushMaxExpiry); expires.After(hi) {
		expires = hi
	}
	expires = expires.Truncate(time.Second)
	js, err := json.Marshal(opts)
	if err != nil {
		q.fail(err)
		return
	}
	s := &push.Subscription{UserID: q.user.ID, Protocol: push.WebDAV, Resource: resource, Endpoint: endpoint,
		Keys: keys, Options: string(js), Expires: expires}
	if _, err := q.s.Push.Register(s); errors.Is(err, push.ErrTaken) {
		writeError(q.w, http.StatusForbidden, pushName("invalid-subscription"), "")
		return
	} else if err != nil {
		q.fail(err)
		return
	}
	q.w.Header().Set("Location", q.s.registrationURL(s.ID))
	q.w.Header().Set("Expires", expires.UTC().Format(http.TimeFormat))
	q.w.WriteHeader(http.StatusNoContent)
}

func (s *Server) registrationURL(id string) string {
	return strings.TrimSuffix(s.BaseURL, "/") + Root + pushPath + "/" + id
}

// pushRegistration serves a registration URL: its owner DELETEs it to
// unsubscribe (section 4.3 of the draft).
func (q *request) pushRegistration(rest string) {
	id := strings.TrimSuffix(rest, "/")
	if id == "" || strings.Contains(id, "/") || q.s.Push == nil {
		http.NotFound(q.w, q.r)
		return
	}
	switch q.r.Method {
	case http.MethodDelete:
		err := q.s.Push.Unregister(id, q.user.ID)
		if errors.Is(err, push.ErrNotFound) {
			http.NotFound(q.w, q.r)
			return
		}
		if err != nil {
			q.fail(err)
			return
		}
		q.w.WriteHeader(http.StatusNoContent)
	case http.MethodOptions:
		q.options("OPTIONS, DELETE")
	default:
		q.methodNotAllowed("OPTIONS, DELETE")
	}
}

// --- Noticing changes ---

// pushKey is something that changed, as noticed: resolved to a resource
// when the queue is flushed.
type pushKey struct {
	collection CollectionID // a collection; 0 for the others
	inboxOf    string       // the username whose inbox changed
	homeOf     string       // the user ID whose home changed
	home       Home         // with homeOf, or alone: every user's home of that kind
}

// pushChange is what is known of one key since the last flush.
type pushChange struct {
	bits    uint
	commits int // changes reported, for Push-Dont-Notify
	// suppressed counts, per registration, the changes made by requests
	// that asked not to notify it; all counts those that asked for none.
	suppressed map[string]int
	all        int
}

// pushQueue gathers changes between the deliverer's rounds.
type pushQueue struct {
	mu      sync.Mutex
	changes map[pushKey]*pushChange
}

func (pq *pushQueue) add(k pushKey, bits uint) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if pq.changes == nil {
		pq.changes = map[pushKey]*pushChange{}
	}
	ch := pq.changes[k]
	if ch == nil {
		ch = &pushChange{}
		pq.changes[k] = ch
	}
	ch.bits |= bits
	ch.commits++
}

// commits returns how many changes of k were reported since the last
// flush, to compare with after a request.
func (pq *pushQueue) commits(k pushKey) int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if ch := pq.changes[k]; ch != nil {
		return ch.commits
	}
	return 0
}

// suppress records that a request that made a change to k, if it made one
// since before (the count from commits), asked not to notify regs, or
// anyone when all is set.
func (pq *pushQueue) suppress(k pushKey, before int, regs []string, all bool) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	ch := pq.changes[k]
	if ch == nil || ch.commits <= before {
		return // flushed meanwhile, or the request changed nothing
	}
	if all {
		ch.all++
		return
	}
	if ch.suppressed == nil {
		ch.suppressed = map[string]int{}
	}
	for _, r := range regs {
		ch.suppressed[r]++
	}
}

// silenced reports whether every change was made by requests that asked
// not to notify registration id.
func (ch *pushChange) silenced(id string) bool { return ch.all+ch.suppressed[id] >= ch.commits }

func (pq *pushQueue) take() map[pushKey]*pushChange {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	out := pq.changes
	pq.changes = nil
	return out
}

// notify reports a change and wakes the deliverer.
func (s *Server) notify(k pushKey, bits uint) {
	if s.Pusher == nil {
		return
	}
	s.pushQ.add(k, bits)
	s.Pusher.Wake()
}

// SyncObserver returns the observer of one connection to a user's plugin
// database: a collection whose dav_sync row a committed transaction wrote
// has changed members. Install it on every plugin database
// (plugin.Registry.SetTxObserverFactory).
func (s *Server) SyncObserver(username, _ string) db.TxObserver {
	return &syncObserver{s: s, username: username}
}

type syncObserver struct {
	s        *Server
	username string
	ids      []CollectionID // written by the transaction in progress
}

func (o *syncObserver) Change(op int, _, table string, rowid int64) {
	// dav_sync's rowid is its collection_id. A deleted row is a purged
	// collection, which the Store reports.
	if table != "dav_sync" || op == sqlite3.SQLITE_DELETE {
		return
	}
	id := CollectionID(rowid)
	for _, x := range o.ids {
		if x == id {
			return
		}
	}
	o.ids = append(o.ids, id)
}

func (o *syncObserver) Commit() {
	for _, id := range o.ids {
		if id == InboxID {
			o.s.notify(pushKey{inboxOf: o.username}, pushContent)
		} else {
			o.s.notify(pushKey{collection: id}, pushContent)
		}
	}
	o.ids = o.ids[:0]
}

func (o *syncObserver) Rollback() { o.ids = o.ids[:0] }

// collectionEvent is what the Store did to a collection.
type collectionEvent int

const (
	collectionCreated collectionEvent = iota
	collectionUpdated
	collectionDeleted
)

// collectionChanged is the Store's report of a collection created, changed
// or deleted.
func (s *Server) collectionChanged(c *Collection, ev collectionEvent) {
	switch ev {
	case collectionCreated:
		s.notify(pushKey{homeOf: c.OwnerID, home: c.Home}, pushContent)
	case collectionUpdated:
		s.notify(pushKey{collection: c.ID}, pushProperty)
	case collectionDeleted:
		// The removal of the subscribed resource is a content update.
		s.notify(pushKey{collection: c.ID}, pushContent)
		s.notify(pushKey{homeOf: c.OwnerID, home: c.Home}, pushContent)
	}
}

// sharePushChanged reports a share of collection id to a subject: the
// collection's privileges changed, and the homes it appears in.
func (s *Server) sharePushChanged(subjectType, subjectID string, c *Collection) {
	s.notify(pushKey{collection: c.ID}, pushProperty)
	if subjectType == "USER" {
		s.notify(pushKey{homeOf: subjectID, home: c.Home}, pushContent)
		return
	}
	// A group's or everyone's: every home of that kind may have changed.
	s.notify(pushKey{home: c.Home}, pushContent)
}

// --- Marking ---

// MarkPush turns the changes noticed since the last call into pushes owed:
// it finds the subscriptions to what changed, keeps those whose triggers
// match and whose user may still read it, and marks them. A sharee's
// subscription to a collection they can no longer read is removed. It is a
// source of the deliverer (push.Deliverer.Sources).
func (s *Server) MarkPush(context.Context) error {
	if s.Push == nil {
		return nil
	}
	changes := s.pushQ.take()
	if len(changes) == 0 {
		return nil
	}
	byResource := map[string]*pushChange{}
	merge := func(res string, ch *pushChange) {
		if prev := byResource[res]; prev != nil {
			// Two keys for one resource cannot happen today; keep the
			// union, and suppress nothing rather than too much.
			prev.bits |= ch.bits
			prev.commits += ch.commits
			return
		}
		byResource[res] = ch
	}
	var resources []string
	var subs []push.Subscription
	for k, ch := range changes {
		var res string
		switch {
		case k.collection != 0:
			res = collectionResource(k.collection)
		case k.inboxOf != "":
			u, err := s.Users.ByUsername(k.inboxOf)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			res = inboxResource(u.ID)
		case k.homeOf != "":
			res = homeResource(k.homeOf, k.home)
		default:
			// Every home of a kind: subscriptions are few, so find them.
			all, err := s.Push.ForPrefix(push.WebDAV, "home/")
			if err != nil {
				return err
			}
			for _, sub := range all {
				if strings.HasSuffix(sub.Resource, "/"+string(k.home)) {
					merge(sub.Resource, &pushChange{bits: ch.bits, commits: ch.commits})
				}
			}
			continue
		}
		merge(res, ch)
	}
	for res := range byResource {
		resources = append(resources, res)
	}
	found, err := s.Push.For(push.WebDAV, resources)
	if err != nil {
		return err
	}
	subs = append(subs, found...)
	if len(subs) == 0 {
		return nil
	}

	// The collections, for their owners; and what is shared with each
	// subscriber who is not the owner, once per subscriber.
	var ids []CollectionID
	for _, sub := range subs {
		if id, ok := subscribedCollection(sub.Resource); ok {
			ids = append(ids, id)
		}
	}
	cols := map[CollectionID]*Collection{}
	if len(ids) > 0 {
		list, err := s.Collections.ByIDs(ids)
		if err != nil {
			return err
		}
		for i := range list {
			cols[list[i].ID] = &list[i]
		}
	}
	shared := map[string]shareMap{}

	marks := map[uint][]string{}
	var lost []string
	for _, sub := range subs {
		ch := byResource[sub.Resource]
		var opts pushOptions
		if err := json.Unmarshal([]byte(sub.Options), &opts); err != nil {
			return fmt.Errorf("push subscription %s options: %w", sub.ID, err)
		}
		bits := ch.bits & opts.bits()
		if bits == 0 || ch.silenced(sub.ID) {
			continue
		}
		if id, ok := subscribedCollection(sub.Resource); ok {
			// A deleted collection is told once; its subscriptions go with
			// the next cleanup.
			if c := cols[id]; c != nil && c.OwnerID != sub.UserID {
				m, ok := shared[sub.UserID]
				if !ok {
					if m, err = sharesFor(s.Shares, sub.UserID); err != nil {
						return err
					}
					shared[sub.UserID] = m
				}
				if !accessTo(c, false, m.perms(c)).read() {
					lost = append(lost, sub.ID)
					continue
				}
			}
		}
		marks[bits] = append(marks[bits], sub.ID)
	}
	if err := s.Push.Remove(lost...); err != nil {
		return err
	}
	for bits, ids := range marks {
		if err := s.Push.Mark(bits, ids...); err != nil {
			return err
		}
	}
	return nil
}

func subscribedCollection(resource string) (CollectionID, bool) {
	rest, ok := strings.CutPrefix(resource, "collection/")
	if !ok {
		return 0, false
	}
	var id int64
	if _, err := fmt.Sscan(rest, &id); err != nil {
		return 0, false
	}
	return CollectionID(id), true
}

// PushCleanup removes subscriptions to collections that no longer exist,
// once they have been told. It is a cleanup of the deliverer.
func (s *Server) PushCleanup(context.Context) error {
	return s.Collections.dropOrphanPushes()
}

// PushMessage writes the push owed to a WebDAV subscription: content-free,
// the topic and which kinds of update happened (section 5.1 of the draft).
// The sync token is left out: the client syncs either way.
func (s *Server) PushMessage(sub *push.Subscription) (webpush.Message, error) {
	topic := s.pushTopic(sub.Resource)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<P:push-message xmlns:D="DAV:" xmlns:P="` + nsPush + `"><P:topic>` + topic + `</P:topic>`)
	if sub.Pending&pushContent != 0 {
		b.WriteString(`<P:content-update/>`)
	}
	if sub.Pending&pushProperty != 0 {
		b.WriteString(`<P:property-update/>`)
	}
	b.WriteString(`</P:push-message>`)
	return webpush.Message{
		Body:        []byte(b.String()),
		ContentType: `application/xml; charset="UTF-8"`,
		TTL:         pushTTL,
		// The push is the same whatever changed, so an undelivered one
		// may be replaced by the next.
		Topic: topic,
	}, nil
}

// --- Push-Dont-Notify ---

// dontNotify reads the request's Push-Dont-Notify header (section 5.2 of
// the draft): the registrations of the current user it names, or all.
// Others' registrations and values that are not registration URLs are
// ignored, as the draft asks.
func (q *request) dontNotify() (regs []string, all bool, err error) {
	vals := q.r.Header.Values("Push-Dont-Notify")
	if len(vals) == 0 || q.s.Push == nil {
		return nil, false, nil
	}
	for _, v := range vals {
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "*" {
				return nil, true, nil
			}
			u := strings.Trim(part, `"`)
			_, id, ok := strings.Cut(u, Root+pushPath+"/")
			if !ok || id == "" || strings.Contains(id, "/") {
				continue
			}
			sub, err := q.s.Push.Get(id)
			if errors.Is(err, push.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, false, err
			}
			if sub.UserID == q.user.ID {
				regs = append(regs, id)
			}
		}
	}
	return regs, false, nil
}

// suppressPush prepares to honour Push-Dont-Notify for a write to the
// collection or inbox k: the returned function, called once the request is
// answered, records the request's change as one not to notify the named
// registrations of. It returns nil when there is nothing to suppress.
func (q *request) suppressPush(k pushKey) (func(), error) {
	regs, all, err := q.dontNotify()
	if err != nil || (len(regs) == 0 && !all) {
		return nil, err
	}
	before := q.s.pushQ.commits(k)
	return func() { q.s.pushQ.suppress(k, before, regs, all) }, nil
}
