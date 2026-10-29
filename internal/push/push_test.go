package push_test

import (
	"context"
	"crypto/ecdh"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/push"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/webpush"
)

func keys(t *testing.T) webpush.Keys {
	ua, err := ecdh.P256().GenerateKey(nil)
	require.NoError(t, err)
	return webpush.Keys{P256DH: ua.PublicKey().Bytes(), Auth: make([]byte, 16)}
}

// TestDeliverWhileMarked: a change marked while its push is being sent is
// not cleared by that push's success; it is owed and due at once. And a
// claimed subscription is leased, so a second deliverer leaves it alone.
func TestDeliverWhileMarked(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	st := env.Providers.Push

	sub := &push.Subscription{UserID: alice.ID, Protocol: "test", Resource: "r", Endpoint: "https://example.invalid/1",
		Keys: keys(t), Expires: env.Clock.Now().Add(time.Hour)}
	created, err := st.Register(sub)
	require.NoError(t, err)
	assert.True(t, created)
	// The same endpoint again is the same registration; another user's
	// claim to it is refused.
	again := *sub
	created, err = st.Register(&again)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, sub.ID, again.ID)
	bob := env.CreateUser(t, "bob", "pass")
	taken := *sub
	taken.UserID = bob.ID
	_, err = st.Register(&taken)
	assert.True(t, errors.Is(err, push.ErrTaken))

	var d *push.Deliverer
	var sends atomic.Int32
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sends.Add(1) == 1 {
			// A change arrives while the first push is in flight, and a
			// second deliverer runs meanwhile.
			// Not require: this is the server's goroutine.
			if err := st.Mark(2, sub.ID); err != nil {
				t.Error(err)
			}
			if err := d.Deliver(context.Background()); err != nil {
				t.Error(err)
			}
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ps.Close()
	_, err = env.Providers.DB.Exec(`UPDATE push_subscriptions SET endpoint = ? WHERE id = ?`, ps.URL, sub.ID)
	require.NoError(t, err)
	d = &push.Deliverer{Store: st, Sender: &webpush.Sender{Client: ps.Client(), Now: env.Clock.Now},
		Messages: map[push.Protocol]push.Messenger{"test": func(s *push.Subscription) (webpush.Message, error) {
			return webpush.Message{Body: []byte{byte(s.Pending)}}, nil
		}},
		Log: slog.New(slog.DiscardHandler)}

	require.NoError(t, st.Mark(1, sub.ID))
	require.NoError(t, d.Deliver(context.Background()))
	assert.Equal(t, int32(1), sends.Load())
	got, err := st.Get(sub.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(3), got.Pending)
	require.NoError(t, d.Deliver(context.Background()))
	assert.Equal(t, int32(2), sends.Load())
	got, err = st.Get(sub.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(0), got.Pending)

	// A protocol nobody writes messages for is left owing, not lost.
	other := &push.Subscription{UserID: alice.ID, Protocol: "unknown", Resource: "r", Endpoint: ps.URL + "/2",
		Keys: keys(t), Expires: env.Clock.Now().Add(time.Hour)}
	_, err = st.Register(other)
	require.NoError(t, err)
	require.NoError(t, st.Mark(1, other.ID))
	require.NoError(t, d.Deliver(context.Background()))
	got, err = st.Get(other.ID)
	require.NoError(t, err)
	assert.Equal(t, uint(1), got.Pending)
	assert.Equal(t, 1, got.Failures)
}
