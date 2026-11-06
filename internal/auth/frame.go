package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// A process plugin's pages render on an origin of their own, inside a frame
// in the shell, so a plugin's HTML cannot act as the user against the host
// (D14). The browser sends no host cookie to that origin, so the frame is
// handed a token: the shell puts a short-lived one in the frame's URL, and
// the plugin origin keeps a longer one in a cookie of its own. Both are
// HMAC-signed by the host, name the user and the plugin, and expire.

// FrameCookieName is the plugin origin's own cookie.
const FrameCookieName = "cog_frame"

// FrameParam carries the handoff token in the frame's URL.
const FrameParam = "cog_frame"

// FrameHandoffTTL is how long the token in a frame's URL is good for: the
// time between rendering the shell and the browser loading the frame.
const FrameHandoffTTL = 2 * time.Minute

// FrameCookieTTL is how long the plugin origin's cookie lasts.
const FrameCookieTTL = 12 * time.Hour

// FrameToken signs a token admitting userID to pluginID's origin until exp.
func FrameToken(key []byte, userID, pluginID string, exp time.Time) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(userID + "\x00" + pluginID + "\x00" + strconv.FormatInt(exp.Unix(), 10)))
	return payload + "." + frameMAC(key, payload)
}

// ParseFrameToken checks token and returns whom it admits, and to which
// plugin, when it is genuine and not yet expired at now.
func ParseFrameToken(key []byte, token string, now time.Time) (userID, pluginID string, ok bool) {
	payload, mac, found := strings.Cut(token, ".")
	if !found || !hmac.Equal([]byte(mac), []byte(frameMAC(key, payload))) {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() >= exp || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func frameMAC(key []byte, payload string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// PluginIDFromPath returns the plugin a /apps/{id}/... path belongs to.
func PluginIDFromPath(p string) string {
	rest, ok := strings.CutPrefix(p, "/apps/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}
