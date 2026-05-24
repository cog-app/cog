package user_test

import (
	"errors"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// --- Create / first-user-is-admin ---

func TestCreate_FirstUserIsAdmin(t *testing.T) {
	env := cogtest.New(t)
	u, err := env.Providers.Users.Create("alice", "pass")
	require.NoError(t, err)
	require.True(t, u.IsAdmin)
}

func TestCreate_SubsequentUsersAreNotAdmin(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.Create("first", "pass")
	require.NoError(t, err)

	u2, err := env.Providers.Users.Create("second", "pass")
	require.NoError(t, err)
	require.False(t, u2.IsAdmin)
}

func TestCreate_DuplicateUsername(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.Create("alice", "pass")
	require.NoError(t, err)
	_, err = env.Providers.Users.Create("alice", "other")
	require.True(t, err != nil)
}

// --- Authenticate ---

func TestAuthenticate_HappyPath(t *testing.T) {
	env := cogtest.New(t)
	created, err := env.Providers.Users.Create("alice", "hunter2")
	require.NoError(t, err)

	got, err := env.Providers.Users.Authenticate("alice", "hunter2")
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
}

func TestAuthenticate_WrongPassword(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.Create("alice", "right")
	require.NoError(t, err)

	_, err = env.Providers.Users.Authenticate("alice", "wrong")
	require.True(t, errors.Is(err, user.ErrAuthFailed))
}

func TestAuthenticate_UnknownUser_ReturnsAuthFailed(t *testing.T) {
	env := cogtest.New(t)
	// No ErrNotFound leak — unknown user looks identical to wrong password.
	_, err := env.Providers.Users.Authenticate("never-existed", "pass")
	require.True(t, errors.Is(err, user.ErrAuthFailed))
}

// --- GetByUsername / GetByID ---

func TestGetByUsername_Missing(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.GetByUsername("nope")
	require.True(t, errors.Is(err, user.ErrNotFound))
}

func TestGetByID_Missing(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.GetByID("does-not-exist")
	require.True(t, errors.Is(err, user.ErrNotFound))
}

// --- Update ---

func TestUpdate_DisplayName(t *testing.T) {
	env := cogtest.New(t)
	u, err := env.Providers.Users.Create("alice", "pass")
	require.NoError(t, err)

	dn := "Alice in Wonderland"
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{DisplayName: &dn}))

	got, _ := env.Providers.Users.GetByID(u.ID)
	require.Equal(t, dn, got.DisplayName)
}

func TestUpdate_IsAdminToggle(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.Create("first", "pass") // becomes admin
	require.NoError(t, err)
	u, err := env.Providers.Users.Create("alice", "pass")
	require.NoError(t, err)
	require.False(t, u.IsAdmin)

	yes := true
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &yes}))
	got, _ := env.Providers.Users.GetByID(u.ID)
	require.True(t, got.IsAdmin)

	no := false
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &no}))
	got, _ = env.Providers.Users.GetByID(u.ID)
	require.False(t, got.IsAdmin)
}

func TestUpdate_Email(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "pass")

	em := "alice@example.com"
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{Email: &em}))
	got, _ := env.Providers.Users.GetByID(u.ID)
	require.Equal(t, em, got.Email)
}

func TestUpdate_AvatarBumpsTimestamp(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "pass")

	require.Equal(t, int64(0), u.AvatarUpdatedAt)
	av := "alice.png"
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{Avatar: &av}))

	got, _ := env.Providers.Users.GetByID(u.ID)
	require.Equal(t, av, got.Avatar)
	require.True(t, got.AvatarUpdatedAt > 0)
}

func TestUpdate_NoOpZeroOpts(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "pass")

	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{}))
	// User unchanged.
	got, _ := env.Providers.Users.GetByID(u.ID)
	require.Equal(t, u.Username, got.Username)
}

// --- UpdatePassword ---

func TestUpdatePassword(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "old")

	require.NoError(t, env.Providers.Users.UpdatePassword(u.ID, "new"))

	// Old password rejected.
	_, err := env.Providers.Users.Authenticate("alice", "old")
	require.True(t, err != nil)

	// New password accepted.
	got, err := env.Providers.Users.Authenticate("alice", "new")
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)
}

// --- SetMFAMethod ---

func TestSetMFAMethod_Roundtrip(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "pass")

	m, err := env.Providers.Users.SetMFAMethod(u.ID, "totp")
	require.NoError(t, err)
	require.Equal(t, "totp", m)

	got, _ := env.Providers.Users.GetByID(u.ID)
	require.Equal(t, "totp", got.MFAMethod)

	// Clear.
	_, err = env.Providers.Users.SetMFAMethod(u.ID, "")
	require.NoError(t, err)
	got, _ = env.Providers.Users.GetByID(u.ID)
	require.Equal(t, "", got.MFAMethod)
}

// --- Usernames / List / Count ---

func TestUsernames_SortedAlphabetically(t *testing.T) {
	env := cogtest.New(t)
	_, _ = env.Providers.Users.Create("charlie", "p")
	_, _ = env.Providers.Users.Create("alice", "p")
	_, _ = env.Providers.Users.Create("bob", "p")

	got, err := env.Providers.Users.Usernames()
	require.NoError(t, err)
	require.Equal(t, 3, len(got))
	require.Equal(t, "alice", got[0])
	require.Equal(t, "bob", got[1])
	require.Equal(t, "charlie", got[2])
}

func TestList_IteratesAll(t *testing.T) {
	env := cogtest.New(t)
	_, _ = env.Providers.Users.Create("alice", "p")
	_, _ = env.Providers.Users.Create("bob", "p")

	var n int
	for u, err := range env.Providers.Users.List() {
		require.NoError(t, err)
		require.True(t, u.Username != "")
		n++
	}
	require.Equal(t, 2, n)
}

func TestCount(t *testing.T) {
	env := cogtest.New(t)
	n, err := env.Providers.Users.Count()
	require.NoError(t, err)
	require.Equal(t, 0, n)

	_, _ = env.Providers.Users.Create("alice", "p")
	_, _ = env.Providers.Users.Create("bob", "p")

	n, _ = env.Providers.Users.Count()
	require.Equal(t, 2, n)
}

// --- AvatarURL ---

func TestAvatarURL_NoVersionWhenUnset(t *testing.T) {
	u := &user.User{ID: "abc"}
	require.Equal(t, "/avatar/abc", u.AvatarURL())
}

func TestAvatarURL_BumpsVersionWhenSet(t *testing.T) {
	u := &user.User{ID: "abc", AvatarUpdatedAt: 1700000000}
	require.Equal(t, "/avatar/abc?v=1700000000", u.AvatarURL())
}

// --- Delete cleans related rows ---

func TestDelete_CleansSessionsAndAppPasswords(t *testing.T) {
	env := cogtest.New(t)
	u, _ := env.Providers.Users.Create("alice", "pass")

	// Create some related rows.
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	_, _, err = env.Providers.AppPasswords.Create(u.ID, "test")
	require.NoError(t, err)

	require.NoError(t, env.Providers.Users.Delete(u.ID))

	// Session no longer validates.
	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)

	// User gone.
	_, err = env.Providers.Users.GetByID(u.ID)
	require.True(t, errors.Is(err, user.ErrNotFound))
}
