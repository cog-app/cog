package webpush

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	require.NoError(t, err)
	return b
}

// TestEncryptRFC8291 reproduces the example of RFC 8291 section 5 from the
// keys and salt of its appendix A.
func TestEncryptRFC8291(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(unb64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	require.NoError(t, err)
	assert.Equal(t, b64(unb64(t, `BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIg
		Dll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8`)), b64(as.PublicKey().Bytes()))
	k := Keys{
		P256DH: unb64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		Auth:   unb64(t, "BTBZMqHH6r4Tts7J_aSIgg"),
	}
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"), k, as, unb64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	require.NoError(t, err)
	want := `DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT
		pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN`
	assert.Equal(t, strings.Join(strings.Fields(want), ""), b64(got))

	// The receiver's side, from its private key, reads it back.
	ua, err := ecdh.P256().NewPrivateKey(unb64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	require.NoError(t, err)
	assert.Equal(t, "When I grow up, I want to be a watermelon", string(decrypt(t, got, ua, k.Auth)))
}

// decrypt is the user agent's half of RFC 8291, for the tests.
func decrypt(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	assert.Equal(t, uint32(recordSize), binary.BigEndian.Uint32(body[16:20]))
	plain, err := Decrypt(body, ua, auth)
	require.NoError(t, err)
	return plain
}

func TestEncryptRejects(t *testing.T) {
	ua, err := ecdh.P256().GenerateKey(nil)
	require.NoError(t, err)
	good := Keys{P256DH: ua.PublicKey().Bytes(), Auth: make([]byte, 16)}
	_, err = Encrypt(make([]byte, MaxPlaintext+1), good)
	assert.True(t, err != nil)
	_, err = Encrypt([]byte("x"), Keys{P256DH: []byte("short"), Auth: good.Auth})
	assert.True(t, err != nil)
	_, err = Encrypt([]byte("x"), Keys{P256DH: good.P256DH, Auth: []byte("short")})
	assert.True(t, err != nil)
	// Every message has its own sender key and salt.
	a, err := Encrypt([]byte("x"), good)
	require.NoError(t, err)
	b, err := Encrypt([]byte("x"), good)
	require.NoError(t, err)
	assert.True(t, b64(a) != b64(b))
	assert.Equal(t, "x", string(decrypt(t, a, ua, good.Auth)))
}

// TestVAPIDRFC8292 checks the verifier against RFC 8292's example token and
// key, so a token Cog signs is checked by something known to be right.
func TestVAPIDRFC8292(t *testing.T) {
	const token = "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJhdWQiOiJodHRwczovL3B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1haWx0bzpwdXNoQGV4YW1wbGUuY29tIn0.i3CYb7t4xfxCDquptFOepC9GAu_HLGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA"
	pub := unb64(t, "BA1Hxzyi1RUM1b5wjxsn7nGxAszw2u61m164i3MrAIxHF6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs")
	assert.True(t, verifyES256(token, pub))
	assert.False(t, verifyES256(strings.Replace(token, ".i3CYb7", ".i3CYb8", 1), pub))

	raw, err := GenerateVAPIDKey()
	require.NoError(t, err)
	v, err := ParseVAPID(raw, "mailto:admin@example.com")
	require.NoError(t, err)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	h, err := v.Authorization("https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV", now)
	require.NoError(t, err)
	tok, k, ok := parseVAPIDHeader(h)
	require.True(t, ok, h)
	assert.Equal(t, v.PublicKey(), k)
	assert.True(t, verifyES256(tok, unb64(t, k)))
	parts := strings.Split(tok, ".")
	var hdr, claims map[string]any
	require.NoError(t, json.Unmarshal(unb64(t, parts[0]), &hdr))
	require.NoError(t, json.Unmarshal(unb64(t, parts[1]), &claims))
	assert.Equal(t, any("ES256"), hdr["alg"])
	assert.Equal(t, any("https://push.example.net"), claims["aud"])
	assert.Equal(t, any("mailto:admin@example.com"), claims["sub"])
	assert.Equal(t, any(float64(now.Add(time.Hour).Unix())), claims["exp"])

	// The same key gives the same identity.
	again, err := ParseVAPID(raw, "")
	require.NoError(t, err)
	assert.Equal(t, v.PublicKey(), again.PublicKey())
	_, err = ParseVAPID([]byte("nope"), "")
	assert.True(t, err != nil)
}

func parseVAPIDHeader(h string) (token, key string, ok bool) {
	rest, ok := strings.CutPrefix(h, "vapid ")
	if !ok {
		return "", "", false
	}
	for p := range strings.SplitSeq(rest, ",") {
		p = strings.TrimSpace(p)
		if v, ok := strings.CutPrefix(p, "t="); ok {
			token = v
		}
		if v, ok := strings.CutPrefix(p, "k="); ok {
			key = v
		}
	}
	return token, key, token != "" && key != ""
}

// TestSend posts to a test push service and reads what it received as a
// user agent would.
func TestSend(t *testing.T) {
	ua, err := ecdh.P256().GenerateKey(nil)
	require.NoError(t, err)
	keys := Keys{P256DH: ua.PublicKey().Bytes(), Auth: []byte("0123456789abcdef")}
	raw, err := GenerateVAPIDKey()
	require.NoError(t, err)
	v, err := ParseVAPID(raw, "")
	require.NoError(t, err)

	status := http.StatusCreated
	var got *http.Request
	var body []byte
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "120")
		}
		w.WriteHeader(status)
	}))
	defer ps.Close()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	s := &Sender{Client: ps.Client(), VAPID: v, Now: func() time.Time { return now }}
	msg := Message{Body: []byte("<x/>"), ContentType: "application/xml", TTL: time.Hour, Topic: "abc", Urgency: "low"}
	ctx := context.Background()

	require.NoError(t, s.Send(ctx, ps.URL+"/sub/1", keys, msg))
	assert.Equal(t, "POST", got.Method)
	assert.Equal(t, "/sub/1", got.URL.Path)
	assert.Equal(t, "3600", got.Header.Get("TTL"))
	assert.Equal(t, "aes128gcm", got.Header.Get("Content-Encoding"))
	assert.Equal(t, "application/xml", got.Header.Get("Content-Type"))
	assert.Equal(t, "abc", got.Header.Get("Topic"))
	assert.Equal(t, "low", got.Header.Get("Urgency"))
	tok, _, ok := parseVAPIDHeader(got.Header.Get("Authorization"))
	require.True(t, ok)
	assert.True(t, verifyES256(tok, unb64(t, v.PublicKey())))
	assert.Equal(t, "<x/>", string(decrypt(t, body, ua, keys.Auth)))

	status = http.StatusGone
	assert.True(t, errors.Is(s.Send(ctx, ps.URL, keys, msg), ErrGone))
	status = http.StatusNotFound
	assert.True(t, errors.Is(s.Send(ctx, ps.URL, keys, msg), ErrGone))
	status = http.StatusTooManyRequests
	var se *StatusError
	require.True(t, errors.As(s.Send(ctx, ps.URL, keys, msg), &se))
	assert.Equal(t, 429, se.Code)
	assert.Equal(t, 2*time.Minute, se.RetryAfter)
	assert.True(t, s.Send(ctx, "ftp://x/y", keys, msg) != nil)
}
