package gallery_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func embedding(v ...float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func TestSharedPeopleList(t *testing.T) {
	e := newShareEnv(t)
	e.scan(t)
	photo := e.mediaID(t, "/Share/Photos/a.jpg")
	_, err := e.db.Exec(`UPDATE media SET recognition_state = 1, recognition_at = 1000 WHERE id = ?`, photo)
	require.NoError(t, err)
	res, err := e.db.Exec(`INSERT INTO people (name) VALUES ('Mum')`)
	require.NoError(t, err)
	mum, err := res.LastInsertId()
	require.NoError(t, err)
	addFace := func(person any, emb []byte) int64 {
		res, err := e.db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, embedding, confidence, quality)
			VALUES (?, ?, 0.1, 0.1, 0.2, 0.2, ?, 0.9, 0.9)`, photo, person, emb)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	mumsFace := addFace(mum, embedding(1, 0, 0, 0))
	strangersFace := addFace(nil, embedding(0, 1, 0, 0))
	_, err = e.fp.Upload("bob", "/Photos/mine.jpg", bytes.NewReader(photoJPEG(t, 3, 320, 240)))
	require.NoError(t, err)

	aliceCookie, aliceCSRF := sessionAuth(t, e.Env, e.alice)
	e.PostForm(t, "/api/shares/create", url.Values{
		"resource_type": {"gallery:people"}, "resource_id": {"1"},
		"subject_type": {"USER"}, "subject_id": {e.bob.ID},
		"permissions": {fmt.Sprint(plugin.PermRead | plugin.PermWrite)},
	}, aliceCookie, aliceCSRF).Status(200)

	e.scanBob(t)
	var mine int64
	require.NoError(t, e.bobDB.QueryRow(`SELECT id FROM media WHERE file_path = '/Photos/mine.jpg'`).Scan(&mine))
	_, err = e.bobDB.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, quality)
		VALUES (?, 0.1, 0.1, 0.2, 0.2, ?, 0.9, 0.9)`, mine, embedding(0.99, 0.01, 0, 0))
	require.NoError(t, err)
	e.scanBob(t)

	var mirror int64
	require.NoError(t, e.bobDB.QueryRow(`SELECT id FROM people WHERE linked_from = 'alice' AND linked_id = ? AND name = 'Mum'`, mum).Scan(&mirror))
	personOf := func(where string, args ...any) any {
		t.Helper()
		var p *int64
		require.NoError(t, e.bobDB.QueryRow(`SELECT person_id FROM faces WHERE `+where, args...).Scan(&p))
		if p == nil {
			return nil
		}
		return *p
	}
	require.Equal(t, any(mirror), personOf(`owner_face_id = ?`, mumsFace))
	require.Equal(t, nil, personOf(`owner_face_id = ?`, strangersFace)) // alice has not named them; bob's clustering leaves them
	require.Equal(t, any(mirror), personOf(`media_id = ?`, mine))

	// bob may change alice's list: a name he gives reaches hers.
	bobCookie, bobCSRF := sessionAuth(t, e.Env, e.bob)
	e.PostForm(t, fmt.Sprintf("/apps/gallery/people/%d/name", mirror), url.Values{"name": {"Mom"}},
		bobCookie, bobCSRF, cogtest.StopRedirect).Status(303)
	var name string
	require.NoError(t, e.db.QueryRow(`SELECT name FROM people WHERE id = ?`, mum).Scan(&name))
	require.Equal(t, "Mom", name)
	require.NoError(t, e.bobDB.QueryRow(`SELECT name FROM people WHERE id = ?`, mirror).Scan(&name))
	require.Equal(t, "Mom", name)

	// Read only, he may not.
	for rec, err := range e.Providers.Shares.ListBySubject(e.bob.ID) {
		require.NoError(t, err)
		if rec.ResourceType == "gallery:people" {
			require.NoError(t, e.Providers.Shares.Delete(rec.ID))
		}
	}
	_, err = e.Providers.Shares.Create(e.alice.ID, "gallery:people", "1", "USER", e.bob.ID, plugin.PermRead)
	require.NoError(t, err)
	e.PostForm(t, fmt.Sprintf("/apps/gallery/people/%d/name", mirror), url.Values{"name": {"Mother"}},
		bobCookie, bobCSRF, cogtest.StopRedirect).Status(303)
	require.NoError(t, e.db.QueryRow(`SELECT name FROM people WHERE id = ?`, mum).Scan(&name))
	require.Equal(t, "Mom", name)

	// Unshared, bob keeps his people as his own.
	for rec, err := range e.Providers.Shares.ListBySubject(e.bob.ID) {
		require.NoError(t, err)
		if rec.ResourceType == "gallery:people" {
			require.NoError(t, e.Providers.Shares.Delete(rec.ID))
		}
	}
	e.scanBob(t)
	var from string
	require.NoError(t, e.bobDB.QueryRow(`SELECT linked_from, name FROM people WHERE id = ?`, mirror).Scan(&from, &name))
	require.Equal(t, "", from)
	require.Equal(t, "Mom", name)
	require.Equal(t, any(mirror), personOf(`media_id = ?`, mine))
}
