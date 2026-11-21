package gallery_test

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func TestSharedAlbum(t *testing.T) {
	e := newScanEnv(t)
	bob := e.CreateUser(t, "bob", "pass")
	carol := e.CreateUser(t, "carol", "pass")
	e.upload(t, "/Photos/in.jpg", photoJPEG(t, 1, 640, 480))
	e.upload(t, "/Photos/out.jpg", photoJPEG(t, 2, 640, 480))
	e.scan(t)
	in, out := e.mediaID(t, "/Photos/in.jpg"), e.mediaID(t, "/Photos/out.jpg")
	res, err := e.db.Exec(`INSERT INTO albums (name) VALUES ('Holiday')`)
	require.NoError(t, err)
	album, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.db.Exec(`INSERT INTO album_media (album_id, media_id) VALUES (?, ?)`, album, in)
	require.NoError(t, err)

	view := fmt.Sprintf("/apps/gallery/shared/alice/album/%d", album)
	thumb := func(media int64) string {
		return fmt.Sprintf("/apps/gallery/api/shared/alice/album/%d/media/%d/thumbnail?size=256", album, media)
	}
	e.Get(t, view, bob.Auth).Status(404)

	cookie, csrf := sessionAuth(t, e.Env, e.alice)
	e.PostForm(t, "/api/shares/create", url.Values{
		"resource_type": {"gallery:album"}, "resource_id": {fmt.Sprint(album)},
		"subject_type": {"USER"}, "subject_id": {bob.ID},
		"permissions": {fmt.Sprint(plugin.PermRead)},
	}, cookie, csrf).Status(200)

	e.Get(t, "/apps/gallery/albums", bob.Auth).Status(200).Contains("Holiday").Contains("From alice")
	e.Get(t, view, bob.Auth).Status(200).Contains("Holiday").Contains(thumb(in))
	e.Get(t, thumb(in), bob.Auth).Status(200)
	e.Get(t, thumb(out), bob.Auth).Status(404)
	e.Get(t, view, carol.Auth).Status(404)
	e.Get(t, thumb(in), carol.Auth).Status(404)

	// A share bob makes of his own album with the same ID grants nothing in alice's.
	bobDB, err := e.g.DB("bob")
	require.NoError(t, err)
	_, err = bobDB.Exec(`INSERT INTO albums (id, name) VALUES (?, 'Mine')`, album)
	require.NoError(t, err)
	_, err = e.Providers.Shares.Create(bob.ID, "gallery:album", fmt.Sprint(album), "USER", carol.ID, plugin.PermRead)
	require.NoError(t, err)
	e.Get(t, view, carol.Auth).Status(404)
	e.Get(t, fmt.Sprintf("/apps/gallery/shared/bob/album/%d", album), carol.Auth).Status(200).Contains("Mine")

	for rec, err := range e.Providers.Shares.ListBySubject(bob.ID) {
		require.NoError(t, err)
		require.NoError(t, e.Providers.Shares.Delete(rec.ID))
	}
	e.Get(t, view, bob.Auth).Status(404)
	e.Get(t, "/apps/gallery/albums", bob.Auth).Status(200).NotContains("Holiday")
}

func TestSharedAlbumInFiles(t *testing.T) {
	e := newScanEnv(t)
	bob := e.CreateUser(t, "bob", "pass")
	in := photoJPEG(t, 1, 640, 480)
	e.upload(t, "/Photos/2019/in.jpg", in)
	e.upload(t, "/Photos/out.jpg", photoJPEG(t, 2, 640, 480))
	e.scan(t)
	res, err := e.db.Exec(`INSERT INTO albums (name) VALUES ('Holiday')`)
	require.NoError(t, err)
	album, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = e.db.Exec(`INSERT INTO album_media (album_id, media_id) VALUES (?, ?)`, album, e.mediaID(t, "/Photos/2019/in.jpg"))
	require.NoError(t, err)
	aliceCookie, aliceCSRF := sessionAuth(t, e.Env, e.alice)
	e.PostForm(t, "/api/shares/create", url.Values{
		"resource_type": {"gallery:album"}, "resource_id": {fmt.Sprint(album)},
		"subject_type": {"USER"}, "subject_id": {bob.ID},
		"permissions": {fmt.Sprint(plugin.PermRead | plugin.PermWrite)},
	}, aliceCookie, aliceCSRF).Status(200)

	e.Get(t, "/apps/files/shared", bob.Auth).Status(200).Contains("Holiday").Contains("view only")
	e.Get(t, "/apps/files/shared/alice/Holiday", bob.Auth).Status(200).Contains("in.jpg").NotContains("out.jpg")
	e.Get(t, "/apps/files/shared/alice/Holiday/in.jpg?dl=1", bob.Auth).Status(200).Equals(string(in))
	e.Get(t, "/apps/files/shared/alice/Holiday/out.jpg?dl=1", bob.Auth).Status(404)

	_, err = e.Providers.Shared.Files.CreateDir("bob", "/Photos")
	require.NoError(t, err)
	bobCookie, bobCSRF := sessionAuth(t, e.Env, bob)
	e.PostForm(t, "/apps/files/shared/alice/Holiday", url.Values{"action": {"mount"}, "path": {"/Photos/Holiday"}},
		bobCookie, bobCSRF, cogtest.StopRedirect).Status(303)
	download := func(p string) *cogtest.Response {
		return e.Get(t, "/api/files/download?path="+url.QueryEscape(p), bob.Auth)
	}
	_, err = e.Providers.Shared.Files.ReconcileTree(context.Background(), "bob", "/Photos/Holiday")
	require.NoError(t, err)
	download("/Photos/Holiday/in.jpg").Status(200).Equals(string(in))
	download("/Photos/Holiday/out.jpg").Status(404)
	r := e.PostText(t, "/api/files/upload?path="+url.QueryEscape("/Photos/Holiday/new.jpg"), "x", bob.Auth)
	require.True(t, r.StatusCode >= 400, "upload into a shared album: %d", r.StatusCode)

	require.NoError(t, e.g.ScanUser(context.Background(), "bob"))
	bobDB, err := e.g.DB("bob")
	require.NoError(t, err)
	var from string
	require.NoError(t, bobDB.QueryRow(`SELECT shared_from FROM media WHERE file_path = '/Photos/Holiday/in.jpg'`).Scan(&from))
	require.Equal(t, "alice", from)

	for rec, err := range e.Providers.Shares.ListBySubject(bob.ID) {
		require.NoError(t, err)
		require.NoError(t, e.Providers.Shares.Delete(rec.ID))
	}
	download("/Photos/Holiday/in.jpg").Status(404)
	e.Get(t, "/apps/files/shared", bob.Auth).Status(200).NotContains("Holiday")
}
