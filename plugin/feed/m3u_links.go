package feed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// m3uList names a list an M3U link plays.
type m3uList struct {
	Kind ViewKind // playlist, tag, folder, sub, starred, unread
	ID   string   // decimal id; tag name (unescaped); "all" for starred and unread
}

// View is the view the list names. The id of a playlist, folder or
// subscription is parsed; a tag's name is the tag.
func (l m3uList) View() (View, error) {
	switch l.Kind {
	case ViewPlaylist, ViewFolder, ViewSubscription:
		id, err := strconv.ParseInt(l.ID, 10, 64)
		// ParseInt takes +7 and 007; the id signed must be the canonical one.
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != l.ID {
			return View{}, fmt.Errorf("bad %s id %s", l.Kind, strconv.Quote(l.ID))
		}
		return View{Kind: l.Kind, ID: id}, nil
	case ViewTag:
		return View{Kind: ViewTag, Tag: l.ID}, nil
	case ViewStarred, ViewUnread:
		return View{Kind: l.Kind}, nil
	}
	return View{}, fmt.Errorf("no M3U for %s", strconv.Quote(string(l.Kind)))
}

const m3uSuffix = ".m3u"

// parseM3UPath reads the kind and file of an M3U route. file is the path
// value, already unescaped, and must end in .m3u.
func parseM3UPath(kind, file string) (m3uList, error) {
	stem, ok := strings.CutSuffix(file, m3uSuffix)
	if !ok || stem == "" {
		return m3uList{}, fmt.Errorf("%s is not an M3U file name", strconv.Quote(file))
	}
	l := m3uList{Kind: ViewKind(kind), ID: stem}
	switch l.Kind {
	case ViewStarred, ViewUnread:
		if stem != "all" {
			return m3uList{}, fmt.Errorf("%s lists are all.m3u", kind)
		}
	case ViewTag:
		// NUL separates the fields of the signed message.
		if strings.ContainsRune(stem, 0) {
			return m3uList{}, errors.New("bad tag")
		}
	}
	if _, err := l.View(); err != nil {
		return m3uList{}, err
	}
	return l, nil
}

// path is the list's path below the user: kind/<escaped id>.m3u.
func (l m3uList) path() string {
	return string(l.Kind) + "/" + url.PathEscape(l.ID) + m3uSuffix
}

// m3uSignature signs user's list with key. The prefix differs from the HLS
// one, so neither signature opens the other route. Only the signed options
// count, so edit never reaches it.
func m3uSignature(key []byte, user string, l m3uList, o hlsOptions) string {
	mac := hmac.New(sha256.New, key)
	msg := "cog-feed-m3u\x00" + user + "\x00" + string(l.Kind) + "\x00" + l.ID
	if opts := o.Values().Encode(); opts != "" {
		msg += "\x00" + opts
	}
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validM3USignature reports whether sig is user's signature for l with o.
func validM3USignature(key []byte, user string, l m3uList, o hlsOptions, sig string) bool {
	return hmac.Equal([]byte(m3uSignature(key, user, l, o)), []byte(sig))
}

// M3UURL is the signed path of a list's M3U: /apps/feed/m3u/<user>/<path>?sig=…[&skip=silence]
// It creates the user's key on first use, so pages call it and the route does not.
func (p *Plugin) M3UURL(store *Store, username string, l m3uList, o hlsOptions) (string, error) {
	key, err := store.HLSKey()
	if err != nil {
		return "", err
	}
	return m3uPath(key, username, l, o), nil
}

// m3uPath is the signed path of a list's M3U for a key already in hand, so
// a page signing several links reads the key once.
func m3uPath(key []byte, username string, l m3uList, o hlsOptions) string {
	v := o.Values()
	v.Set("sig", m3uSignature(key, username, l, o))
	return "/apps/feed/m3u/" + url.PathEscape(username) + "/" + l.path() + "?" + v.Encode()
}

// absoluteURL prefixes path with the configured base URL, or else the
// scheme and host r reached.
func (p *Plugin) absoluteURL(r *http.Request, path string) string {
	base := p.ctx.BaseURL()
	if base == "" {
		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		base = scheme + "://" + r.Host
	}
	return base + path
}
