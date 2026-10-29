package jmap_test

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/jmap"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/webpush"
)

// pushService is a Web Push service over TLS and the clients behind it,
// which decrypt what arrives.
type pushService struct {
	*httptest.Server
	t *testing.T

	mu   sync.Mutex
	keys map[string]*ecdh.PrivateKey
	auth map[string][]byte
	got  map[string][]string
}

func newPushService(t *testing.T, env *cogtest.Env) *pushService {
	ps := &pushService{t: t, keys: map[string]*ecdh.PrivateKey{}, auth: map[string][]byte{}, got: map[string][]string{}}
	ps.Server = httptest.NewTLSServer(http.HandlerFunc(ps.serve))
	t.Cleanup(ps.Close)
	addr := netip.MustParseAddrPort(strings.TrimPrefix(ps.URL, "https://"))
	env.Providers.Pusher.Sender.Client = safehttp.ClientWith(10*time.Second, safehttp.Options{
		Allow: []netip.Prefix{netip.PrefixFrom(addr.Addr(), addr.Addr().BitLen())},
		TLS:   ps.Client().Transport.(*http.Transport).TLSClientConfig,
	})
	return ps
}

func (ps *pushService) serve(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ua, ok := ps.keys[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	plain, err := webpush.Decrypt(body, ua, ps.auth[name])
	if err != nil {
		ps.t.Errorf("push to %s: %v", name, err)
		http.Error(w, err.Error(), 400)
		return
	}
	ps.got[name] = append(ps.got[name], string(plain))
	w.WriteHeader(http.StatusCreated)
}

// subscription is a PushSubscription create for a new endpoint.
func (ps *pushService) subscription(name string) obj {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ua, err := ecdh.P256().GenerateKey(nil)
	require.NoError(ps.t, err)
	ps.keys[name] = ua
	ps.auth[name] = []byte(name + "-auth-secret-16b")[:16]
	return obj{
		"deviceClientId": "device-" + name,
		"url":            ps.URL + "/" + name,
		"keys": obj{
			"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(ps.auth[name]),
		},
	}
}

func (ps *pushService) take(name string) []obj {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	var out []obj
	for _, m := range ps.got[name] {
		var o obj
		require.NoError(ps.t, json.Unmarshal([]byte(m), &o))
		out = append(out, o)
	}
	delete(ps.got, name)
	return out
}

func round(t *testing.T, env *cogtest.Env) {
	t.Helper()
	require.NoError(t, env.Providers.Pusher.Round(context.Background()))
}

func TestPushSubscription(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	book(t, env, alice)
	ps := newPushService(t, env)

	set := one(t, env, alice, "PushSubscription/set", obj{"create": obj{
		"p":     ps.subscription("phone"),
		"plain": obj{"deviceClientId": "x", "url": "http://example.com/push"},
	}})
	id := set["created"].(obj)["p"].(obj)["id"].(string)
	assert.Equal[any](t, []any{"keys", "url"}, set["notCreated"].(obj)["plain"].(obj)["properties"])

	// The verification code arrives first; until it is set nothing else
	// does.
	round(t, env)
	msgs := ps.take("phone")
	require.Equal(t, 1, len(msgs))
	assert.Equal[any](t, "PushVerification", msgs[0]["@type"])
	assert.Equal[any](t, id, msgs[0]["pushSubscriptionId"])
	code := msgs[0]["verificationCode"].(string)

	davReq(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/a.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:a\r\nFN:A\r\nEND:VCARD\r\n").Status(201)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("phone")))

	bad := one(t, env, alice, "PushSubscription/set", obj{"update": obj{id: obj{"verificationCode": "wrong"}}})
	assert.Equal[any](t, "invalidProperties", bad["notUpdated"].(obj)[id].(obj)["type"])
	one(t, env, alice, "PushSubscription/set", obj{"update": obj{id: obj{"verificationCode": code, "types": []any{"ContactCard"}}}})

	// The URL and keys are never shown.
	got := one(t, env, alice, "PushSubscription/get", obj{})["list"].([]any)[0].(obj)
	assert.Nil(t, got["url"])
	assert.Nil(t, got["keys"])
	assert.Equal[any](t, code, got["verificationCode"])
	assert.Equal[any](t, []any{"ContactCard"}, got["types"])
	// Another user does not see it, nor can they change it.
	assert.Equal(t, 0, len(one(t, env, bob, "PushSubscription/get", obj{})["list"].([]any)))
	assert.Equal[any](t, "notFound", one(t, env, bob, "PushSubscription/set", obj{"update": obj{id: obj{"types": nil}}})["notUpdated"].(obj)[id].(obj)["type"])

	// A change over DAV pushes the new state.
	davReq(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/contacts/b.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:b\r\nFN:B\r\nEND:VCARD\r\n").Status(201)
	round(t, env)
	msgs = ps.take("phone")
	require.Equal(t, 1, len(msgs))
	assert.Equal[any](t, "StateChange", msgs[0]["@type"])
	state := one(t, env, alice, "ContactCard/get", obj{"accountId": acct(alice), "ids": []any{}})["state"]
	assert.Equal[any](t, obj{acct(alice): obj{"ContactCard": state}}, msgs[0]["changed"])

	// So does a change over JMAP, and to a book shared with alice.
	b := book(t, env, bob)
	_, err := env.Providers.Shares.Create(bob.ID, dav.ShareType, b.ID.String(), "USER", alice.ID, 1)
	require.NoError(t, err)
	round(t, env)
	ps.take("phone")
	davReq(t, env, bob, "PUT", "/remote.php/dav/addressbooks/bob/contacts/c.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:c\r\nFN:C\r\nEND:VCARD\r\n").Status(201)
	round(t, env)
	msgs = ps.take("phone")
	require.Equal(t, 1, len(msgs))
	changed := msgs[0]["changed"].(obj)
	assert.NotNil(t, changed[acct(bob)])

	des := one(t, env, alice, "PushSubscription/set", obj{"destroy": []any{id}})
	assert.Equal[any](t, []any{id}, des["destroyed"])
	davReq(t, env, alice, "DELETE", "/remote.php/dav/addressbooks/alice/contacts/b.vcf", "").Status(204)
	round(t, env)
	assert.Equal(t, 0, len(ps.take("phone")))
}

func TestEventSource(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	book(t, env, alice)
	body := env.Get(t, jmap.EventsPath+"?types=*&closeafter=state&ping=0", alice.Auth).Status(200).ReadBody()
	require.True(t, strings.HasPrefix(body, "event: state\ndata: "), body)
	var sc obj
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(body, "event: state\ndata: "))), &sc))
	assert.Equal[any](t, "StateChange", sc["@type"])
	assert.NotNil(t, sc["changed"].(obj)[acct(alice)].(obj)["ContactCard"])
}
