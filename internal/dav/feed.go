package dav

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/plugin"
)

// ICS feeds: a secret, revocable URL that serves a whole calendar as one
// iCalendar file, for subscribers that poll it (webcal) rather than speak
// CalDAV. DESIGN/dav-storage.md section 9.1. A public link to an address
// book serves its cards the same way, as one vCard file.
//
// The secret is a public link share of the collection (Links): the share
// table already keeps revocable tokens, the share panel lists and revokes
// them like any other share, and its permission bits say which feed it is.
// A link with Read serves the calendar; one with FreeBusy only serves busy
// times, as a free/busy-only share sees them. Revoking deletes the share, and
// every request looks the token up, so a revoked feed stops at once.

// Link is a secret link to a collection.
type Link struct {
	ID          string
	OwnerID     string
	Collection  CollectionID
	Token       string
	Permissions uint32
}

// BusyOnly reports whether the link serves only busy times.
func (l *Link) BusyOnly() bool { return l.Permissions&plugin.PermRead == 0 }

// Links keeps collections' secret links.
type Links interface {
	CreateLink(ownerID string, id CollectionID, permissions uint32) (*Link, error)
	// LinkByToken returns the link with this token, or ErrNotFound when
	// there is none or it has expired.
	LinkByToken(token string) (*Link, error)
	// LinksTo returns the links to a collection, made by anyone.
	LinksTo(id CollectionID) ([]Link, error)
	// LinksBy returns the links a user made to collections.
	LinksBy(ownerID string) ([]Link, error)
	DeleteLink(id string) error
}

const feedsPath = "feeds"

// FeedURL is where the feed of a link's token to a collection in home is
// served: an .ics file for a calendar, a .vcf one for an address book.
func (s *Server) FeedURL(token string, home Home) string {
	ext := ".ics"
	if home == HomeAddressBooks {
		ext = ".vcf"
	}
	return strings.TrimSuffix(s.BaseURL, "/") + Root + feedsPath + "/" + token + ext
}

// Feed is a collection's feeds: the one serving it and the one serving
// busy times, either nil when there is none.
type Feed struct {
	Full, Busy *Link
}

// FeedsOf returns the feeds of a user's collections, in one query.
func (s *Server) FeedsOf(ownerID string) (map[CollectionID]Feed, error) {
	out := map[CollectionID]Feed{}
	if s.Links == nil {
		return out, nil
	}
	links, err := s.Links.LinksBy(ownerID)
	if err != nil {
		return nil, err
	}
	for i := range links {
		l := &links[i]
		f := out[l.Collection]
		switch {
		case !l.BusyOnly() && f.Full == nil:
			f.Full = l
		case l.BusyOnly() && l.Permissions&plugin.PermFreeBusy != 0 && f.Busy == nil:
			f.Busy = l
		}
		out[l.Collection] = f
	}
	return out, nil
}

// ErrNoLinks is returned when the server has no link store.
var ErrNoLinks = errors.New("dav: feeds are not available")

// RenewFeed replaces the owner's feed of c (the busy-times one when busyOnly)
// with a new secret, or creates it: the old URL stops working at once.
func (s *Server) RenewFeed(c *Collection, busyOnly bool) (*Link, error) {
	if s.Links == nil {
		return nil, ErrNoLinks
	}
	if err := s.RevokeFeed(c, busyOnly); err != nil {
		return nil, err
	}
	perms := plugin.PermRead
	if busyOnly {
		perms = plugin.PermFreeBusy
	}
	return s.Links.CreateLink(c.OwnerID, c.ID, perms)
}

