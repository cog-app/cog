package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// VAPID identifies an application server to push services (RFC 8292). A
// subscription made with its public key accepts messages only from the
// holder of the private key.
type VAPID struct {
	key    *ecdsa.PrivateKey
	public string // base64url of the uncompressed public key
	// Subject is a contact for the push service's operator: a mailto: or
	// https: URI (RFC 8292 section 2.1).
	Subject string
}

// GenerateVAPIDKey returns a new P-256 private key in raw form, to be kept
// and handed to ParseVAPID.
func GenerateVAPIDKey() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate VAPID key: %w", err)
	}
	b, err := k.Bytes()
	if err != nil {
		return nil, fmt.Errorf("webpush: encode VAPID key: %w", err)
	}
	return b, nil
}

// ParseVAPID returns the identity whose private key is raw, a P-256 scalar
// as GenerateVAPIDKey makes.
func ParseVAPID(raw []byte, subject string) (*VAPID, error) {
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID key: %w", err)
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID public key: %w", err)
	}
	return &VAPID{key: k, public: b64(pub), Subject: subject}, nil
}

// PublicKey is the public key, uncompressed and base64url-encoded without
// padding: what clients subscribe with and the k parameter of the
// Authorization header.
func (v *VAPID) PublicKey() string { return v.public }

// tokenLifetime is how long a VAPID token is valid. RFC 8292 section 2
// allows at most 24 hours; a push is sent at once, so an hour is plenty
// and keeps the clocks of both sides from mattering.
const tokenLifetime = time.Hour

// Authorization returns the Authorization header for a push to endpoint
// (RFC 8292 section 3): a JWT signed with ES256 whose audience is the
// endpoint's origin.
func (v *VAPID) Authorization(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("webpush: endpoint: %w", err)
	}
	claims := map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(tokenLifetime).Unix(),
	}
	if v.Subject != "" {
		claims["sub"] = v.Subject
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("webpush: VAPID claims: %w", err)
	}
	signing := b64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64(body)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: sign VAPID token: %w", err)
	}
	// JWS (RFC 7518 section 3.4) wants R and S as fixed 32-octet halves,
	// not the ASN.1 form.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64(sig) + ", k=" + v.public, nil
}

// verifyES256 checks a compact JWS signed with ES256 against an
// uncompressed public key. Push services do this; Cog only needs it to test
// its tokens, against RFC 8292's example as well as its own.
func verifyES256(token string, public []byte) bool {
	var dots []int
	for i := range len(token) {
		if token[i] == '.' {
			dots = append(dots, i)
		}
	}
	if len(dots) != 2 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(token[dots[1]+1:])
	if err != nil || len(sig) != 64 {
		return false
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), public)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(token[:dots[1]]))
	return ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// DecodeKey decodes a key or secret as clients send them: base64url,
// padded or not, and plain base64 for clients that use it.
func DecodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("webpush: %q is not base64", s)
}
