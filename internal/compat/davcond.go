package compat

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/files"
)

// davCompliance is the DAV header for the files tree: class 2 is locking,
// which Finder and Windows look for before they mount a share writable.
const davCompliance = "1, 2, 3, extended-mkcol"

var (
	errPreconditionFailed = errors.New("webdav: precondition failed")
	errBadIfHeader        = errors.New("webdav: malformed If header")
)

// lockedError is a change to something locked without the lock's token. Roots
// are the paths of the locks whose tokens were missing.
type lockedError struct{ Roots []string }

func (e *lockedError) Error() string {
	return "webdav: locked: " + strings.Join(e.Roots, ", ")
}

// ifCondition is one condition of an If header list: a lock token or an
// entity tag, possibly negated.
type ifCondition struct {
	not   bool
	token string
	etag  string
}

// ifList is one parenthesised list of an If header (RFC 4918 §10.4), and the
// resource it is tagged with, "" for the request's own.
type ifList struct {
	resource string
	conds    []ifCondition
}

// parseIf parses an If header.
func parseIf(h string) ([]ifList, error) {
	var lists []ifList
	resource := ""
	s := strings.TrimSpace(h)
	for s != "" {
		switch s[0] {
		case '<':
			end := strings.IndexByte(s, '>')
			if end < 0 {
				return nil, errBadIfHeader
			}
			resource = s[1:end]
			if resource == "" {
				return nil, errBadIfHeader
			}
			s = s[end+1:]
		case '(':
			l := ifList{resource: resource}
			s = s[1:]
			for {
				s = strings.TrimLeft(s, " \t")
				if s == "" {
					return nil, errBadIfHeader
				}
				if s[0] == ')' {
					s = s[1:]
					break
				}
				var c ifCondition
				if rest, ok := cutPrefixFold(s, "Not"); ok {
					c.not = true
					s = strings.TrimLeft(rest, " \t")
				}
				switch {
				case strings.HasPrefix(s, "<"):
					end := strings.IndexByte(s, '>')
					if end < 0 {
						return nil, errBadIfHeader
					}
					c.token, s = s[1:end], s[end+1:]
				case strings.HasPrefix(s, "["):
					// An entity tag is a quoted string, which may hold "]".
					tag, rest, ok := cutETag(s[1:])
					if !ok {
						return nil, errBadIfHeader
					}
					c.etag, s = tag, rest
				default:
					return nil, errBadIfHeader
				}
				l.conds = append(l.conds, c)
			}
			if len(l.conds) == 0 {
				return nil, errBadIfHeader
			}
			lists = append(lists, l)
		default:
			return nil, errBadIfHeader
		}
		s = strings.TrimLeft(s, " \t")
	}
	if len(lists) == 0 {
		return nil, errBadIfHeader
	}
	return lists, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		if c := s[len(prefix)]; c == ' ' || c == '\t' || c == '<' || c == '[' {
			return s[len(prefix):], true
		}
	}
	return s, false
}

// cutETag reads an entity tag up to its closing "]".
func cutETag(s string) (tag, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	i := 0
	if strings.HasPrefix(s, "W/") {
		i = 2
	}
	if i >= len(s) || s[i] != '"' {
		return "", "", false
	}
	end := strings.IndexByte(s[i+1:], '"')
	if end < 0 {
		return "", "", false
	}
	tag = s[:i+1+end+1]
	rest = strings.TrimLeft(s[i+1+end+1:], " \t")
	if !strings.HasPrefix(rest, "]") {
		return "", "", false
	}
	return tag, rest[1:], true
}

// etagMatches reports whether an entity tag from a header names etag, the
// unquoted tag a file carries. Weakness is ignored: Cog's tags change with
// every write, so a weak and a strong tag name the same version.
func etagMatches(tag, etag string) bool {
	tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
	return tag == `"`+etag+`"`
}

func etagListMatches(list, etag string) bool {
	for t := range strings.SplitSeq(list, ",") {
		if etagMatches(t, etag) {
			return true
		}
	}
	return false
}

// davConditions checks a request that changes files against its conditional
// headers and the locks on what it changes. One is made per request: it
// parses the If header once, and remembers which lock tokens the request
// submitted.
type davConditions struct {
	r        *http.Request
	files    *files.Plugin
	username string
	lists    []ifList
	// submitted is the lock tokens of the If header's lists that held.
	submitted map[string]bool
	// extra is what else the request changes, a MOVE or COPY's destination,
	// whose lock tokens a client may send untagged with the source's.
	extra []scopedPath
}

type scopedPath struct {
	path  string
	scope lockScope
}

// also adds what else the request changes, so a token for one of its locks
// in an untagged list counts.
func (c *davConditions) also(p string, scope lockScope) {
	c.extra = append(c.extra, scopedPath{p, scope})
}

func newDAVConditions(r *http.Request, fp *files.Plugin, username string) (*davConditions, error) {
	c := &davConditions{r: r, files: fp, username: username, submitted: map[string]bool{}}
	if h := r.Header.Get("If"); h != "" {
		lists, err := parseIf(h)
		if err != nil {
			return nil, err
		}
		c.lists = lists
	}
	return c, nil
}

