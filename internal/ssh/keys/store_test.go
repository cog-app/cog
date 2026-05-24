package keys_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// dbProvider adapts the plugin registry's UserDB to keys.DBProvider.
type dbProvider struct {
	env *cogtest.Env
}

func (p *dbProvider) UserDB(username string) (*sql.DB, error) {
	return p.env.Providers.Plugins.UserDB(username)
}

// usernameProvider adapts the user store to keys.UsernameProvider.
type usernameProvider struct {
	env *cogtest.Env
}

func (p *usernameProvider) Usernames() ([]string, error) {
	return p.env.Providers.Users.Usernames()
}

// genAuthorizedKey returns a fresh ed25519 authorized_keys line and its fingerprint.
func genAuthorizedKey(t *testing.T, comment string) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub))) + " " + comment
	fp := gossh.FingerprintSHA256(sshPub)
	return line, fp
}

func newStore(t *testing.T) (*keys.Store, *cogtest.Env) {
	t.Helper()
	env := cogtest.New(t)
	s := keys.NewStore(&dbProvider{env}, &usernameProvider{env}, env.Clock.Clock())
	return s, env
}

// --- Add / List / Delete ---

func TestAdd_StoresKey(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")

	key, err := s.Add(alice.Username, "laptop", line)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, "laptop", key.Name)
	require.Equal(t, fp, key.Fingerprint)
}

func TestAdd_ParseError(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	_, err := s.Add(alice.Username, "bad", "not a real key")
	require.True(t, err != nil)
}

func TestList_ReturnsAddedKeys(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	line1, _ := genAuthorizedKey(t, "k1")
	line2, _ := genAuthorizedKey(t, "k2")

	_, err := s.Add(alice.Username, "k1", line1)
	require.NoError(t, err)
	_, err = s.Add(alice.Username, "k2", line2)
	require.NoError(t, err)

	got, err := s.List(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
}

func TestList_Empty(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	got, err := s.List(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 0, len(got))
}

func TestDelete_Removes(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	line, _ := genAuthorizedKey(t, "k")

	k, err := s.Add(alice.Username, "k", line)
	require.NoError(t, err)
	require.NoError(t, s.Delete(alice.Username, k.ID))

	got, _ := s.List(alice.Username)
	require.Equal(t, 0, len(got))
}

func TestDelete_Missing(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	err := s.Delete(alice.Username, "no-such-id")
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "not found"))
}

// --- FindByFingerprint ---

func TestFindByFingerprint_Match(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")

	_, err := s.Add(alice.Username, "laptop", line)
	require.NoError(t, err)

	username, key, err := s.FindByFingerprint(fp)
	require.NoError(t, err)
	require.Equal(t, "alice", username)
	require.Equal(t, fp, key.Fingerprint)
}

func TestFindByFingerprint_NotFound(t *testing.T) {
	s, env := newStore(t)
	env.CreateUser(t, "alice", "pass")
	_, _, err := s.FindByFingerprint("SHA256:never-stored")
	require.True(t, err != nil)
}

func TestFindByFingerprint_OnlyHisOwn(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	aliceLine, aliceFP := genAuthorizedKey(t, "alice@laptop")
	bobLine, bobFP := genAuthorizedKey(t, "bob@laptop")

	_, err := s.Add(alice.Username, "k", aliceLine)
	require.NoError(t, err)
	_, err = s.Add(bob.Username, "k", bobLine)
	require.NoError(t, err)

	user, key, err := s.FindByFingerprint(aliceFP)
	require.NoError(t, err)
	require.Equal(t, "alice", user)
	require.Equal(t, aliceFP, key.Fingerprint)

	user, key, err = s.FindByFingerprint(bobFP)
	require.NoError(t, err)
	require.Equal(t, "bob", user)
	require.Equal(t, bobFP, key.Fingerprint)
}

// --- Fingerprint migration idempotency ---

func TestAdd_MigrateFingerprintIdempotent(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	line, _ := genAuthorizedKey(t, "k")

	// First Add triggers migrateFingerprint.
	_, err := s.Add(alice.Username, "k1", line)
	require.NoError(t, err)
	// Second Add re-enters migrateFingerprint with column already present.
	line2, _ := genAuthorizedKey(t, "k2")
	_, err = s.Add(alice.Username, "k2", line2)
	require.NoError(t, err)
}
