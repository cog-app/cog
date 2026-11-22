package gallery_test

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/vfs"
)

// renameEnv is a scanned gallery with a session that can post forms.
type renameEnv struct {
	*scanEnv
	cookie, csrf cogtest.HTTPOpt
	photos       map[string][]byte
}

func newRenameEnv(t *testing.T, paths ...string) *renameEnv {
	t.Helper()
	e := &renameEnv{scanEnv: newScanEnv(t), photos: map[string][]byte{}}
	e.cookie, e.csrf = sessionAuth(t, e.Env, e.alice)
	for i, p := range paths {
		e.photos[p] = photoJPEG(t, uint64(i+1), 64, 48)
		e.upload(t, p, e.photos[p])
	}
	e.scan(t)
	return e
}

func (e *renameEnv) preview(t *testing.T, pattern string, policy gallery.ConflictPolicy) *cogtest.Response {
	t.Helper()
	q := url.Values{"pattern": {pattern}, "conflicts": {string(policy)}}
	return e.Get(t, "/apps/gallery/rename/preview?"+q.Encode(), e.alice.Auth).Status(200)
}

func (e *renameEnv) apply(t *testing.T, pattern string, policy gallery.ConflictPolicy) {
	t.Helper()
	e.PostForm(t, "/apps/gallery/rename/apply", url.Values{"pattern": {pattern}, "conflicts": {string(policy)}},
		e.cookie, e.csrf, cogtest.StopRedirect).Status(303)
}

func (e *renameEnv) undo(t *testing.T) {
	t.Helper()
	e.PostForm(t, "/apps/gallery/rename/undo", url.Values{}, e.cookie, e.csrf, cogtest.StopRedirect).Status(303)
}

// holds asserts that path holds the photo first uploaded as from.
func (e *renameEnv) holds(t *testing.T, path, from string) {
	t.Helper()
	got := e.Get(t, "/remote.php/dav/files/alice"+path, e.alice.Auth).Status(200).ReadBody()
	if got == string(e.photos[from]) {
		return
	}
	for p, data := range e.photos {
		if got == string(data) {
			t.Fatalf("%s holds the photo uploaded as %s, want %s", path, p, from)
		}
	}
	t.Fatalf("%s holds none of the uploaded photos, want %s", path, from)
}

func (e *renameEnv) mediaPath(t *testing.T, id int64) string {
	t.Helper()
	var p string
	require.NoError(t, e.db.QueryRow(`SELECT file_path FROM media WHERE id = ?`, id).Scan(&p))
	return p
}

// Before anything is applied the offered pattern keeps photos inside the
// first scan folder; once one is applied, it and its conflict policy are
// what the settings page offers.
func TestRenameRemembersPattern(t *testing.T) {
	e := newRenameEnv(t, "/Photos/a.jpg")
	e.Get(t, "/apps/gallery/settings", e.alice.Auth).Status(200).
		Contains(`value="Photos/{{.Year}}/{{.Month}}/`)

	require.NoError(t, gallery.SettingScanFolders.Set(e.db, "Camera/*.jpg\nPhotos/"))
	e.Get(t, "/apps/gallery/settings", e.alice.Auth).Status(200).
		Contains(`value="Camera/{{.Year}}/{{.Month}}/`)
	require.NoError(t, gallery.SettingScanFolders.Set(e.db, "Photos/"))

	e.apply(t, "Photos/x/{{.Original}}{{.Ext}}", gallery.ConflictSkip)
	e.Get(t, "/apps/gallery/settings", e.alice.Auth).Status(200).
		Contains(`value="Photos/x/{{.Original}}{{.Ext}}"`).
		Contains(`<option value="skip" selected>`)
}

// A name another file already has is never overwritten: it gets a number,
// or the photo stays put, as asked.
func TestRenameConflictWithExistingFile(t *testing.T) {
	for _, tc := range []struct {
		policy gallery.ConflictPolicy
		want   string // where /Photos/in/a.jpg ends up
	}{
		{gallery.ConflictNumber, "/Photos/out/a_1.jpg"},
		{gallery.ConflictSkip, "/Photos/in/a.jpg"},
	} {
		t.Run(string(tc.policy), func(t *testing.T) {
			e := newRenameEnv(t, "/Photos/in/a.jpg", "/Photos/out/a.jpg")
			id := e.mediaID(t, "/Photos/in/a.jpg")
			pattern := "Photos/out/{{.Original}}{{.Ext}}"

			res := e.preview(t, pattern, tc.policy)
			if tc.policy == gallery.ConflictSkip {
				res.Contains("1 file left alone").Contains("“/Photos/out/a.jpg” is already taken.")
			} else {
				res.Contains("/Photos/out/a_1.jpg").Contains("numbered")
			}

			e.apply(t, pattern, tc.policy)
			e.holds(t, "/Photos/out/a.jpg", "/Photos/out/a.jpg")
			e.holds(t, tc.want, "/Photos/in/a.jpg")
			require.Equal(t, tc.want, e.mediaPath(t, id))
		})
	}
}

