package web_test

import (
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSharePanel(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	// A volume of alice's own: the system volumes are shared from the admin
	// pages, not this panel.
	vol, err := env.Providers.Volumes.Create("alices", "", "mem://", &alice.ID, "{}")
	require.NoError(t, err)

	env.Get(t, "/api/shares/panel?type=files:volume&id="+vol.ID, cookie).Status(200)
}

func TestSharePanelMissingParams(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/api/shares/panel", cookie).Status(400)
}

func TestCreateShareForm(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// A volume of alice's own: the system volumes are shared from the admin
	// pages, not this panel.
	vol, err := env.Providers.Volumes.Create("alices", "", "mem://", &alice.ID, "{}")
	require.NoError(t, err)

	env.PostForm(t, "/api/shares/create", url.Values{
		"csrf_token":    {token},
		"resource_type": {"files:volume"},
		"resource_id":   {vol.ID},
		"subject_type":  {"USER"},
		"subject_id":    {bob.ID},
		"permissions":   {"1"},
	}, cookie).Status(200)

	// Verify share was created.
	var count int
	for _, err := range env.Providers.Shares.ListByResource("files:volume", vol.ID) {
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 1, count)
}

func TestCreatePublicLinkForm(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// A volume of alice's own: the system volumes are shared from the admin
	// pages, not this panel.
	vol, err := env.Providers.Volumes.Create("alices", "", "mem://", &alice.ID, "{}")
	require.NoError(t, err)

	env.PostForm(t, "/api/shares/public-link-form", url.Values{
		"csrf_token":    {token},
		"resource_type": {"files:volume"},
		"resource_id":   {vol.ID},
		"permissions":   {"1"},
	}, cookie).Status(200)

	// Verify a PUBLIC share record exists.
	var found bool
	for rec, err := range env.Providers.Shares.ListByResource("files:volume", vol.ID) {
		require.NoError(t, err)
		if rec.SubjectType == "PUBLIC" {
			found = true
		}
	}
	require.True(t, found, "expected a PUBLIC share")
}

func TestDeleteShareHTMX(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// A volume of alice's own: the system volumes are shared from the admin
	// pages, not this panel.
	vol, err := env.Providers.Volumes.Create("alices", "", "mem://", &alice.ID, "{}")
	require.NoError(t, err)

	rec, err := env.Providers.Shares.Create(alice.ID, "files:volume", vol.ID, "USER", alice.ID, 1)
	require.NoError(t, err)

	env.Delete(t, "/api/shares/"+rec.ID+"/delete",
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Shares.Get(rec.ID)
	require.Error(t, err)
}

func TestDeleteShareNotOwnerForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	bobCookie, bobToken := sessionAndCSRF(t, env, bob.ID)

	// A volume of alice's own: the system volumes are shared from the admin
	// pages, not this panel.
	vol, err := env.Providers.Volumes.Create("alices", "", "mem://", &alice.ID, "{}")
	require.NoError(t, err)

	rec, err := env.Providers.Shares.Create(alice.ID, "files:volume", vol.ID, "USER", bob.ID, 1)
	require.NoError(t, err)

	env.Delete(t, "/api/shares/"+rec.ID+"/delete",
		bobCookie, cogtest.Header("X-CSRF-Token", bobToken)).Status(403)
}

func TestUserSearch(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")

	env.Get(t, "/api/users/search?q=bob", alice.Auth).Status(200)
}

func TestPickerSearchUser(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/api/picker/search?type=user&q=bob", cookie).Status(200)
}

func TestPickerSearchGroup(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	_, err := env.Providers.Groups.Create("test-team", nil, nil)
	require.NoError(t, err)

	env.Get(t, "/api/picker/search?type=group&q=test", cookie).Status(200)
}

func TestSharePanelAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.Get(t, "/admin/shares/panel?type=files:volume&id="+vol.ID, cookie).Status(200)
}

func TestCreateShareAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.PostForm(t, "/admin/shares/create", url.Values{
		"csrf_token":    {token},
		"resource_type": {"files:volume"},
		"resource_id":   {vol.ID},
		"subject_id":    {bob.ID},
		"permissions":   {"1"},
	}, cookie).Status(200)

	var count int
	for _, err := range env.Providers.Shares.ListByResource("files:volume", vol.ID) {
		require.NoError(t, err)
		count++
	}
	require.Equal(t, 1, count)
}

func TestDeleteShareAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	rec, err := env.Providers.Shares.Create(admin.ID, "files:volume", vol.ID, "USER", admin.ID, 1)
	require.NoError(t, err)

	env.Delete(t, "/admin/shares/"+rec.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Shares.Get(rec.ID)
	require.Error(t, err)
}
