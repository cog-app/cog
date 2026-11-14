package compat

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// maxLockBody bounds a LOCK request's lockinfo.
const maxLockBody = 64 << 10

// lockInfo is a LOCK request body (RFC 4918 §14.11).
type lockInfo struct {
	XMLName   xml.Name `xml:"DAV: lockinfo"`
	LockScope struct {
		Exclusive *struct{} `xml:"DAV: exclusive"`
		Shared    *struct{} `xml:"DAV: shared"`
	} `xml:"DAV: lockscope"`
	LockType struct {
		Write *struct{} `xml:"DAV: write"`
	} `xml:"DAV: locktype"`
	Owner *ownerXML `xml:"DAV: owner"`
}

// ownerXML is the content of a lockinfo's DAV:owner, re-encoded so it
// carries its own namespace declarations: the client may have bound its
// prefixes on the request's root, which is not there when the owner is given
// back. It is decoded within the request, so those prefixes resolve.
type ownerXML struct{ XML string }

func (o *ownerXML) UnmarshalXML(d *xml.Decoder, _ xml.StartElement) error {
	var b strings.Builder
	enc := xml.NewEncoder(&b)
	for depth := 0; ; {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			// Declarations come from the names; copying the decoded
			// xmlns attributes too would declare them twice.
			attrs := make([]xml.Attr, 0, len(t.Attr))
			for _, a := range t.Attr {
				if a.Name.Space != "xmlns" && a.Name.Local != "xmlns" {
					attrs = append(attrs, a)
				}
			}
			t.Attr = attrs
			tok = t
		case xml.EndElement:
			if depth == 0 {
				if err := enc.Flush(); err != nil {
					return err
				}
				o.XML = b.String()
				return nil
			}
			depth--
		case xml.ProcInst, xml.Directive:
			continue
		}
		if err := enc.EncodeToken(xml.CopyToken(tok)); err != nil {
			return err
		}
	}
}

// handleLock takes a lock, or with no body refreshes the one whose token the
// If header names. Locking a name with nothing there creates an empty file,
// as RFC 4918 §7.3 says: editors lock a file before they first save it.
func (h *WebDAVHandler) handleLock(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	hrefPrefix := "/remote.php/dav/files/" + urlUserID
	timeout := lockTimeout(r.Header.Get("Timeout"))

	body, err := io.ReadAll(io.LimitReader(r.Body, maxLockBody+1))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxLockBody {
		http.Error(w, "lockinfo too large", http.StatusRequestEntityTooLarge)
		return
	}
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}

	if len(bytes.TrimSpace(body)) == 0 {
		h.refreshLock(w, r, u, c, hrefPrefix, filePath, timeout)
		return
	}

	var info lockInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		http.Error(w, "parse lockinfo", http.StatusBadRequest)
		return
	}
	if info.LockType.Write == nil || (info.LockScope.Exclusive == nil) == (info.LockScope.Shared == nil) {
		http.Error(w, "a lock is an exclusive or a shared write lock", http.StatusBadRequest)
		return
	}
	infinite := true
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Depth"))) {
	case "", "infinity":
	case "0":
		infinite = false
	default:
		http.Error(w, "Depth must be 0 or infinity", http.StatusBadRequest)
		return
	}
	owner := ""
	if info.Owner != nil {
		owner = info.Owner.XML
	}

	cur, err := h.Files.GetByPath(u.Username, filePath)
	if err != nil && !errors.Is(err, files.ErrNotFound) {
		h.writeError(w, r, u, urlUserID, "LOCK", err)
		return
	}
	if err != nil {
		cur = nil
	}
	if err := c.check(filePath, cur, lockScope{member: cur == nil, beneath: infinite}); err != nil {
		h.writeError(w, r, u, urlUserID, "LOCK", err)
		return
	}
	if cur == nil {
		// The empty file joins a folder, which may be locked itself; and
		// there must be a folder for it to join.
		if err := c.requireTokens(filePath, lockScope{member: true}); err != nil {
			h.writeError(w, r, u, urlUserID, "LOCK", err)
			return
		}
		parent, err := h.Files.GetByPath(u.Username, path.Dir(filePath))
		if errors.Is(err, files.ErrNotFound) || (err == nil && !parent.IsDir) {
			http.Error(w, "no folder to create the file in", http.StatusConflict)
			return
		}
		if err != nil {
			h.writeError(w, r, u, urlUserID, "LOCK", err)
			return
		}
	}

	l, err := h.Files.Lock(u.Username, files.DAVLock{
		Path:     filePath,
		Infinite: infinite,
		Shared:   info.LockScope.Shared != nil,
		Owner:    owner,
		Timeout:  timeout,
	})
	if errors.Is(err, files.ErrLocked) {
		writeCondError(w, hrefPrefix, &lockedError{Roots: []string{filePath}})
		return
	}
	if err != nil {
		h.writeError(w, r, u, urlUserID, "LOCK", err)
		return
	}

	status := http.StatusOK
	if cur == nil {
		// Only if nothing appeared since: the lock is taken, so a write
		// that came first is the file to keep.
		_, err := h.Files.UploadIf(u.Username, filePath, strings.NewReader(""), func(cur *files.FileMeta) error {
			if cur != nil {
				return errFileAppeared
			}
			return nil
		})
		switch {
		case err == nil:
			status = http.StatusCreated
		case errors.Is(err, errFileAppeared):
		default:
			if uerr := h.Files.Unlock(u.Username, filePath, l.Token); uerr != nil {
				err = errors.Join(err, uerr)
			}
			h.writeError(w, r, u, urlUserID, "LOCK: create empty file", err)
			return
		}
	}
	w.Header().Set("Lock-Token", "<"+l.Token+">")
	writeLockDiscovery(w, status, hrefPrefix, []files.DAVLock{*l}, h.Clock())
}

