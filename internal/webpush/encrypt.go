// Package webpush sends Web Push messages (RFC 8030) as an application
// server: payloads encrypted for the user agent (RFC 8291, aes128gcm of
// RFC 8188) and requests signed with VAPID (RFC 8292). It needs nothing
// beyond the standard library.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Keys are what a user agent hands out with a subscription so that only it
// can read the messages (RFC 8291 section 2).
type Keys struct {
	P256DH []byte // the user agent's ECDH public key, uncompressed P-256 point
	Auth   []byte // the authentication secret, 16 octets
}

// Validate reports whether the keys can be used to encrypt.
func (k Keys) Validate() error {
	if _, err := ecdh.P256().NewPublicKey(k.P256DH); err != nil {
		return fmt.Errorf("webpush: subscription public key: %w", err)
	}
	if len(k.Auth) != 16 {
		return fmt.Errorf("webpush: authentication secret is %d octets, not 16", len(k.Auth))
	}
	return nil
}

// recordSize is the rs field of the content coding header. A push message
// is one record, so it only has to exceed the message; 4096 is what every
// implementation sends and the limit push services accept.
const recordSize = 4096

// MaxPlaintext is the most a single-record message can carry: the record
// size less the AEAD tag and the padding delimiter.
const MaxPlaintext = recordSize - 16 - 1

// Encrypt encrypts plaintext for the user agent that holds k, with a fresh
// sender key and salt, as an aes128gcm body.
func Encrypt(plaintext []byte, k Keys) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: sender key: %w", err)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("webpush: salt: %w", err)
	}
	return encrypt(plaintext, k, as, salt)
}

// encrypt is Encrypt with the sender key and salt given, so the RFC's
// example can be reproduced.
func encrypt(plaintext []byte, k Keys, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, fmt.Errorf("webpush: message of %d octets is larger than %d", len(plaintext), MaxPlaintext)
	}
	if len(salt) != 16 {
		return nil, errors.New("webpush: salt must be 16 octets")
	}
	if err := k.Validate(); err != nil {
		return nil, err
	}
	ua, err := ecdh.P256().NewPublicKey(k.P256DH)
	if err != nil {
		return nil, fmt.Errorf("webpush: subscription public key: %w", err)
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, fmt.Errorf("webpush: key agreement: %w", err)
	}
	asPublic := as.PublicKey().Bytes()

	// RFC 8291 section 3.3: the ECDH secret and the authentication secret
	// make the input keying material of RFC 8188.
	keyInfo := "WebPush: info\x00" + string(k.P256DH) + string(asPublic)
	ikm, err := hkdf.Key(sha256.New, secret, k.Auth, keyInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("webpush: combine secrets: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("webpush: content key: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("webpush: content key: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("webpush: nonce: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher: %w", err)
	}

	// RFC 8188 section 2.1: salt, record size, key ID length and key ID
	// (the sender's public key, RFC 8291 section 3.1), then the one and
	// only record, whose padding delimiter 0x02 marks it as the last.
	out := make([]byte, 0, 16+4+1+len(asPublic)+len(plaintext)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPublic)))
	out = append(out, asPublic...)
	record := append(append(make([]byte, 0, len(plaintext)+1), plaintext...), 0x02)
	return gcm.Seal(out, nonce, record, nil), nil
}

// Decrypt is the user agent's side of Encrypt: it reads a single-record
// aes128gcm body with the subscription's private key and authentication
// secret. Cog sends rather than receives; tests use it as a push client.
func Decrypt(body []byte, ua *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("webpush: body shorter than its header")
	}
	salt, idlen := body[:16], int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("webpush: body shorter than its key ID")
	}
	asPublic := body[21 : 21+idlen]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: sender key: %w", err)
	}
	secret, err := ua.ECDH(as)
	if err != nil {
		return nil, fmt.Errorf("webpush: key agreement: %w", err)
	}
	ikm, err := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPublic), 32)
	if err != nil {
		return nil, fmt.Errorf("webpush: combine secrets: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("webpush: content key: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("webpush: content key: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("webpush: nonce: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: cipher: %w", err)
	}
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, fmt.Errorf("webpush: decrypt: %w", err)
	}
	// Strip the padding: zeros, then the delimiter, 0x02 for the last record.
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("webpush: not a last record")
	}
	return plain[:i], nil
}