// Two photos trading names is a cycle: neither can move first without
// landing on the other. One goes through a temporary name, and undo puts
// both back.
func TestRenameSwap(t *testing.T) {
	e := newRenameEnv(t, "/Photos/a.jpg", "/Photos/b.jpg")
	pattern := `Photos/{{if eq .Original "a"}}b{{else}}a{{end}}{{.Ext}}`

	e.preview(t, pattern, gallery.ConflictNumber).Contains("2 files will be renamed").NotContains("numbered")
	e.apply(t, pattern, gallery.ConflictNumber)
	e.holds(t, "/Photos/a.jpg", "/Photos/b.jpg")
	e.holds(t, "/Photos/b.jpg", "/Photos/a.jpg")

	e.undo(t)
	e.holds(t, "/Photos/a.jpg", "/Photos/a.jpg")
	e.holds(t, "/Photos/b.jpg", "/Photos/b.jpg")
}

// A file moves onto a name another file is leaving in the same rename.
func TestRenameChain(t *testing.T) {
	e := newRenameEnv(t, "/Photos/1.jpg", "/Photos/2.jpg")
	// 1 → 2 and 2 → 3: 2 must go first.
	pattern := `Photos/{{if eq .Original "1"}}2{{else}}3{{end}}{{.Ext}}`
	e.apply(t, pattern, gallery.ConflictSkip)
	e.holds(t, "/Photos/2.jpg", "/Photos/1.jpg")
	e.holds(t, "/Photos/3.jpg", "/Photos/2.jpg")
}

// What the rename cannot or should not touch is listed with the reason,
// not silently dropped.
func TestRenameExplainsWhatItLeaves(t *testing.T) {
	e := newRenameEnv(t, "/Photos/a.jpg")
	e.preview(t, "Elsewhere/{{.Original}}{{.Ext}}", gallery.ConflictNumber).
		Contains("1 file left alone").
		Contains("is outside the scan folders")
	e.preview(t, "Photos/{{.Nope}}", gallery.ConflictNumber).
		Contains("The pattern failed")
	e.preview(t, "Photos/../../{{.Original}}{{.Ext}}", gallery.ConflictNumber).
		Contains("which is not a file name")

	// Uploaded since the last scan: the gallery has no row for it yet.
	e.upload(t, "/Photos/new.jpg", photoJPEG(t, 9, 64, 48))
	e.preview(t, "Photos/x/{{.Original}}{{.Ext}}", gallery.ConflictNumber).
		Contains("1 file in the scan folders has not been scanned yet")

	// Nothing outside the folders moved.
	e.holds(t, "/Photos/a.jpg", "/Photos/a.jpg")
}

// diskPath is where p, a path in alice's files, is on the local disk.
func (e *renameEnv) diskPath(t *testing.T, p string) string {
	t.Helper()
	fsys, err := e.Providers.Plugins.UserFS(e.alice.Username)
	require.NoError(t, err)
	u, ok := fsys.(vfs.Unwrapper)
	require.True(t, ok, "user FS %T does not unwrap", fsys)
	backend, sub, err := u.Unwrap(p)
	require.NoError(t, err)
	lr, ok := backend.(vfs.LocalRooter)
	require.True(t, ok, "backend %T is not local", backend)
	return filepath.Join(lr.LocalRoot(), filepath.FromSlash(sub))
}

// A file written straight to disk is unknown to the file index, so the
// plan thinks its name is free. The move itself finds it there and leaves
// both files alone.
func TestRenameKeepsFileTheIndexHasNotSeen(t *testing.T) {
	e := newRenameEnv(t, "/Photos/a.jpg")
	id := e.mediaID(t, "/Photos/a.jpg")
	intruder := e.diskPath(t, "/Photos/out/a.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(intruder), 0o700))
	require.NoError(t, os.WriteFile(intruder, []byte("written behind the index's back"), 0o600))

	pattern := "Photos/out/{{.Original}}{{.Ext}}"
	e.preview(t, pattern, gallery.ConflictNumber).Contains("1 file will be renamed")
	e.apply(t, pattern, gallery.ConflictNumber)

	got, err := os.ReadFile(intruder)
	require.NoError(t, err)
	require.Equal(t, "written behind the index's back", string(got))
	e.holds(t, "/Photos/a.jpg", "/Photos/a.jpg")
	require.Equal(t, "/Photos/a.jpg", e.mediaPath(t, id))
}