var errFileAppeared = errors.New("webdav: a file appeared while it was being locked")

// refreshLock restarts the timeout of the lock the If header names.
func (h *WebDAVHandler) refreshLock(w http.ResponseWriter, r *http.Request, u *user.User, c *davConditions, hrefPrefix, filePath string, timeout time.Duration) {
	token := ""
	for _, l := range c.lists {
		for _, cond := range l.conds {
			if cond.token != "" && !cond.not {
				token = cond.token
			}
		}
	}
	if token == "" {
		http.Error(w, "a lock refresh names its lock in the If header", http.StatusBadRequest)
		return
	}
	l, err := h.Files.RefreshLock(u.Username, filePath, token, timeout)
	if errors.Is(err, files.ErrNoLock) {
		http.Error(w, "no such lock", http.StatusPreconditionFailed)
		return
	}
	if err != nil {
		h.Log.Error("LOCK refresh", "user", u.Username, "path", filePath, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	writeLockDiscovery(w, http.StatusOK, hrefPrefix, []files.DAVLock{*l}, h.Clock())
}

// handleUnlock removes the lock the Lock-Token header names.
func (h *WebDAVHandler) handleUnlock(w http.ResponseWriter, r *http.Request, u *user.User, _ string, filePath string) {
	token := strings.TrimSpace(r.Header.Get("Lock-Token"))
	token = strings.TrimSuffix(strings.TrimPrefix(token, "<"), ">")
	if token == "" {
		http.Error(w, "Lock-Token header required", http.StatusBadRequest)
		return
	}
	err := h.Files.Unlock(u.Username, filePath, token)
	if errors.Is(err, files.ErrNoLock) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<d:error xmlns:d="DAV:"><d:lock-token-matches-request-uri/></d:error>`+"\n")
		return
	}
	if err != nil {
		h.Log.Error("UNLOCK", "user", u.Username, "path", filePath, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lockTimeout reads a Timeout header: the first "Second-N" it offers, or
// "Infinite", which the files plugin bounds. Zero is the default.
func lockTimeout(h string) time.Duration {
	for t := range strings.SplitSeq(h, ",") {
		t = strings.TrimSpace(t)
		if strings.EqualFold(t, "Infinite") {
			return files.MaxLockTimeout
		}
		if n, ok := strings.CutPrefix(t, "Second-"); ok {
			if secs, err := strconv.ParseInt(n, 10, 64); err == nil && secs > 0 {
				return time.Duration(min(secs, int64(files.MaxLockTimeout/time.Second))) * time.Second
			}
		}
	}
	return 0
}

// activeLock is a lock as DAV:lockdiscovery describes it.
type activeLock struct {
	LockType  lockTypeXML  `xml:"D:locktype"`
	LockScope lockScopeXML `xml:"D:lockscope"`
	Depth     string       `xml:"D:depth"`
	Owner     *innerXML    `xml:"D:owner,omitempty"`
	Timeout   string       `xml:"D:timeout"`
	LockToken hrefXML      `xml:"D:locktoken"`
	LockRoot  hrefXML      `xml:"D:lockroot"`
}

type lockTypeXML struct {
	Write struct{} `xml:"D:write"`
}

type lockScopeXML struct {
	Exclusive *struct{} `xml:"D:exclusive,omitempty"`
	Shared    *struct{} `xml:"D:shared,omitempty"`
}

type hrefXML struct {
	Href string `xml:"D:href"`
}

type innerXML struct {
	Inner string `xml:",innerxml"`
}

// lockDiscovery is the DAV:lockdiscovery property.
type lockDiscovery struct {
	Locks []activeLock `xml:"D:activelock"`
}

// supportedLock is the DAV:supportedlock property: exclusive and shared write
// locks, on everything.
type supportedLock struct {
	Entries []lockEntry `xml:"D:lockentry"`
}

type lockEntry struct {
	LockScope lockScopeXML `xml:"D:lockscope"`
	LockType  lockTypeXML  `xml:"D:locktype"`
}

var supportedLocks = &supportedLock{Entries: []lockEntry{
	{LockScope: lockScopeXML{Exclusive: &struct{}{}}},
	{LockScope: lockScopeXML{Shared: &struct{}{}}},
}}

func toActiveLock(hrefPrefix string, l *files.DAVLock, now time.Time) activeLock {
	a := activeLock{
		Depth:     "0",
		Timeout:   "Second-" + strconv.FormatInt(int64(max(l.Expires.Sub(now), 0)/time.Second), 10),
		LockToken: hrefXML{l.Token},
		LockRoot:  hrefXML{davHref(hrefPrefix, l.Path, false)},
	}
	if l.Infinite {
		a.Depth = "infinity"
	}
	if l.Shared {
		a.LockScope.Shared = &struct{}{}
	} else {
		a.LockScope.Exclusive = &struct{}{}
	}
	if l.Owner != "" {
		a.Owner = &innerXML{l.Owner}
	}
	return a
}

// lockDiscoveryFor is the lockdiscovery of p: the locks among locks that
// cover it.
func lockDiscoveryFor(hrefPrefix, p string, locks []files.DAVLock, now time.Time) *lockDiscovery {
	d := &lockDiscovery{}
	for i := range locks {
		if locks[i].Covers(p) {
			d.Locks = append(d.Locks, toActiveLock(hrefPrefix, &locks[i], now))
		}
	}
	return d
}

// writeLockDiscovery answers a LOCK with the lock's DAV:lockdiscovery.
func writeLockDiscovery(w http.ResponseWriter, status int, hrefPrefix string, locks []files.DAVLock, now time.Time) {
	type propXML struct {
		XMLName       xml.Name      `xml:"D:prop"`
		DAV           string        `xml:"xmlns:D,attr"`
		LockDiscovery lockDiscovery `xml:"D:lockdiscovery"`
	}
	out := propXML{DAV: "DAV:"}
	for i := range locks {
		out.LockDiscovery.Locks = append(out.LockDiscovery.Locks, toActiveLock(hrefPrefix, &locks[i], now))
	}
	var b bytes.Buffer
	b.WriteString(xml.Header)
	if err := xml.NewEncoder(&b).Encode(out); err != nil {
		http.Error(w, fmt.Sprintf("encode lock: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	w.Write(b.Bytes())
}
