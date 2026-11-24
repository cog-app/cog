package gallery_test

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

func scaledJPEG(t *testing.T, src []byte, w, h int) []byte {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(src))
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, imaging.Resize(img, w, h), &jpeg.Options{Quality: 85}))
	return buf.Bytes()
}

// groupKinds maps each grouped file to its group's kind, and says which
// file each group keeps.
func (e *scanEnv) groupKinds(t *testing.T) (kinds map[string]gallery.DuplicateKind, keeps map[string]bool) {
	t.Helper()
	rows, err := e.db.Query(`SELECT m.file_path, g.kind, dm.is_keep FROM duplicate_members dm
		JOIN duplicate_groups g ON g.id = dm.group_id JOIN media m ON m.id = dm.media_id`)
	require.NoError(t, err)
	defer rows.Close()
	kinds, keeps = map[string]gallery.DuplicateKind{}, map[string]bool{}
	for rows.Next() {
		var p string
		var k gallery.DuplicateKind
		var keep bool
		require.NoError(t, rows.Scan(&p, &k, &keep))
		if _, seen := kinds[p]; seen && k != gallery.DuplicateIdentical {
			p += " (again)"
		}
		kinds[p] = k
		keeps[p] = keeps[p] || keep
	}
	require.NoError(t, rows.Err())
	return kinds, keeps
}

// A scan sorts copies by kind: the same bytes under two names, and the
// same picture scaled down. Deleting identical copies keeps the original
// name and trashes the copy, which leaves the scaled one for review.
func TestDuplicatesByKindAndDeleteIdentical(t *testing.T) {
	e := newScanEnv(t)
	cookie, csrf := sessionAuth(t, e.Env, e.alice)
	photo := photoJPEG(t, 7, 320, 240)
	e.upload(t, "/Photos/IMG_2024.jpg", photo)
	e.upload(t, "/Photos/copy/IMG_2024_1.jpg", photo)
	e.upload(t, "/Photos/small.jpg", scaledJPEG(t, photo, 160, 120))
	e.upload(t, "/Photos/other.jpg", photoJPEG(t, 99, 320, 240))
	e.scan(t)

	kinds, keeps := e.groupKinds(t)
	assert.Equal(t, gallery.DuplicateIdentical, kinds["/Photos/copy/IMG_2024_1.jpg"])
	assert.False(t, keeps["/Photos/copy/IMG_2024_1.jpg"], "the copy's name is kept")
	assert.Equal(t, gallery.DuplicateSmaller, kinds["/Photos/small.jpg"])
	// These test photos are all one gradient, so their fingerprints match:
	// the different one may look similar, but is no copy.
	if k, ok := kinds["/Photos/other.jpg"]; ok {
		assert.Equal(t, gallery.DuplicateSimilar, k)
	}

	e.Get(t, "/apps/gallery/duplicates?kind=identical", e.alice.Auth).Status(200).
		Contains("1 identical copy").Contains("Delete all identical copies")
	e.PostForm(t, "/apps/gallery/duplicates/identical/delete", url.Values{}, cookie, csrf, cogtest.StopRedirect).Status(303)
	deadline := time.Now().Add(20 * time.Second)
	for {
		job, ok := e.g.DedupeStatus(e.alice.Username)
		require.True(t, ok, "no deletion started")
		if !job.Running() {
			require.Equal(t, "", job.Err)
			require.Equal(t, 1, job.Done)
			require.Equal(t, 0, job.Failed)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deletion still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Get(t, "/remote.php/dav/files/alice/Photos/copy/IMG_2024_1.jpg", e.alice.Auth).Status(404)
	e.Get(t, "/remote.php/dav/files/alice/Photos/IMG_2024.jpg", e.alice.Auth).Status(200)

	e.scan(t)
	kinds, _ = e.groupKinds(t)
	_, still := kinds["/Photos/copy/IMG_2024_1.jpg"]
	assert.False(t, still, "the deleted copy is still grouped")
	assert.Equal(t, gallery.DuplicateSmaller, kinds["/Photos/small.jpg"])
}

// A copy that changed after the scan grouped it is no longer identical,
// so it is left alone.
func TestDeleteIdenticalRechecksBytes(t *testing.T) {
	e := newScanEnv(t)
	photo := photoJPEG(t, 3, 320, 240)
	e.upload(t, "/Photos/a.jpg", photo)
	e.upload(t, "/Photos/a_1.jpg", photo)
	e.scan(t)

	changed := append(bytes.Clone(photo[:len(photo)-2]), 0, 0xd9)
	changed[len(photo)/2] ^= 0xff
	e.upload(t, "/Photos/a_1.jpg", changed)
	require.NoError(t, e.g.StartDedupe(e.alice.Username))
	deadline := time.Now().Add(20 * time.Second)
	for {
		job, _ := e.g.DedupeStatus(e.alice.Username)
		if !job.Running() {
			require.Equal(t, 0, job.Done)
			require.Equal(t, 1, job.Failed)
			assert.Contains(t, job.Failures[0], "no longer identical")
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deletion still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Get(t, "/remote.php/dav/files/alice/Photos/a_1.jpg", e.alice.Auth).Status(200)
}
