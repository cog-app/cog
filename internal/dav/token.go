package dav

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// ETag returns the entity tag of a resource at version in collection c,
// quotes included. It is opaque to clients; Cog never hashes content.
func ETag(c CollectionID, version int64) string {
	return `"` + c.String() + "-" + strconv.FormatInt(version, 10) + `"`
}

// parseETag returns the version an entity tag names, and whether it belongs
// to collection c. Weak tags never match: every Cog tag is strong.
func parseETag(tag string, c CollectionID) (int64, bool) {
	tag = strings.TrimSpace(tag)
	if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) || len(tag) < 2 {
		return 0, false
	}
	coll, ver, ok := strings.Cut(tag[1:len(tag)-1], "-")
	if !ok || coll != c.String() {
		return 0, false
	}
	v, err := strconv.ParseInt(ver, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// syncTokenPrefix starts every sync token. Tokens are URIs (RFC 6578 §6.2).
const syncTokenPrefix = "http://cog/ns/sync/v2"

// SyncToken is a collection's composite sync token: one counter per backend,
// keyed by Backend.Key. A backend absent from a token is at 0.
type SyncToken map[string]int64

// String formats the token as a URI, backends in key order so the same state
// always gives the same token.
func (t SyncToken) String() string {
	var b strings.Builder
	b.WriteString(syncTokenPrefix)
	for _, k := range slices.Sorted(maps.Keys(t)) {
		fmt.Fprintf(&b, "-%s%d", k, t[k])
	}
	return b.String()
}

// ParseSyncToken parses a token from String. An empty string is the initial
// sync, an empty token.
func ParseSyncToken(s string) (SyncToken, error) {
	s = strings.TrimSpace(s)
	t := SyncToken{}
	if s == "" {
		return t, nil
	}
	rest, ok := strings.CutPrefix(s, syncTokenPrefix)
	if !ok {
		return nil, fmt.Errorf("unknown sync token %q", s)
	}
	if rest == "" {
		return t, nil
	}
	if rest[0] != '-' {
		return nil, fmt.Errorf("malformed sync token %q", s)
	}
	for part := range strings.SplitSeq(rest[1:], "-") {
		i := strings.IndexFunc(part, func(r rune) bool { return r >= '0' && r <= '9' })
		if i <= 0 {
			return nil, fmt.Errorf("malformed sync token %q", s)
		}
		v, err := strconv.ParseInt(part[i:], 10, 64)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("malformed sync token %q", s)
		}
		if _, dup := t[part[:i]]; dup {
			return nil, fmt.Errorf("malformed sync token %q", s)
		}
		t[part[:i]] = v
	}
	return t, nil
}

// ctag is the CalendarServer getctag: the sync token plus the properties
// version, so clients that poll it also notice PROPPATCH changes.
func ctag(t SyncToken, propsVersion int64) string {
	return t.String() + "-p" + strconv.FormatInt(propsVersion, 10)
}
