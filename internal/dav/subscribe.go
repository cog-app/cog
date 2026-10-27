package dav

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// ICS subscriptions: read-only calendars filled from a remote iCalendar feed
// (a school's holidays, a team's fixtures). The events are stored as rows
// like any calendar's, through the backend that stores events, so the web UI
// shows them and DAV clients sync them; nobody may write to them (Kind).
// DESIGN/dav-storage.md section 9.

// Limits on fetching a feed.
const (
	maxFeedSize    = 10 << 20
	feedTimeout    = 30 * time.Second
	defaultRefresh = time.Hour
	minRefresh     = 15 * time.Minute
	maxRefresh     = 7 * 24 * time.Hour
	maxBackoff     = 24 * time.Hour
	// pollLease is how long a poller holds a subscription it claimed, so
	// another replica does not fetch it at the same time.
	pollLease = 10 * time.Minute
	// pollBatch bounds the subscriptions polled in one pass.
	pollBatch = 20
)

// Subscription is the state of a subscription collection's feed.
type Subscription struct {
	Collection   CollectionID
	URL          string // as the user gave it (webcal:// included)
	ETag         string // validators of the last response
	LastModified string
	// Refresh is how often the feed asks to be fetched (REFRESH-INTERVAL
	// or X-PUBLISHED-TTL); 0 when it does not say.
	Refresh     time.Duration
	NextPoll    time.Time
	LastPoll    time.Time
	LastSuccess time.Time // zero until a fetch succeeds
	Failures    int       // consecutive failed fetches
	LastError   string    // why the last fetch failed; "" when it did not
	Skipped     int       // components of the last fetch a subscription does not hold
}

const subscriptionColumns = `collection_id, url, etag, last_modified, refresh, next_poll_at, last_poll_at,
	last_success_at, failures, last_error, skipped`

func scanSubscription(row interface{ Scan(...any) error }) (*Subscription, error) {
	var s Subscription
	var refresh, next, last, success int64
	if err := row.Scan(&s.Collection, &s.URL, &s.ETag, &s.LastModified, &refresh, &next, &last,
		&success, &s.Failures, &s.LastError, &s.Skipped); err != nil {
		return nil, err
	}
	s.Refresh = time.Duration(refresh) * time.Second
	s.NextPoll, s.LastPoll = unixTime(next), unixTime(last)
	s.LastSuccess = unixTime(success)
	return &s, nil
}

func unixTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Subscriptions returns the state of a user's subscriptions, by collection.
func (s *Store) Subscriptions(ownerID string) (map[CollectionID]Subscription, error) {
	rows, err := s.db.Query(`SELECT `+subscriptionColumns+` FROM dav_subscriptions
		WHERE collection_id IN (SELECT id FROM dav_collections WHERE owner_user_id = ?)`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()
	out := map[CollectionID]Subscription{}
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		out[sub.Collection] = *sub
	}
	return out, rows.Err()
}

