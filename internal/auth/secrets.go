package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"golang.org/x/crypto/hkdf"
)

// DerivedKey returns a 32-byte key derived from the master secret for a
// specific purpose label. Different labels produce independent keys so that a
// compromise of one purpose-key does not weaken another.
//
// The derivation uses HKDF-SHA256 with a fixed salt and the label as info.
func DerivedKey(master []byte, label string) []byte {
	r := hkdf.New(sha256.New, master, []byte("cog/v1/derived"), []byte(label))
	out := make([]byte, 32)
	if _, err := io.ReadFull(r, out); err != nil {
		// HKDF over SHA-256 cannot fail for output ≤ 8160 bytes; defensive only.
		panic("hkdf: " + err.Error())
	}
	return out
}

// HashAtRest returns a constant-time-comparable hex digest of token under the
// per-purpose key derived from master. Use this when storing one-time tokens
// (password reset, MFA pending, WebAuthn session) so that a database leak does
// not yield usable secrets.
//
// Stored value: HashAtRest(master, label, plaintextToken).
// Lookup: re-hash the inbound token and compare equal.
func HashAtRest(master []byte, label, token string) string {
	mac := hmac.New(sha256.New, DerivedKey(master, label))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
