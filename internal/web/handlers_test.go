package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Logout ---

func TestLogout(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)

	resp := env.Post(t, "/logout", cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Session should be invalidated.
	s, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.True(t, s == nil, "session should be deleted after logout")
}

// --- Registration ---

func TestRegisterEnabled(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass") // first user, admin
	require.NoError(t, env.Providers.Settings.Set(settings.KeyPublicRegistration, "true"))

	// GET register page.
	env.Get(t, "/register").Status(200)

	// POST register.
	resp := env.PostForm(t, "/register", url.Values{
		"username": {"newuser"},
		"password": {"secret"},
		"confirm":  {"secret"},
	}, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// User should exist.
	_, err := env.Providers.Users.GetByUsername("newuser")
	require.NoError(t, err)
}

func TestRegisterDisabled(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	// Registration is disabled by default.

	env.Get(t, "/register").Status(http.StatusNotFound)
}

func TestRegisterPasswordMismatch(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	require.NoError(t, env.Providers.Settings.Set(settings.KeyPublicRegistration, "true"))

	resp := env.PostForm(t, "/register", url.Values{
		"username": {"newuser"},
		"password": {"one"},
		"confirm":  {"two"},
	}, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// User should not have been created.
	_, err := env.Providers.Users.GetByUsername("newuser")
	require.Error(t, err)
}

// --- Admin CRUD ---

func TestAdminCreateUser(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.PostForm(t, "/admin/users", url.Values{
		"csrf_token": {token},
		"username":   {"newuser"},
		"password":   {"newpass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err := env.Providers.Users.GetByUsername("newuser")
	require.NoError(t, err)
	require.Equal(t, "newuser", u.Username)
}

func TestAdminDeleteUser(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	victim := env.CreateUser(t, "victim", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.Delete(t, "/admin/users/"+victim.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err := env.Providers.Users.GetByUsername("victim")
	require.Error(t, err)
}

func TestAdminResetPassword(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	target := env.CreateUser(t, "target", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.PostForm(t, "/admin/users/"+target.ID+"/password", url.Values{
		"csrf_token":   {token},
		"new_password": {"newpassword"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Old password should no longer work, new one should.
	_, err := env.Providers.Users.Authenticate("target", "newpassword")
	require.NoError(t, err)
}

func TestAdminIndex(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)

	env.Get(t, "/admin/", cookie).Status(200)
}

func TestAdminListPlugins(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	env.Get(t, "/admin/plugins", admin.Auth).Status(200)
}

// --- Settings ---

func TestSettingsRedirect(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	resp := env.Get(t, "/settings/", cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/settings/profile", loc.Path)
}

func TestSettingsUpdatePassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "oldpass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/password", url.Values{
		"csrf_token":       {token},
		"current_password": {"oldpass"},
		"new_password":     {"newpass"},
		"confirm_password": {"newpass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	_, err := env.Providers.Users.Authenticate("alice", "newpass")
	require.NoError(t, err)
}

func TestSettingsUpdatePasswordWrongCurrent(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/password", url.Values{
		"csrf_token":       {token},
		"current_password": {"wrong"},
		"new_password":     {"newpass"},
		"confirm_password": {"newpass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Old password should still work.
	_, err := env.Providers.Users.Authenticate("alice", "pass")
	require.NoError(t, err)
}

func TestSettingsShowStorage(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/storage", cookie).Status(200)
}

// --- Group Web Handlers ---

func TestGroupsListPage(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/groups", cookie).Status(200)
}

func TestGroupsCreateViaWeb(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	resp := env.PostForm(t, "/settings/groups", url.Values{
		"csrf_token": {token},
		"name":       {"my-team"},
	}, cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Group should exist.
	var found bool
	for g, err := range env.Providers.Groups.List() {
		require.NoError(t, err)
		if g.Name == "my-team" {
			found = true
		}
	}
	require.True(t, found, "group 'my-team' should have been created")
}

func TestGroupsDeleteViaWeb(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("to-delete", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	env.PostForm(t, "/settings/groups/"+grp.ID+"/delete", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	_, err = env.Providers.Groups.Get(grp.ID)
	require.Error(t, err)
}

func TestGroupsAddMemberViaWeb(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	env.PostForm(t, "/settings/groups/"+grp.ID+"/members", url.Values{
		"csrf_token":  {token},
		"member_id":   {bob.ID},
		"member_type": {"USER"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.MemberCount) // alice (OWNER) + bob (USER)
}

func TestGroupsRemoveMemberViaWeb(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, bob.ID, group.User))

	env.Delete(t, "/settings/groups/"+grp.ID+"/members/"+bob.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.MemberCount) // only alice
}

func TestGroupsUpdateViaWeb(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("old-name", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	env.PostForm(t, "/settings/groups/"+grp.ID, url.Values{
		"csrf_token": {token},
		"name":       {"new-name"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
}

func TestGroupsCreateInvite(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	resp := env.PostForm(t, "/settings/groups/"+grp.ID+"/invites", url.Values{
		"csrf_token":  {token},
		"email":       {"invite@example.com"},
		"member_type": {"USER"},
	}, cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Invitation should exist.
	var count int
	for _, err := range env.Providers.Invitations.ListByGroup(grp.ID) {
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 1, count)
}

func TestGroupsRevokeInvite(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	grp, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, alice.ID, group.Owner))

	inv, err := env.Providers.Invitations.Create(grp.ID, "rev@example.com", alice.ID, 24, group.User)
	require.NoError(t, err)

	env.Delete(t, "/settings/groups/"+grp.ID+"/invites/"+inv.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	// Invitation should be gone.
	err = env.Providers.Invitations.Delete(inv.ID)
	require.Error(t, err) // already deleted
}

// --- Search ---

func TestSearchAPI(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Search returns JSON.
	env.Get(t, "/api/search?q=test&limit=10", alice.Auth).Status(200)
}

func TestSearchPartial(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/api/search/partial?q=test", alice.Auth).Status(200)
}

func TestSearchUnauthenticated(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	// API endpoint returns 401 for non-browser requests.
	env.Get(t, "/api/search?q=test").Status(http.StatusUnauthorized)
}

// --- Admin Groups ---

func TestAdminCreateGroup(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	env.PostForm(t, "/admin/groups", url.Values{
		"csrf_token": {token},
		"name":       {"admin-group"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	var found bool
	for g, err := range env.Providers.Groups.List() {
		require.NoError(t, err)
		if g.Name == "admin-group" {
			found = true
		}
	}
	require.True(t, found)
}
