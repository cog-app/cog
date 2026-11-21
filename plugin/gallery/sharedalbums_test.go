package gallery_test

import (
	"fmt"
	"net/url"
	"testing"

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
