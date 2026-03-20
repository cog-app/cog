package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestAdminListVolumes(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/volumes", cookie).Status(200)
}

func TestAdminNewVolumeForm(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/admin/volumes/new", cookie).Status(200)
}

func TestAdminCreateVolume(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	dir := t.TempDir()
	env.PostForm(t, "/admin/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"testvol"},
		"conn_type":  {"file"},
		"file_path":  {dir},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	v, err := env.Providers.Volumes.GetByName("testvol")
	require.NoError(t, err)
	require.Equal(t, "testvol", v.Name)
}

func TestAdminCreateVolumeValidation(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	resp := env.PostForm(t, "/admin/volumes", url.Values{
		"csrf_token": {token},
		"name":       {""},
		"uri":        {""},
	}, cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/admin/volumes/new", loc.Path)
}

func TestAdminEditVolumeForm(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.Get(t, "/admin/volumes/"+vol.ID, cookie).Status(200)
}

func TestAdminUpdateVolume(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	dir := t.TempDir()
	v, err := env.Providers.Volumes.Create("myvol", "desc", "file://"+dir, nil, "{}")
	require.NoError(t, err)

	env.PostForm(t, "/admin/volumes/"+v.ID, url.Values{
		"csrf_token":  {token},
		"name":        {"renamed"},
		"description": {"new desc"},
		"conn_type":   {"file"},
		"file_path":   {dir},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	got, err := env.Providers.Volumes.GetByID(v.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
}

func TestAdminDeleteVolume(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	dir := t.TempDir()
	v, err := env.Providers.Volumes.Create("todelete", "desc", "file://"+dir, nil, "{}")
	require.NoError(t, err)

	env.Delete(t, "/admin/volumes/"+v.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Volumes.GetByID(v.ID)
	require.Error(t, err)
}

func TestAdminCreateVolumeInvalidPath(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	// Non-existent path should fail.
	resp := env.PostForm(t, "/admin/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"bad"},
		"conn_type":  {"file"},
		"file_path":  {"/nonexistent/path/xyz"},
	}, cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/admin/volumes/new", loc.Path)

	// Relative path should fail.
	resp = env.PostForm(t, "/admin/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"bad"},
		"conn_type":  {"file"},
		"file_path":  {"relative/path"},
	}, cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

func TestAdminDirSuggest(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)

	// Suggest should return 200 with some results for /.
	env.Get(t, "/api/admin/dir-suggest?file_path=/", cookie).Status(200)

	// Suggest should return 200 for a non-existent path (empty results).
	env.Get(t, "/api/admin/dir-suggest?file_path=/nonexistent/zzz", cookie).Status(200)
}

func TestAdminDeleteDefaultVolumeFails(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.Delete(t, "/admin/volumes/"+vol.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(400)
}
