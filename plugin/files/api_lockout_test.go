package files_test

import (
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestPublicLink_PasswordThrottle refuses a client after enough wrong
// attempts (audit security.md #36), even with the right password.
func TestPublicLink_PasswordThrottle(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload a file and create a password-protected public link.
	meta := uploadFile(t, env, alice, "/secret.txt", "data")

	var rec share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"password":      "correct-horse",
	}, alice.Auth).Status(201).JSON(&rec)
	require.NotNil(t, rec.Token)
	tok := *rec.Token

	// 10 wrong attempts get individually rejected with 401.
	for range 10 {
		env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "wrong")).Status(401)
	}

	// The next — even with the CORRECT password — is rejected because the
	// client is limited. Without the throttle, the correct password would
	// unlock and return 200.
	env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "correct-horse")).Status(401)
}

// TestPublicLink_ThrottleIsolatedByToken: wrong guesses at link A, short of
// the client's limit, don't count against link B.
func TestPublicLink_ThrottleIsolatedByToken(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	metaA := uploadFile(t, env, alice, "/a.txt", "a")
	metaB := uploadFile(t, env, alice, "/b.txt", "b")

	var recA, recB share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(metaA.ID, 10),
		"password":      "pwa",
	}, alice.Auth).Status(201).JSON(&recA)
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(metaB.ID, 10),
		"password":      "pwb",
	}, alice.Auth).Status(201).JSON(&recB)

	for range 5 {
		env.Get(t, "/s/"+*recA.Token, cogtest.BasicAuth("", "wrong")).Status(401)
	}

	// B still works on first try.
	env.Get(t, "/s/"+*recB.Token, cogtest.BasicAuth("", "pwb")).Status(200)
}

// TestPublicLink_SuccessClearsFailureCounter: a correct password before the
// threshold resets the counter so subsequent attempts get a fresh budget.
func TestPublicLink_SuccessClearsFailureCounter(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/r.txt", "r")

	var rec share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"password":      "good",
	}, alice.Auth).Status(201).JSON(&rec)
	tok := *rec.Token

	// 4 wrong (still under threshold).
	for range 4 {
		env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "wrong")).Status(401)
	}
	// Correct password — succeeds and clears counter.
	env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "good")).Status(200)

	// Another 4 wrong should still be allowed (counter was reset).
	for range 4 {
		env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "wrong")).Status(401)
	}
	env.Get(t, "/s/"+tok, cogtest.BasicAuth("", "good")).Status(200)
}
