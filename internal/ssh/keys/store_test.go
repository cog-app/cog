package keys_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// dbProvider adapts the plugin registry's UserDB to keys.LegacyDBProvider,
// counting the databases opened through it.
type dbProvider struct {
	env    *cogtest.Env
	opened int
}

func (p *dbProvider) UserDB(username string) (*sql.DB, error) {
	p.opened++
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
	s, err := keys.NewStore(env.Providers.DB, &dbProvider{env: env}, &usernameProvider{env}, env.Clock.Clock())
	require.NoError(t, err)
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

// --- Find ---

func TestFind_Match(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")

	_, err := s.Add(alice.Username, "laptop", line)
	require.NoError(t, err)

	key, err := s.Find("alice", fp)
	require.NoError(t, err)
	require.Equal(t, fp, key.Fingerprint)
}

func TestFind_NotFound(t *testing.T) {
	s, env := newStore(t)
	env.CreateUser(t, "alice", "pass")
	_, err := s.Find("alice", "SHA256:never-stored")
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
}

// A key finds only the user it was added for: alice's key does not log in
// as bob.
func TestFind_OnlyOwnKeys(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	aliceLine, aliceFP := genAuthorizedKey(t, "alice@laptop")
	bobLine, bobFP := genAuthorizedKey(t, "bob@laptop")

	_, err := s.Add(alice.Username, "k", aliceLine)
	require.NoError(t, err)
	_, err = s.Add(bob.Username, "k", bobLine)
	require.NoError(t, err)

	_, err = s.Find("alice", aliceFP)
	require.NoError(t, err)
	_, err = s.Find("bob", bobFP)
	require.NoError(t, err)
	_, err = s.Find("bob", aliceFP)
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
}

// Public keys are public. Adding someone else's key must not stop them
// adding it too, and only grants login as the account it was added to.
func TestAdd_SameKeyTwoUsers(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	mallory := env.CreateUser(t, "mallory", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")

	_, err := s.Add(mallory.Username, "squat", line)
	require.NoError(t, err)
	_, err = s.Add(alice.Username, "k", line)
	require.NoError(t, err)
	_, err = s.Add(alice.Username, "again", line)
	require.True(t, errors.Is(err, keys.ErrKeyExists), "got %v", err)

	_, err = s.Find("alice", fp)
	require.NoError(t, err)
	_, err = s.Find("mallory", fp)
	require.NoError(t, err)
}

func TestDelete_OtherUsersKeyRefused(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	line, fp := genAuthorizedKey(t, "k")

	k, err := s.Add(alice.Username, "k", line)
	require.NoError(t, err)
	err = s.Delete(bob.Username, k.ID)
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)

	_, err = s.Find("alice", fp)
	require.NoError(t, err)
}

func TestFind_UserDeletedRemovesKeys(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	line, fp := genAuthorizedKey(t, "k")
	_, err := s.Add(alice.Username, "k", line)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Users.Delete(alice.ID))
	var n int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE fingerprint = ?`, fp).Scan(&n))
	require.Equal(t, 0, n)
	_, err = s.Find("alice", fp)
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
}

// TestFind_NoUserDatabases checks that a login lookup is answered
// from the system database alone: with many users, not one per-user database
// is opened.
func TestFind_NoUserDatabases(t *testing.T) {
	env := cogtest.New(t)
	legacy := &dbProvider{env: env}
	s, err := keys.NewStore(env.Providers.DB, legacy, &usernameProvider{env}, env.Clock.Clock())
	require.NoError(t, err)

	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		env.CreateUser(t, name, "pass")
	}
	line, fp := genAuthorizedKey(t, "dave@laptop")
	_, err = s.Add("dave", "laptop", line)
	require.NoError(t, err)

	opened := legacy.opened
	_, err = s.Find("dave", fp)
	require.NoError(t, err)
	_, err = s.Find("dave", "SHA256:never-stored")
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
	require.Equal(t, opened, legacy.opened)
}

// --- Moving keys out of the per-user databases ---

// writeLegacyKey stores a key the way the per-user databases used to.
func writeLegacyKey(t *testing.T, env *cogtest.Env, username, id, line string) {
	t.Helper()
	db, err := env.Providers.Plugins.UserDB(username)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ssh_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		public_key TEXT NOT NULL,
		created_at INTEGER NOT NULL
	)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ssh_keys (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, id, "legacy", line, 1700000000)
	require.NoError(t, err)
}

// asUpgraded makes env's system database look like one from before keys
// moved: the server's own start already did the (empty) move and marked it.
func asUpgraded(t *testing.T, env *cogtest.Env) {
	t.Helper()
	_, err := env.Providers.DB.Exec(`DELETE FROM system_settings WHERE key = 'ssh.keys_moved'`)
	require.NoError(t, err)
}

func TestNewStore_MovesLegacyKeys(t *testing.T) {
	env := cogtest.New(t)
	asUpgraded(t, env)
	env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")
	env.CreateUser(t, "carol", "pass") // no legacy table at all

	aliceLine, aliceFP := genAuthorizedKey(t, "alice")
	bobLine, bobFP := genAuthorizedKey(t, "bob")
	writeLegacyKey(t, env, "alice", "a1", aliceLine)
	writeLegacyKey(t, env, "bob", "b1", bobLine)
	writeLegacyKey(t, env, "bob", "b2", aliceLine) // same key as alice's: both keep it
	writeLegacyKey(t, env, "bob", "b3", bobLine)   // bob's twice: one is kept
	writeLegacyKey(t, env, "bob", "b4", "not a key")

	s, err := keys.NewStore(env.Providers.DB, &dbProvider{env: env}, &usernameProvider{env}, env.Clock.Clock())
	require.NoError(t, err)

	k, err := s.Find("alice", aliceFP)
	require.NoError(t, err)
	require.Equal(t, "a1", k.ID)
	k, err = s.Find("bob", aliceFP)
	require.NoError(t, err)
	require.Equal(t, "b2", k.ID)
	k, err = s.Find("bob", bobFP)
	require.NoError(t, err)
	require.True(t, k.ID == "b1" || k.ID == "b3", "got %s", k.ID)

	got, err := s.List("bob")
	require.NoError(t, err)
	require.Equal(t, 2, len(got))

	// The move runs once: a key deleted afterwards is not brought back.
	require.NoError(t, s.Delete("bob", k.ID))
	legacy := &dbProvider{env: env}
	s, err = keys.NewStore(env.Providers.DB, legacy, &usernameProvider{env}, env.Clock.Clock())
	require.NoError(t, err)
	require.Equal(t, 0, legacy.opened)
	_, err = s.Find("bob", bobFP)
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
}

// failingDB fails to open any user's database.
type failingDB struct{}

func (failingDB) UserDB(string) (*sql.DB, error) { return nil, errors.New("disk on fire") }

func TestNewStore_MoveFailureSurfaces(t *testing.T) {
	env := cogtest.New(t)
	asUpgraded(t, env)
	env.CreateUser(t, "alice", "pass")

	_, err := keys.NewStore(env.Providers.DB, failingDB{}, &usernameProvider{env}, env.Clock.Clock())
	require.Error(t, err)

	// Nothing was marked done, so a later start tries again.
	line, fp := genAuthorizedKey(t, "alice")
	writeLegacyKey(t, env, "alice", "a1", line)
	s, err := keys.NewStore(env.Providers.DB, &dbProvider{env: env}, &usernameProvider{env}, env.Clock.Clock())
	require.NoError(t, err)
	_, err = s.Find("alice", fp)
	require.NoError(t, err)
}

func TestAdd_NameAndValidation(t *testing.T) {
	s, env := newStore(t)
	env.CreateUser(t, "alice", "pass")

	line, _ := genAuthorizedKey(t, "alice@laptop")
	k, err := s.Add("alice", "", line)
	require.NoError(t, err)
	require.Equal(t, "alice@laptop", k.Name)

	other, _ := genAuthorizedKey(t, "ignored")
	k, err = s.Add("alice", "work", other)
	require.NoError(t, err)
	require.Equal(t, "work", k.Name)

	_, err = s.Add("alice", "", "not a key")
	require.True(t, errors.Is(err, keys.ErrInvalidKey), "got %v", err)

	a, _ := genAuthorizedKey(t, "a")
	b, _ := genAuthorizedKey(t, "b")
	_, err = s.Add("alice", "", a+"\n"+b)
	require.True(t, errors.Is(err, keys.ErrInvalidKey), "got %v", err)
}

func TestOwner(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")
	_, err := s.Add(alice.Username, "laptop", line)
	require.NoError(t, err)

	owner, err := s.Owner(fp)
	require.NoError(t, err)
	require.Equal(t, "alice", owner)

	_, err = s.Owner("SHA256:never-stored")
	require.True(t, errors.Is(err, keys.ErrNotFound), "got %v", err)
}

// Mallory can add alice's public key to her own account; that must lock the
// key out of owner lookups rather than pick either of them.
func TestOwner_SharedKey(t *testing.T) {
	s, env := newStore(t)
	alice := env.CreateUser(t, "alice", "pass")
	mallory := env.CreateUser(t, "mallory", "pass")
	line, fp := genAuthorizedKey(t, "alice@laptop")
	_, err := s.Add(alice.Username, "k", line)
	require.NoError(t, err)
	_, err = s.Add(mallory.Username, "squat", line)
	require.NoError(t, err)

	_, err = s.Owner(fp)
	require.True(t, errors.Is(err, keys.ErrSharedKey), "got %v", err)
}