// RevokeFeed deletes the owner's feeds of c of one kind; there may be more
// than one, made from the share panel.
func (s *Server) RevokeFeed(c *Collection, busyOnly bool) error {
	if s.Links == nil {
		return ErrNoLinks
	}
	links, err := s.Links.LinksTo(c.ID)
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.OwnerID == c.OwnerID && l.BusyOnly() == busyOnly {
			if err := s.Links.DeleteLink(l.ID); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

// feedAttachmentToken lets a feed's subscribers fetch the owner's managed
// attachment id: it is bound to the feed's secret, so revoking the feed
// revokes it, and accepted only while an event of the feed's calendar
// carries the attachment.
func (s *Server) feedAttachmentToken(owner, id, feedToken string) string {
	mac := hmac.New(sha256.New, s.Secret)
	fmt.Fprintf(mac, "feed\x00%s\x00%s\x00%s", owner, id, feedToken)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// feedMayFetch reports whether a feed token lets its holder fetch the
// owner's attachment id, carried by uses.
func (s *Server) feedMayFetch(owner *User, id, token string, uses []AttachmentUse) (bool, error) {
	if s.Links == nil || token == "" {
		return false, nil
	}
	seen := map[CollectionID]bool{}
	for _, u := range uses {
		if seen[u.Collection] {
			continue
		}
		seen[u.Collection] = true
		links, err := s.Links.LinksTo(u.Collection)
		if err != nil {
			return false, err
		}
		for _, l := range links {
			if l.OwnerID != owner.ID || l.BusyOnly() {
				continue
			}
			if hmac.Equal([]byte(token), []byte(s.feedAttachmentToken(owner.Username, id, l.Token))) {
				return true, nil
			}
		}
	}
	return false, nil
}

// state returns a collection's sync token over bes and when it last
// changed: a write to a resource or to the collection's properties.
func (s *Server) state(owner string, c *Collection, bes []Backend) (SyncToken, time.Time, error) {
	tok := SyncToken{}
	modified := c.UpdatedAt
	for _, b := range bes {
		states, err := b.States(owner, []CollectionID{c.ID})
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("sync state from %s: %w", b.Key(), err)
		}
		st := states[c.ID]
		tok[b.Key()] = st.Token
		modified = max(modified, st.Modified)
	}
	return tok, time.Unix(modified, 0).UTC(), nil
}

// serveFeed answers a GET for /feeds/<token>.ics. The ETag is the
// collection's ctag, so a subscriber polling an unchanged calendar gets a 304
// without anything being rendered.
func (s *Server) serveFeed(w http.ResponseWriter, r *http.Request, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ext, _ := strings.Cut(rest, ".")
	fail := func(err error) {
		s.Log.Error("dav: feed", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
	if s.Links == nil || token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	link, err := s.Links.LinkByToken(token)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	c, err := s.Collections.ByID(link.Collection)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(err)
		return
	}
	// Like any share, a link counts only when the collection's owner made
	// it.
	wantExt := "ics"
	if c.Home == HomeAddressBooks {
		// Busy times mean nothing for cards.
		wantExt = "vcf"
		if link.BusyOnly() {
			http.NotFound(w, r)
			return
		}
	}
	if c.OwnerID != link.OwnerID || ext != wantExt ||
		link.Permissions&(plugin.PermRead|plugin.PermFreeBusy) == 0 {
		http.NotFound(w, r)
		return
	}
	owner, err := s.username(c.OwnerID)
	if err != nil {
		fail(err)
		return
	}
	bes := s.backendsOf(c)
	tok, modified, err := s.state(owner, c, bes)
	if err != nil {
		fail(err)
		return
	}
	etag := `"` + ctag(tok, c.PropsVersion) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", modified.Format(http.TimeFormat))
	h.Set("Cache-Control", "private, no-cache")
	h.Set("Content-Type", "text/calendar; charset=utf-8")
	if c.Home == HomeAddressBooks {
		h.Set("Content-Type", "text/vcard; charset=utf-8")
	}
	h.Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": c.Name + "." + ext}))
	if notModified(r, etag, modified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	opt := exportOptions{busyOnly: link.BusyOnly()}
	if !opt.busyOnly {
		opt.attachment = func(id string) string {
			return s.AttachmentURL(owner, id) + "?feed=" + s.feedAttachmentToken(owner, id, link.Token)
		}
	}
	var b bytes.Buffer
	if err := s.export(&b, owner, c, bes, opt); err != nil {
		fail(err)
		return
	}
	h.Set("Content-Length", strconv.Itoa(b.Len()))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(b.Bytes())
	}
}

// notModified evaluates If-None-Match, or If-Modified-Since when there is
// none (RFC 9110 section 13.2.2).
func notModified(r *http.Request, etag string, modified time.Time) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for t := range strings.SplitSeq(inm, ",") {
			if t = strings.TrimSpace(t); t == "*" || strings.TrimPrefix(t, "W/") == etag {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := http.ParseTime(ims); err == nil && !modified.Truncate(time.Second).After(t) {
			return true
		}
	}
	return false
}
