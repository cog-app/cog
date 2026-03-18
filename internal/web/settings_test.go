package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSettingsShowSecurity(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/security", cookie).Status(200)
}

func TestSettingsShowProfile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/profile", cookie).Status(200).Contains("alice")
}

func TestSettingsUpdateProfile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/profile", url.Values{
		"csrf_token":   {token},
		"display_name": {"Alice W"},
		"email":        {"alice@example.com"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "Alice W", u.DisplayName)
	require.Equal(t, "alice@example.com", u.Email)
}

func TestSettingsCreateMount(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.PostForm(t, "/settings/mounts", url.Values{
		"csrf_token":     {token},
		"volume_id":      {vol.ID},
		"user_path":      {"/extra"},
		"volume_subpath": {"extra"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify mount was created.
	var found bool
	for m, err := range env.Providers.Mounts.ListByUser(alice.ID) {
		require.NoError(t, err)
		if m.UserPath == "/extra" {
			found = true
		}
	}
	require.True(t, found, "mount at /extra should exist")
}

func TestSettingsDeleteMount(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	m, err := env.Providers.Mounts.Create(alice.ID, "/extra", vol.ID, "extra", false)
	require.NoError(t, err)

	env.Delete(t, "/settings/mounts/"+m.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Mounts.GetByID(m.ID)
	require.Error(t, err)
}

func TestSettingsDeleteRootMountFails(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	// Create a root mount manually (cogtest users don't get one by default).
	root, err := env.Providers.Mounts.Create(alice.ID, "/", vol.ID, alice.Username+"/files", false)
	require.NoError(t, err)

	env.Delete(t, "/settings/mounts/"+root.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(400)
}

func TestSettingsCreateUserVolume(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"my-remote"},
		"remote_url": {"https://dav.example.com/files/"},
		"auth_type":  {"basic"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	v, err := env.Providers.Volumes.GetByName("my-remote")
	require.NoError(t, err)
	require.NotNil(t, v.OwnerUserID)
	require.Equal(t, alice.ID, *v.OwnerUserID)
}

func TestSettingsDeleteUserVolume(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	v, err := env.Providers.Volumes.Create("alice-vol", "", "webdav://x", &alice.ID, "{}")
	require.NoError(t, err)

	env.Delete(t, "/settings/volumes/"+v.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Volumes.GetByID(v.ID)
	require.Error(t, err)
}

func TestSettingsDeleteOtherUserVolumeForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	bobCookie, bobToken := sessionAndCSRF(t, env, bob.ID)

	v, err := env.Providers.Volumes.Create("alice-vol", "", "webdav://x", &alice.ID, "{}")
	require.NoError(t, err)

	env.Delete(t, "/settings/volumes/"+v.ID,
		bobCookie, cogtest.Header("X-CSRF-Token", bobToken)).Status(403)
}