// lockScope is what a change reaches beyond its own resource: with member, it
// adds to or removes from a folder, whose lock protects what the folder
// holds; with beneath, it changes everything inside it too.
type lockScope struct{ member, beneath bool }

// check evaluates the conditional headers against target, the request's
// resource, as cur (nil when nothing is there): If-Match, If-Unmodified-Since,
// If-None-Match, and the If header, whose lock tokens count against the locks
// the change reaches by scope. It is errPreconditionFailed when one does not
// hold.
func (c *davConditions) check(target string, cur *files.FileMeta, scope lockScope) error {
	h := c.r.Header
	if im := strings.TrimSpace(h.Get("If-Match")); im != "" {
		if cur == nil || (im != "*" && !etagListMatches(im, cur.ETag)) {
			return errPreconditionFailed
		}
	} else if ius := h.Get("If-Unmodified-Since"); ius != "" && cur != nil {
		if t, err := http.ParseTime(ius); err == nil && time.Unix(cur.MTime, 0).After(t) {
			return errPreconditionFailed
		}
	}
	if inm := strings.TrimSpace(h.Get("If-None-Match")); inm != "" && cur != nil {
		if inm == "*" || etagListMatches(inm, cur.ETag) {
			return errPreconditionFailed
		}
	}
	if c.lists == nil {
		return nil
	}

	// The If header holds when any one of its lists does.
	held := false
	for _, l := range c.lists {
		ok, err := c.listHolds(l, target, cur, scope)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		held = true
		for _, cond := range l.conds {
			if cond.token != "" && !cond.not {
				c.submitted[cond.token] = true
			}
		}
	}
	if !held {
		return errPreconditionFailed
	}
	return nil
}

// listHolds evaluates one If list against the resource it is tagged with.
func (c *davConditions) listHolds(l ifList, target string, cur *files.FileMeta, scope lockScope) (bool, error) {
	res, meta := target, cur
	if l.resource != "" {
		p, ok := chunkDestination(l.resource)
		if !ok {
			// A resource outside the files tree has no state here to match.
			return false, nil
		}
		if p != target {
			m, err := c.files.GetByPath(c.username, p)
			if err != nil && !errors.Is(err, files.ErrNotFound) {
				return false, err
			}
			res, meta, scope = p, m, lockScope{}
			if err != nil {
				meta = nil
			}
		}
	}
	var locks []files.DAVLock
	for _, cond := range l.conds {
		var ok bool
		if cond.token != "" {
			if locks == nil {
				var err error
				if locks, err = c.locks(res, scope); err != nil {
					return false, err
				}
				if l.resource == "" {
					for _, e := range c.extra {
						more, err := c.locks(e.path, e.scope)
						if err != nil {
							return false, err
						}
						locks = append(locks, more...)
					}
				}
			}
			ok = hasToken(locks, cond.token)
		} else {
			ok = meta != nil && etagMatches(cond.etag, meta.ETag)
		}
		if ok == cond.not {
			return false, nil
		}
	}
	return true, nil
}

func hasToken(locks []files.DAVLock, token string) bool {
	for i := range locks {
		if locks[i].Token == token {
			return true
		}
	}
	return false
}

// locks is the locks a change to target reaches: on target and the folders
// above it, and as scope says, its folder and everything inside it.
func (c *davConditions) locks(target string, scope lockScope) ([]files.DAVLock, error) {
	locks, err := c.files.LocksFor(c.username, target, scope.beneath)
	if err != nil {
		return nil, err
	}
	if scope.member && target != "/" {
		parentLocks, err := c.files.LocksFor(c.username, path.Dir(target), false)
		if err != nil {
			return nil, err
		}
		locks = append(locks, parentLocks...)
	}
	return locks, nil
}

// requireTokens is a *lockedError unless the request submitted a token for
// every lock a change to target reaches.
func (c *davConditions) requireTokens(target string, scope lockScope) error {
	locks, err := c.locks(target, scope)
	if err != nil {
		return err
	}
	// A lock root shared by several shared locks needs only one of them.
	satisfied := map[string]bool{}
	for _, l := range locks {
		if c.submitted[l.Token] {
			satisfied[l.Path] = true
		}
	}
	var missing []string
	for _, l := range locks {
		if !satisfied[l.Path] {
			satisfied[l.Path] = true
			missing = append(missing, l.Path)
		}
	}
	if len(missing) > 0 {
		return &lockedError{Roots: missing}
	}
	return nil
}

// writeCondError answers an error from a conditional write: the client's
// conditions or locks with their status, and anything else as handle says.
// It reports whether it answered.
func writeCondError(w http.ResponseWriter, hrefPrefix string, err error) bool {
	var le *lockedError
	switch {
	case errors.As(err, &le):
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusLocked)
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`+"\n"+`<d:error xmlns:d="DAV:"><d:lock-token-submitted>`)
		for _, root := range le.Roots {
			fmt.Fprintf(w, "<d:href>%s</d:href>", xmlEscape(davHref(hrefPrefix, root, false)))
		}
		io.WriteString(w, "</d:lock-token-submitted></d:error>\n")
	case errors.Is(err, errPreconditionFailed):
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
	case errors.Is(err, errBadIfHeader):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		return false
	}
	return true
}
