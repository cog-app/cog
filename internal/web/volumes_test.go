package web_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func defaultVolumeDir(t *testing.T, env *cogtest.Env) (string, string) {
	t.Helper()
	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)
	dir := strings.TrimPrefix(vol.URI, "file://")
	return vol.ID, dir
}

func TestVolumeBrowseRoot(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	volID, _ := defaultVolumeDir(t, env)
	env.Get(t, "/volumes/"+volID+"/browse", cookie).Status(200)
}

func TestVolumeBrowseForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	bobCookie, _ := sessionAndCSRF(t, env, bob.ID)

	dir := t.TempDir()
	v, err := env.Providers.Volumes.Create("private", "", "file://"+dir, &alice.ID, "{}")
	require.NoError(t, err)

	env.Get(t, "/volumes/"+v.ID+"/browse", bobCookie).Status(403)
}

func TestVolumeMkDir(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	volID, dir := defaultVolumeDir(t, env)

	env.PostForm(t, "/volumes/"+volID+"/mkdir?path=/", url.Values{
		"csrf_token": {token},
		"name":       {"testdir"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify directory exists on disk.
	info, err := os.Stat(filepath.Join(dir, "testdir"))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestVolumeUpload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	volID, dir := defaultVolumeDir(t, env)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	w.WriteField("csrf_token", token)
	part, err := w.CreateFormFile("file", "hello.txt")
	require.NoError(t, err)
	part.Write([]byte("hello world"))
	w.Close()

	req := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/volumes/"+volID+"/upload?path=/", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	env.Do(t, req, cookie)

	// Verify file exists on disk.
	data, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello world", string(data))
}

func TestVolumeDownload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	volID, dir := defaultVolumeDir(t, env)

	// Write a file directly to disk.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dl.txt"), []byte("download me"), 0644))

	resp := env.Get(t, "/volumes/"+volID+"/download?path=/dl.txt", cookie).Status(200)
	require.Equal(t, "download me", resp.ReadBody())
}

func TestVolumeDeleteFile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	volID, dir := defaultVolumeDir(t, env)

	fpath := filepath.Join(dir, "todelete.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("bye"), 0644))

	env.Delete(t, "/volumes/"+volID+"/file?path=/todelete.txt",
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err := os.Stat(fpath)
	require.True(t, os.IsNotExist(err))
}