// Subscription returns one collection's subscription, or ErrNotFound.
func (s *Store) Subscription(id CollectionID) (*Subscription, error) {
	sub, err := scanSubscription(s.db.QueryRow(`SELECT `+subscriptionColumns+` FROM dav_subscriptions WHERE collection_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription %d: %w", id, err)
	}
	return sub, nil
}

// due returns subscriptions whose next poll is at or before now.
func (s *Store) due(now time.Time, limit int) ([]Subscription, error) {
	rows, err := s.db.Query(`SELECT `+subscriptionColumns+` FROM dav_subscriptions
		WHERE next_poll_at <= ? ORDER BY next_poll_at LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("due subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		out = append(out, *sub)
	}
	return out, rows.Err()
}

// claim moves a due subscription's next poll to until, unless another
// poller moved it first, and reports whether it did.
func (s *Store) claim(sub *Subscription, until time.Time) (bool, error) {
	res, err := s.db.Exec(`UPDATE dav_subscriptions SET next_poll_at = ? WHERE collection_id = ? AND next_poll_at = ?`,
		until.Unix(), sub.Collection, sub.NextPoll.Unix())
	if err != nil {
		return false, fmt.Errorf("claim subscription %d: %w", sub.Collection, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim subscription %d: %w", sub.Collection, err)
	}
	return n == 1, nil
}

// record saves the outcome of a poll.
func (s *Store) record(sub *Subscription) error {
	_, err := s.db.Exec(`UPDATE dav_subscriptions SET etag = ?, last_modified = ?, refresh = ?, next_poll_at = ?,
		last_poll_at = ?, last_success_at = ?, failures = ?, last_error = ?, skipped = ? WHERE collection_id = ?`,
		sub.ETag, sub.LastModified, int64(sub.Refresh/time.Second), sub.NextPoll.Unix(), sub.LastPoll.Unix(),
		unixOrZero(sub.LastSuccess), sub.Failures, sub.LastError, sub.Skipped, sub.Collection)
	if err != nil {
		return fmt.Errorf("record poll of subscription %d: %w", sub.Collection, err)
	}
	return nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// NormalizeFeedURL returns the URL a subscription is fetched from: webcal:// and
// webcals:// are https://, as every client reads them. Anything that is not
// an absolute http(s) URL is refused.
func NormalizeFeedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("not a URL: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "webcal", "webcals", "https":
		u.Scheme = "https"
	case "http":
		u.Scheme = "http"
	default:
		return nil, errors.New("a feed URL starts with webcal://, https:// or http://")
	}
	if u.Host == "" {
		return nil, errors.New("the feed URL has no host")
	}
	if u.User != nil {
		return nil, errors.New("the feed URL may not hold a user name or password")
	}
	return u, nil
}

// Subscribe creates a subscription calendar for owner filled from the feed
// at rawURL, and fetches it. When that first fetch fails, nothing is kept and
// the error says why.
func (s *Server) Subscribe(ctx context.Context, owner *User, rawURL, displayName, color string) (*Collection, *PollResult, error) {
	if _, err := NormalizeFeedURL(rawURL); err != nil {
		return nil, nil, &FeedError{err}
	}
	if s.eventBackend() == nil {
		return nil, nil, errors.New("no backend stores events")
	}
	if displayName == "" {
		displayName = "Subscription"
	}
	c := &Collection{
		OwnerID: owner.ID, Home: HomeCalendars, Kind: KindSubscription, Name: Slug(displayName, "subscription"),
		Components: []Component{VEVENT}, DisplayName: displayName, Color: color,
	}
	if err := s.Collections.CreateNamed(c); err != nil {
		return nil, nil, err
	}
	now := s.Clock()
	if _, err := s.Collections.db.Exec(`INSERT INTO dav_subscriptions (collection_id, url, next_poll_at) VALUES (?, ?, ?)`,
		c.ID, strings.TrimSpace(rawURL), now.Unix()); err != nil {
		return nil, nil, errors.Join(fmt.Errorf("create subscription: %w", err), s.DeleteCollection(owner.Username, c))
	}
	c.Source = strings.TrimSpace(rawURL)
	res, err := s.Refresh(ctx, owner.Username, c)
	if err != nil {
		return nil, nil, errors.Join(err, s.DeleteCollection(owner.Username, c))
	}
	return c, res, nil
}

// PollResult says what one fetch of a feed changed.
type PollResult struct {
	NotModified bool // the feed answered 304
	Added       int
	Updated     int
	Removed     int
	Unchanged   int
	Skipped     int // components a subscription does not hold
}

// Refresh fetches a subscription's feed now and stores what changed. The
// outcome, failure included, is recorded on the subscription; a failure is
// also returned.
func (s *Server) Refresh(ctx context.Context, ownerUsername string, c *Collection) (*PollResult, error) {
	sub, err := s.Collections.Subscription(c.ID)
	if err != nil {
		return nil, err
	}
	return s.poll(ctx, ownerUsername, c, sub)
}

// PollDue fetches the subscriptions that are due, across every user. Each is
// claimed first, so replicas sharing the system database do not fetch the
// same feed at once. A feed that fails is recorded and retried later; only
// errors of Cog's own (the database) are returned.
func (s *Server) PollDue(ctx context.Context) error {
	now := s.Clock()
	due, err := s.Collections.due(now, pollBatch)
	if err != nil {
		return err
	}
	var errs []error
	for i := range due {
		if ctx.Err() != nil {
			break
		}
		sub := &due[i]
		ok, err := s.Collections.claim(sub, now.Add(pollLease))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		c, err := s.Collections.ByID(sub.Collection)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		owner, err := s.username(c.OwnerID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.poll(ctx, owner, c, sub); err != nil && !isFeedError(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// FeedError is a feed that could not be fetched or read, or a URL that is
// not a feed's: the user's or the remote's fault, recorded on the
// subscription and shown to the user, rather than Cog's.
type FeedError struct{ Err error }

func (e *FeedError) Error() string { return e.Err.Error() }
func (e *FeedError) Unwrap() error { return e.Err }

func isFeedError(err error) bool {
	var fe *FeedError
	return errors.As(err, &fe)
}

func (s *Server) fetchClient() *http.Client {
	if s.Fetch != nil {
		return s.Fetch
	}
	s.fetchOnce.Do(func() { s.fetch = safehttp.Client(feedTimeout) })
	return s.fetch
}

// eventBackend returns the enabled backend that stores events.
func (s *Server) eventBackend() Backend {
	for _, b := range s.Backends() {
		if slices.Contains(b.Components(), VEVENT) {
			return b
		}
	}
	return nil
}

// poll fetches the feed and records the outcome on sub.
func (s *Server) poll(ctx context.Context, owner string, c *Collection, sub *Subscription) (*PollResult, error) {
	res, err := s.fetchAndStore(ctx, owner, c, sub)
	now := s.Clock()
	sub.LastPoll = now
	refresh := sub.Refresh
	if refresh == 0 {
		refresh = defaultRefresh
	}
	if err != nil && isFeedError(err) {
		sub.Failures++
		sub.LastError = err.Error()
		backoff := refresh << min(sub.Failures-1, 6)
		sub.NextPoll = now.Add(min(max(backoff, minRefresh), maxBackoff))
		s.Log.Warn("dav: subscription fetch failed", "collection", c.ID, "failures", sub.Failures, "err", err)
		return nil, errors.Join(err, s.Collections.record(sub))
	}
	if err != nil {
		// Cog's own failure: try again soon, and say nothing against the
		// feed.
		sub.NextPoll = now.Add(minRefresh)
		return nil, errors.Join(err, s.Collections.record(sub))
	}
	sub.Failures, sub.LastError, sub.LastSuccess = 0, "", now
	sub.NextPoll = now.Add(refresh)
	if !res.NotModified {
		sub.Skipped = res.Skipped
	}
	return res, s.Collections.record(sub)
}

// fetchAndStore fetches the feed (conditionally, with the last response's
// validators) and makes the collection hold exactly its events: new and
// changed ones stored, those gone from the feed deleted, with tombstones so
// clients see them go. An event whose text is unchanged is not rewritten,
// so its ETag stays and clients fetch nothing.
func (s *Server) fetchAndStore(ctx context.Context, owner string, c *Collection, sub *Subscription) (*PollResult, error) {
	u, err := NormalizeFeedURL(sub.URL)
	if err != nil {
		return nil, &FeedError{err}
	}
	be := s.eventBackend()
	if be == nil {
		return nil, errors.New("no backend stores events")
	}
	body, notModified, validators, err := s.fetchFeed(ctx, u, sub)
	if err != nil {
		return nil, &FeedError{err}
	}
	if notModified {
		return &PollResult{NotModified: true}, nil
	}
	comps, err := contentline.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, &FeedError{fmt.Errorf("not an iCalendar feed: %w", err)}
	}
	res := &PollResult{}
	want := map[string]*PutRequest{}
	var order []string
	sawCalendar := false
	for _, top := range comps {
		if !top.Is("VCALENDAR") {
			res.Skipped++
			continue
		}
		sawCalendar = true
		if d, ok := feedRefresh(top); ok {
			sub.Refresh = d
		}
		for _, obj := range splitCalendar(top) {
			last := obj.Components[len(obj.Components)-1]
			if !last.Is("VEVENT") {
				res.Skipped++
				continue
			}
			uid, err := ensureUID(obj)
			if err != nil {
				return nil, err
			}
			uri := feedURI(uid)
			if _, dup := want[uri]; dup {
				res.Skipped++
				continue
			}
			stripManaged(obj)
			stripAlarms(obj)
			o, err := ical.Decode(obj, ical.VEVENT)
			if err != nil {
				res.Skipped++
				continue
			}
			o.SortMaster()
			cal, data, err := encodeObject(o)
			if err != nil {
				return nil, err
			}
			want[uri] = &PutRequest{URI: uri, UID: o.UID, Component: VEVENT, Data: data, Object: cal}
			order = append(order, uri)
		}
	}
	if !sawCalendar {
		return nil, &FeedError{errors.New("not an iCalendar feed: no VCALENDAR")}
	}
	if c.Timezone == "" {
		// Floating times read in the feed's zone, when it names one
		// (Google's X-WR-TIMEZONE) and the user has not chosen another.
		for _, top := range comps {
			if p := top.Prop("X-WR-TIMEZONE"); top.Is("VCALENDAR") && p != nil {
				if loc := tz.Resolve(p.Value, nil); loc != nil {
					c.Timezone = loc.String()
					if err := s.Collections.Update(c); err != nil {
						return nil, err
					}
					if o, ok := be.(TimezoneObserver); ok {
						if err := o.TimezoneChanged(owner, c); err != nil {
							return nil, err
						}
					}
				}
				break
			}
		}
	}
	if len(body) > 0 && len(want) == 0 && res.Skipped > 0 {
		// Everything was something a subscription cannot hold; storing
		// nothing would delete every event. Keep what there is.
		return nil, &FeedError{fmt.Errorf("the feed has no events Cog can read (%d components skipped)", res.Skipped)}
	}
	have, err := be.List(owner, c, true)
	if err != nil {
		return nil, err
	}
	var puts []*PutRequest
	var deletes []string
	for _, r := range have {
		w, ok := want[r.URI]
		switch {
		case !ok:
			deletes = append(deletes, r.URI)
			res.Removed++
		case bytes.Equal(w.Data, r.Data):
			delete(want, r.URI)
			res.Unchanged++
		default:
			res.Updated++
		}
	}
	for _, uri := range order {
		if w, ok := want[uri]; ok {
			puts = append(puts, w)
		}
	}
	res.Added = len(puts) - res.Updated
	if err := writeBatch(be, owner, c, puts, deletes); err != nil {
		return nil, err
	}
	// Only now that the events are stored may the next fetch be
	// conditional on this response.
	sub.ETag, sub.LastModified = validators.etag, validators.lastModified
	return res, nil
}

// validators are a response's ETag and Last-Modified.
type validators struct{ etag, lastModified string }

// fetchFeed GETs the feed, conditionally on the last response's validators,
// and returns the new ones.
func (s *Server) fetchFeed(ctx context.Context, u *url.URL, sub *Subscription) ([]byte, bool, validators, error) {
	ctx, cancel := context.WithTimeout(ctx, feedTimeout)
	defer cancel()
	rq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, false, validators{}, err
	}
	rq.Header.Set("User-Agent", "Cog (calendar subscription)")
	rq.Header.Set("Accept", "text/calendar, */*;q=0.5")
	if sub.ETag != "" {
		rq.Header.Set("If-None-Match", sub.ETag)
	}
	if sub.LastModified != "" {
		rq.Header.Set("If-Modified-Since", sub.LastModified)
	}
	resp, err := s.fetchClient().Do(rq)
	if err != nil {
		// The URL itself may be secret (a private Google feed): say why
		// without it.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, false, validators{}, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil, true, validators{}, nil
	case resp.StatusCode != http.StatusOK:
		return nil, false, validators{}, fmt.Errorf("the feed answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedSize+1))
	if err != nil {
		return nil, false, validators{}, fmt.Errorf("read the feed: %w", err)
	}
	if len(body) > maxFeedSize {
		return nil, false, validators{}, fmt.Errorf("the feed is larger than %d MB", maxFeedSize>>20)
	}
	return body, false, validators{resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")}, nil
}

// feedRefresh reads how often a feed asks to be fetched: REFRESH-INTERVAL
// (RFC 7986) or X-PUBLISHED-TTL, within bounds.
func feedRefresh(cal *contentline.Component) (time.Duration, bool) {
	for _, name := range []string{"REFRESH-INTERVAL", "X-PUBLISHED-TTL"} {
		p := cal.Prop(name)
		if p == nil {
			continue
		}
		d, err := ical.ParseDuration(p.Value)
		if err != nil || d.Negative {
			continue
		}
		total := time.Duration(d.Days)*24*time.Hour + d.Clock
		return min(max(total, minRefresh), maxRefresh), true
	}
	return 0, false
}

// stripAlarms removes a feed's VALARMs: a feed's author does not decide
// when its subscribers are reminded (Apple Calendar removes them from
// subscriptions too), and Cog's reminders would otherwise fire for every
// match of a team's season.
func stripAlarms(obj *contentline.Component) {
	for _, sub := range obj.Components {
		sub.Components = slices.DeleteFunc(sub.Components, func(c *contentline.Component) bool { return c.Is("VALARM") })
	}
}

// ensureUID returns the UID of a calendar object from splitCalendar, and
// gives it one when the feed sent none: a hash of its text, so the same
// event gets the same UID on every fetch.
func ensureUID(obj *contentline.Component) (string, error) {
	last := obj.Components[len(obj.Components)-1]
	if p := last.Prop("UID"); p != nil && p.Value != "" {
		return p.Value, nil
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, obj.Components); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	uid := "cog-feed-" + hex.EncodeToString(sum[:16])
	for _, sub := range obj.Components {
		if !sub.Is("VTIMEZONE") {
			sub.Props = append(sub.Props, contentline.Property{Name: "UID", Value: uid})
		}
	}
	return uid, nil
}

// feedURI names a feed's event after its UID, or a hash of it when the UID
// is not safe in a path: the same name on every fetch.
func feedURI(uid string) string {
	if safeURI.MatchString(uid) {
		return uid + ".ics"
	}
	sum := sha256.Sum256([]byte(uid))
	return hex.EncodeToString(sum[:16]) + ".ics"
}
