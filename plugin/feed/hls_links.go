package feed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// Media links: signed URLs to an item's HLS presentation that need no
// session, so a player that cannot log in (mpv, hls.js, Media3) can fetch
// them. A signature covers the user and the item, and every file of the
// presentation carries the same one. Links do not expire: one that leaks
// plays one item, which costs little. Revoking the user's key (RotateHLSKey)
// is what ends them.

// hlsOptions are the choices a link makes about its presentation. The
// signed ones (Skip) make a link a different grant.
type hlsOptions struct {
	// Skip plays the item with its silences cut out.
	Skip bool
	// Edit names the edit list a skip request is for. It only selects among
	// the plans the server can compute, so it is not signed.
	Edit string
}

// parseHLSOptions reads the options of a request's query. Only sig, skip=silence
// and, with skip, edit are allowed.
func parseHLSOptions(q url.Values) (hlsOptions, error) {
	var o hlsOptions
	for k, vs := range q {
		if len(vs) != 1 {
			return hlsOptions{}, fmt.Errorf("parameter %s given %d times", strconv.Quote(k), len(vs))
		}
		switch k {
		case "sig":
		case "skip":
			if vs[0] != "silence" {
				return hlsOptions{}, fmt.Errorf("unknown skip %s", strconv.Quote(vs[0]))
			}
			o.Skip = true
		case "edit":
			o.Edit = vs[0]
		default:
			return hlsOptions{}, fmt.Errorf("unknown parameter %s", strconv.Quote(k))
		}
	}
	if o.Edit != "" {
		if !o.Skip {
			return hlsOptions{}, errors.New("edit needs skip")
		}
		if !validEditID(o.Edit) {
			return hlsOptions{}, fmt.Errorf("bad edit %s", strconv.Quote(o.Edit))
		}
	} else if _, ok := q["edit"]; ok {
		return hlsOptions{}, errors.New("bad edit \"\"")
	}
	return o, nil
}

// validEditID reports whether s is 12 lowercase hex characters.
func validEditID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Values are the signed options as query parameters, without sig and edit;
// empty for zero options.
func (o hlsOptions) Values() url.Values {
	v := url.Values{}
	if o.Skip {
		v.Set("skip", "silence")
	}
	return v
}

// query is the query string every file of the presentation carries: the
// options, the signature and the edit list, in sorted key order.
func (o hlsOptions) query(sig string) string {
	v := o.Values()
	v.Set("sig", sig)
	if o.Edit != "" {
		v.Set("edit", o.Edit)
	}
	return "?" + v.Encode()
}

// hlsSignature signs the presentation of user's item with key. The options
// are appended to the message in their canonical encoding, so a link with
// them is a different grant and one without keeps its original signature.
func hlsSignature(key []byte, user string, itemID int64, o hlsOptions) string {
	mac := hmac.New(sha256.New, key)
	msg := "cog-feed-hls\x00" + user + "\x00" + strconv.FormatInt(itemID, 10)
	if opts := o.Values().Encode(); opts != "" {
		msg += "\x00" + opts
	}
	mac.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validHLSSignature reports whether sig is user's signature for itemID with
// options o.
func validHLSSignature(key []byte, user string, itemID int64, o hlsOptions, sig string) bool {
	return hmac.Equal([]byte(hlsSignature(key, user, itemID, o)), []byte(sig))
}

// HLSMasterURL is the signed path of an item's master playlist. It works
// until the user revokes media links.
func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64, o hlsOptions) (string, error) {
	key, err := store.HLSKey()
	if err != nil {
		return "", err
	}
	return "/apps/feed/hls/" + url.PathEscape(username) + "/" + strconv.FormatInt(itemID, 10) +
		"/master.m3u8" + o.query(hlsSignature(key, username, itemID, o)), nil
}

// HLSSkipMasterURL is HLSMasterURL with skip=silence, for phase 6a and tests.
func (p *Plugin) HLSSkipMasterURL(store *Store, username string, itemID int64) (string, error) {
	return p.HLSMasterURL(store, username, itemID, hlsOptions{Skip: true})
}
