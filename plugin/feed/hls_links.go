package feed

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
)

// Media links: signed URLs to an item's HLS presentation that need no
// session, so a player that cannot log in (mpv, hls.js, Media3) can fetch
// them. A signature covers the user and the item, and every file of the
// presentation carries the same one. Links do not expire: one that leaks
// plays one item, which costs little. Revoking the user's key (RotateHLSKey)
// is what ends them.

// hlsSignature signs the presentation of user's item with key. Options a
// later phase adds (captions, mute, skip) are appended to the message in a
// canonical order, so a link with them is a different grant.
func hlsSignature(key []byte, user string, itemID int64) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("cog-feed-hls\x00" + user + "\x00" + strconv.FormatInt(itemID, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// validHLSSignature reports whether sig is user's signature for itemID.
func validHLSSignature(key []byte, user string, itemID int64, sig string) bool {
	return hmac.Equal([]byte(hlsSignature(key, user, itemID)), []byte(sig))
}

// HLSMasterURL is the signed path of an item's master playlist. It works
// until the user revokes media links.
func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64) (string, error) {
	key, err := store.HLSKey()
	if err != nil {
		return "", err
	}
	return "/apps/feed/hls/" + url.PathEscape(username) + "/" + strconv.FormatInt(itemID, 10) +
		"/master.m3u8?sig=" + hlsSignature(key, username, itemID), nil
}
