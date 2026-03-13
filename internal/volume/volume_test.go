package volume_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

func TestCreateAndGetByID(t *testing.T) {
	env := cogtest.New(t)
	vol, err := env.Providers.Volumes.Create("myvol", "A test volume", "file:///data/test", nil, "{}")
	require.NoError(t, err)
	require.Equal(t, "myvol", vol.Name)

	got, err := env.Providers.Volumes.GetByID(vol.ID)
	require.NoError(t, err)
	require.Equal(t, vol.ID, got.ID)
	require.Equal(t, "myvol", got.Name)
	require.Equal(t, "A test volume", got.Description)
	require.Equal(t, "file:///data/test", got.URI)
}

func TestGetByName(t *testing.T) {
	env := cogtest.New(t)
	vol, err := env.Providers.Volumes.Create("myvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	got, err := env.Providers.Volumes.GetByName("myvol")
	require.NoError(t, err)
	require.Equal(t, vol.ID, got.ID)

	_, err = env.Providers.Volumes.GetByName("nonexistent")
	require.Error(t, err)
}

func TestParseURI(t *testing.T) {
	tests := []struct {
		raw     string
		scheme  string
		wantErr bool
	}{
		{"file:///data", "file", false},
		{"mem://", "mem", false},
		{"webdav://host/path", "webdav", false},
		{"ftp://bad", "", true},
		{"://invalid", "", true},
	}
	for _, tt := range tests {
		parsed, err := volume.ParseURI(tt.raw)
		if tt.wantErr {
			require.Error(t, err, "ParseURI(%q)", tt.raw)
			continue
		}
		require.NoError(t, err, "ParseURI(%q)", tt.raw)
		require.Equal(t, tt.scheme, parsed.Scheme, "ParseURI(%q)", tt.raw)
	}
}

func TestIsDefault(t *testing.T) {
	env := cogtest.New(t)
	// cogtest bootstraps a "default" volume automatically.
	defVol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)
	require.True(t, defVol.IsDefault())

	otherVol, err := env.Providers.Volumes.Create("other", "", "file:///data/other", nil, "{}")
	require.NoError(t, err)
	require.False(t, otherVol.IsDefault())
}

func TestDeleteInUse(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("myvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	// Create a mount referencing this volume.
	_, err = env.Providers.DB.Exec(
		`INSERT INTO mounts (id, user_id, volume_id, volume_subpath, user_path, read_only, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"mount1", admin.ID, vol.ID, "", "/files", 0, 0,
	)
	require.NoError(t, err)

	err = env.Providers.Volumes.Delete(vol.ID)
	require.Error(t, err)
	require.Equal(t, volume.ErrInUse, err)
}

func TestListAccessible(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "user1", "pass")

	// System volume (no owner).
	_, err := env.Providers.Volumes.Create("system", "", "file:///data/sys", nil, "{}")
	require.NoError(t, err)

	// Owned volume.
	_, err = env.Providers.Volumes.Create("owned", "", "file:///data/owned", &u.ID, "{}")
	require.NoError(t, err)

	// Other user's volume.
	other := env.CreateUser(t, "other", "pass")
	_, err = env.Providers.Volumes.Create("private", "", "file:///data/priv", &other.ID, "{}")
	require.NoError(t, err)

	var vols []volume.Volume
	for v, err := range env.Providers.Volumes.ListAccessible(u.ID, nil) {
		require.NoError(t, err)
		vols = append(vols, v)
	}

	// Should see default + system + owned, not other's private.
	names := make(map[string]bool)
	for _, v := range vols {
		names[v.Name] = true
	}
	require.True(t, names["system"])
	require.True(t, names["owned"])
	require.False(t, names["private"])
}

func TestDeleteDefaultVolumeHTTP(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	defVol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.Delete(t, "/admin/volumes/"+defVol.ID, cookie, cogtest.Header("X-CSRF-Token", csrf)).
		Status(http.StatusBadRequest)
}

func TestDeleteNonDefaultVolumeHTTP(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("other", "", "file:///data/other", nil, "{}")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.Delete(t, "/admin/volumes/"+vol.ID, cookie, cogtest.Header("X-CSRF-Token", csrf)).
		Status(http.StatusOK)

	// Verify deleted.
	_, err = env.Providers.Volumes.GetByID(vol.ID)
	require.Error(t, err)
}

func TestSetQuotaHTTP(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.PostForm(t, "/admin/volumes/"+vol.ID+"/quotas", url.Values{
		"csrf_token":   {csrf},
		"subject_type": {"DEFAULT"},
		"subject_id":   {""},
		"amount":       {"5"},
		"unit":         {"GB"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify quota in store.
	q, err := env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, int64(5*1024*1024*1024), q.QuotaBytes)
}

func TestDeleteQuotaHTTP(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///data/test", nil, "{}")
	require.NoError(t, err)

	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "DEFAULT", "", 1024))
	q, err := env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(admin.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	env.Delete(t, "/admin/volumes/"+vol.ID+"/quotas/"+q.ID, cookie, cogtest.Header("X-CSRF-Token", csrf)).
		Status(http.StatusOK)

	// Verify removed.
	q, err = env.Providers.Quotas.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.Nil(t, q)
}
