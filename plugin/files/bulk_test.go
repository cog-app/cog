package files_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// bulkTree gives a user /A (with /A/sub/deep.txt), /B (with /B/y.txt),
// /x.txt (a favourite) and /y.txt.
func bulkTree(t *testing.T, fp *files.Plugin, username string) {
	t.Helper()
	require.NoError(t, fp.EnsureRootDir(username))
	for _, d := range []string{"/A", "/A/sub", "/B"} {
		_, err := fp.CreateDir(username, d)
		require.NoError(t, err)
	}
	for p, body := range map[string]string{
		"/x.txt":          "x content",
		"/y.txt":          "y at the root",
		"/B/y.txt":        "y in B",
		"/A/sub/deep.txt": strings.Repeat("deep ", 1000),
	} {
		_, err := fp.Upload(username, p, strings.NewReader(body))
		require.NoError(t, err)
	}
	require.NoError(t, fp.SetFavorite(username, "/x.txt", true))
}

func TestMove(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	env.CreateUser(t, "mover", "pass")
	bulkTree(t, fp, "mover")

	x, err := fp.GetByPath("mover", "/x.txt")
	require.NoError(t, err)

	moved, failures, err := fp.Move("mover", []string{"/x.txt", "/y.txt", "/A"}, "/B")
	require.NoError(t, err)
	require.Equal(t, []string{"/x.txt", "/A"}, moved)
	require.Equal(t, 1, len(failures))
	require.Equal(t, "/y.txt", failures[0].Path)
	assert.True(t, strings.Contains(failures[0].Err.Error(), "already has an item with that name"), "y.txt: %v", failures[0].Err)

	// The file keeps its ID and favourite; the folder brings its tree.
	got, err := fp.GetByPath("mover", "/B/x.txt")
	require.NoError(t, err)
	require.Equal(t, x.ID, got.ID)
	require.True(t, got.Favorite)
	_, err = fp.GetByPath("mover", "/B/A/sub/deep.txt")
	require.NoError(t, err)
	_, err = fp.GetByPath("mover", "/A")
	require.True(t, err != nil, "/A is still there")
	// y.txt stayed, and B's own y.txt was not replaced.
	rc, _, err := fp.Download("mover", "/B/y.txt")
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, err)
	require.Equal(t, "y in B", string(body))

	// Nothing goes into itself or where it already is, and the root stays
	// put.
	moved, failures, err = fp.Move("mover", []string{"/B", "/B/A/sub", "/"}, "/B/A")
	require.NoError(t, err)
	require.Equal(t, 0, len(moved))
	require.Equal(t, 3, len(failures))
	assert.True(t, strings.Contains(failures[0].Err.Error(), "into itself"), "B into B/A: %v", failures[0].Err)
	assert.True(t, strings.Contains(failures[1].Err.Error(), "already there"), "sub: %v", failures[1].Err)
	assert.True(t, strings.Contains(failures[2].Err.Error(), "root"), "root: %v", failures[2].Err)

	// A destination that is a file, or missing, moves nothing.
	_, _, err = fp.Move("mover", []string{"/y.txt"}, "/B/y.txt")
	require.True(t, errors.Is(err, files.ErrNotFolder), "file destination: %v", err)
	_, _, err = fp.Move("mover", []string{"/y.txt"}, "/nowhere")
	require.True(t, err != nil, "missing destination moved something")
}

// browserSession is a session cookie and the CSRF token for its forms, with
// a cookie jar so a redirect's flash reaches the page it lands on.
func browserSession(t *testing.T, env *cogtest.Env, u *cogtest.User) (cookie cogtest.HTTPOpt, token string) {
	t.Helper()
	env.WithCookieJar(t)
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	return cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
}

func TestMoveForm(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	u := env.CreateUser(t, "mover", "pass")
	bulkTree(t, fp, "mover")
	cookie, token := browserSession(t, env, u)

	// Partly successful: the flash says both what moved and what did not,
	// and the browser comes back to the folder the items were in.
	resp := env.PostForm(t, "/apps/files/?move=1", url.Values{
		"csrf_token": {token},
		"path":       {"/x.txt", "/y.txt"},
		"dest":       {"/B"},
	}, cookie).Status(http.StatusOK)
	require.Equal(t, "/apps/files/", resp.Request.URL.Path)
	resp.Contains("Moved “x.txt” to “B”. Could not move “y.txt” (“B” already has an item with that name).")

	resp = env.PostForm(t, "/apps/files/?move=1", url.Values{
		"csrf_token": {token},
		"path":       {"/A", "/y.txt"},
		"dest":       {"/B"},
	}, cookie).Status(http.StatusOK)
	resp.NotContains("Moved 2 items")
	resp.Contains("Moved “A” to “B”. Could not move")

	resp = env.PostForm(t, "/apps/files/B?move=1", url.Values{
		"csrf_token": {token},
		"path":       {"/B/x.txt", "/B/A"},
		"dest":       {"/"},
	}, cookie).Status(http.StatusOK)
	require.Equal(t, "/apps/files/B", resp.Request.URL.Path)
	resp.Contains("Moved 2 items to “Files”.")

	env.PostForm(t, "/apps/files/?move=1", url.Values{
		"csrf_token": {token},
		"path":       {"/y.txt"},
		"dest":       {"/missing"},
	}, cookie).Status(http.StatusOK).Contains("Nothing was moved to “missing”")
}

func TestFolderPicker(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	u := env.CreateUser(t, "picker", "pass")
	bulkTree(t, fp, "picker")

	// The folder the selection is in: both folders listed, the selected one
	// refused, and the submit disabled because that is where it already is.
	body := env.Get(t, "/apps/files/?folders=1&path=%2FA", u.Auth).Status(http.StatusOK).ReadBody()
	require.True(t, !strings.Contains(body, "<html"), "the picker is a fragment, not a page")
	require.True(t, strings.Contains(body, `<input type="hidden" name="dest" value="/">`), "dest: %s", body)
	require.True(t, strings.Contains(body, "Being moved"), "selected folder not marked: %s", body)
	require.True(t, strings.Contains(body, "It is already in this folder."), "no note: %s", body)
	require.True(t, strings.Contains(body, `id="files-move-submit" hx-swap-oob="true" disabled`), "submit not disabled: %s", body)
	require.True(t, !strings.Contains(body, "x.txt"), "files listed in the picker: %s", body)

	// Another folder can be chosen.
	body = env.Get(t, "/apps/files/B?folders=1&path=%2FA", u.Auth).Status(http.StatusOK).ReadBody()
	require.True(t, strings.Contains(body, `value="/B"`), "dest: %s", body)
	require.True(t, strings.Contains(body, "No folders in here."), "empty folder: %s", body)
	require.True(t, strings.Contains(body, `id="files-move-submit" hx-swap-oob="true">`), "submit disabled: %s", body)
	require.True(t, strings.Contains(body, `aria-label="Up to Files"`), "no way up: %s", body)
}

func TestZipDownload(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	u := env.CreateUser(t, "zipper", "pass")
	bulkTree(t, fp, "zipper")

	resp := env.Get(t, "/apps/files/?zip=1&path=%2FA&path=%2Fx.txt", u.Auth).Status(http.StatusOK)
	require.Equal(t, "application/zip", resp.Header.Get("Content-Type"))
	require.Equal(t, `attachment; filename=Files.zip`, resp.Header.Get("Content-Disposition"))
	data := []byte(resp.ReadBody())
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	contents := map[string]string{}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		require.NoError(t, err)
		b, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
		contents[f.Name] = string(b)
	}
	sort.Strings(names)
	require.Equal(t, []string{"A/", "A/sub/", "A/sub/deep.txt", "x.txt"}, names)
	require.Equal(t, "x content", contents["x.txt"])
	require.Equal(t, strings.Repeat("deep ", 1000), contents["A/sub/deep.txt"])

	// One folder is named after itself; a name outside ASCII survives.
	_, err = fp.CreateDir("zipper", "/Fotos – été")
	require.NoError(t, err)
	resp = env.Get(t, "/apps/files/?zip=1&path="+url.QueryEscape("/Fotos – été"), u.Auth).Status(http.StatusOK)
	require.Equal(t, `attachment; filename*=utf-8''Fotos%20%E2%80%93%20%C3%A9t%C3%A9.zip`, resp.Header.Get("Content-Disposition"))

	// One file is sent as itself.
	resp = env.Get(t, "/apps/files/B?zip=1&path=%2FB%2Fy.txt", u.Auth).Status(http.StatusOK)
	require.Equal(t, "y in B", resp.ReadBody())

	// A missing path goes back to the folder with an error.
	resp = env.Get(t, "/apps/files/?zip=1&path=%2Fgone.txt&path=%2Fx.txt", u.Auth, cogtest.StopRedirect).Status(http.StatusSeeOther)
	require.Equal(t, "/apps/files/", resp.Header.Get("Location"))
}
