package gallery_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

func TestGalleryPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/", alice.Auth).Status(200)
}

func TestGalleryTimelineWithMedia(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Get the gallery DB and insert media directly.
	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)

	// Run gallery migrations (schema.sql + migrations).
	// The gallery plugin's DB method does this, but PluginDB returns raw.
	// We need the schema first.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id INTEGER NOT NULL,
		file_path TEXT NOT NULL,
		file_etag TEXT NOT NULL,
		mime_type TEXT NOT NULL,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		orientation INTEGER NOT NULL DEFAULT 1,
		date_taken INTEGER,
		date_indexed INTEGER NOT NULL,
		latitude REAL, longitude REAL, altitude REAL,
		camera_make TEXT NOT NULL DEFAULT '',
		camera_model TEXT NOT NULL DEFAULT '',
		lens_model TEXT NOT NULL DEFAULT '',
		focal_length REAL, f_number REAL,
		exposure_time TEXT NOT NULL DEFAULT '',
		iso INTEGER, flash INTEGER, duration_ms INTEGER,
		video_codec TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '',
		place_name TEXT NOT NULL DEFAULT '',
		country TEXT NOT NULL DEFAULT '',
		region TEXT NOT NULL DEFAULT '',
		city TEXT NOT NULL DEFAULT '',
		recognition_state INTEGER NOT NULL DEFAULT 0,
		recognition_at INTEGER,
		recognition_error TEXT NOT NULL DEFAULT '',
		ocr_text TEXT NOT NULL DEFAULT '',
		fingerprint TEXT NOT NULL DEFAULT '',
		quality_score REAL NOT NULL DEFAULT -1
	)`)
	require.NoError(t, err)

	now := time.Now().Unix()
	m := &gallery.Media{
		FileID:      42,
		FilePath:    "/Photos/test.jpg",
		FileETag:    "abc123",
		MimeType:    "image/jpeg",
		Width:       1920,
		Height:      1080,
		DateIndexed: now,
	}
	id, err := gallery.InsertMedia(db, m)
	require.NoError(t, err)
	t.Logf("Inserted media id=%d", id)

	// Verify RecentMedia returns our item.
	media, err := gallery.RecentMedia(db, 100)
	require.NoError(t, err)
	require.Equal(t, 1, len(media))

	// Now hit the timeline page and verify it shows content, not "No photos yet".
	env.Get(t, "/apps/gallery/", alice.Auth).Status(200).Contains("/apps/gallery/photo/")
}

func TestGalleryPeopleAndTags(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)

	// Create schema.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS media (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id INTEGER NOT NULL, file_path TEXT NOT NULL, file_etag TEXT NOT NULL,
		mime_type TEXT NOT NULL, width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0, orientation INTEGER NOT NULL DEFAULT 1,
		date_taken INTEGER, date_indexed INTEGER NOT NULL,
		latitude REAL, longitude REAL, altitude REAL,
		camera_make TEXT NOT NULL DEFAULT '', camera_model TEXT NOT NULL DEFAULT '',
		lens_model TEXT NOT NULL DEFAULT '', focal_length REAL, f_number REAL,
		exposure_time TEXT NOT NULL DEFAULT '', iso INTEGER, flash INTEGER,
		duration_ms INTEGER, video_codec TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '', place_name TEXT NOT NULL DEFAULT '',
		country TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '',
		city TEXT NOT NULL DEFAULT '', recognition_state INTEGER NOT NULL DEFAULT 0,
		recognition_at INTEGER, recognition_error TEXT NOT NULL DEFAULT '',
		ocr_text TEXT NOT NULL DEFAULT '', fingerprint TEXT NOT NULL DEFAULT '', quality_score REAL NOT NULL DEFAULT -1
	);
	CREATE TABLE IF NOT EXISTS tags (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, tag_type INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS media_tags (media_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, confidence REAL NOT NULL DEFAULT 1.0, PRIMARY KEY (media_id, tag_id));
	CREATE TABLE IF NOT EXISTS people (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL DEFAULT '', representative_face_id INTEGER, visible INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL DEFAULT 0, contact_uid TEXT NOT NULL DEFAULT '');
	CREATE TABLE IF NOT EXISTS faces (id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, person_id INTEGER, x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL, embedding BLOB, confidence REAL NOT NULL DEFAULT 0.0, estimated_age REAL NOT NULL DEFAULT -1);
	CREATE TABLE IF NOT EXISTS gallery_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS rename_log (id INTEGER PRIMARY KEY AUTOINCREMENT, batch_id INTEGER NOT NULL, file_id INTEGER NOT NULL, old_path TEXT NOT NULL, new_path TEXT NOT NULL, renamed_at INTEGER NOT NULL DEFAULT 0)`)
	require.NoError(t, err)

	now := time.Now().Unix()

	// Insert two media items.
	for i := int64(1); i <= 2; i++ {
		_, err = db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_indexed)
			VALUES (?, ?, ?, 'image/jpeg', 1920, 1080, ?)`, i*10, fmt.Sprintf("/Photos/test%d.jpg", i), fmt.Sprintf("etag%d", i), now)
		require.NoError(t, err)
	}

	// Insert a tag and tag one media item.
	_, err = db.Exec(`INSERT INTO tags (name, tag_type) VALUES ('groom', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO media_tags (media_id, tag_id, confidence) VALUES (1, 1, 0.8)`)
	require.NoError(t, err)

	// Insert a person with faces.
	_, err = db.Exec(`INSERT INTO people (name, visible) VALUES ('', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence) VALUES (1, 1, 0.5, 0.5, 0.1, 0.1, 5.0)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence) VALUES (2, 1, 0.3, 0.3, 0.1, 0.1, 7.0)`)
	require.NoError(t, err)

	// Verify ListTags returns our tag.
	tags, err := gallery.ListTags(db)
	require.NoError(t, err)
	require.Equal(t, 1, len(tags))
	require.Equal(t, "groom", tags[0].Name)

	// Verify ListPeople returns our person.
	people, err := gallery.ListPeople(db)
	require.NoError(t, err)
	require.Equal(t, 1, len(people))
	require.Equal(t, 2, people[0].FaceCount)

	// Check pages render with content.
	env.Get(t, "/apps/gallery/tags", alice.Auth).Status(200).Contains("groom")
	env.Get(t, "/apps/gallery/people", alice.Auth).Status(200).Contains("2 photos")

	// Verify tag detail page shows linked media.
	env.Get(t, "/apps/gallery/tag/groom", alice.Auth).Status(200).Contains("/apps/gallery/photo/")

	// Verify person detail page shows linked media and face overlays.
	env.Get(t, "/apps/gallery/person/1", alice.Auth).Status(200).Contains("/apps/gallery/photo/").Contains("position:absolute")

	// Verify photo detail page shows faces sidebar.
	env.Get(t, "/apps/gallery/photo/1", alice.Auth).Status(200).Contains("Faces")
}

func TestGalleryAlbumsPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/albums", alice.Auth).Status(200)
}

func TestGallerySettingsPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/settings", alice.Auth).Status(200)
}

// TestSaveSystemSettingsSectionIndependence is tested via the internal
// package in clustering_test.go:TestSettingsSectionIndependence.

func TestGalleryPeoplePage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/people", alice.Auth).Status(200)
}

func TestGalleryPeopleUnnamedFilter(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)

	now := time.Now().Unix()
	_, err = db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_indexed)
		VALUES (1, '/Photos/a.jpg', 'etag', 'image/jpeg', 1920, 1080, ?)`, now)
	require.NoError(t, err)

	// A person only appears in the list once they have a face, so give each one.
	_, err = db.Exec(`INSERT INTO people (name, visible) VALUES ('Dieter', 1), ('', 1)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence)
		VALUES (1, 1, 0.1, 0.1, 0.2, 0.2, 5.0), (1, 2, 0.5, 0.5, 0.2, 0.2, 5.0)`)
	require.NoError(t, err)

	env.Get(t, "/apps/gallery/people", alice.Auth).Status(200).
		Contains("All (2)").Contains("Unnamed (1)").
		Contains("Dieter").Contains("Unknown")

	env.Get(t, "/apps/gallery/people?filter=unnamed", alice.Auth).Status(200).
		Contains("All (2)").Contains("Unnamed (1)").
		NotContains("Dieter").Contains("Unknown")
}

func TestGalleryTagsPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/tags", alice.Auth).Status(200)
}

func TestGalleryVideosPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/videos", alice.Auth).Status(200)
}

func TestGalleryStarredPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/starred", alice.Auth).Status(200)
}

func TestGalleryDuplicatesPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/duplicates", alice.Auth).Status(200)
}

func TestGalleryTimelineAPI(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	// TimelineAPI queries the media table directly, so we need the schema.
	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)
	_, err = db.Exec(fullSchema)
	require.NoError(t, err)

	// Empty timeline should return valid JSON.
	env.Get(t, "/apps/gallery/api/timeline", alice.Auth).Status(200)
}

func TestGallerySearchAPIs(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/api/tags/search?q=test", alice.Auth).Status(200)
	env.Get(t, "/apps/gallery/api/people/search?q=test", alice.Auth).Status(200)
}

// fullSchema creates all gallery tables needed for tests that write data.
const fullSchema = `CREATE TABLE IF NOT EXISTS media (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	file_id INTEGER NOT NULL, file_path TEXT NOT NULL, file_etag TEXT NOT NULL,
	mime_type TEXT NOT NULL, width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0, orientation INTEGER NOT NULL DEFAULT 1,
	date_taken INTEGER, date_indexed INTEGER NOT NULL,
	latitude REAL, longitude REAL, altitude REAL,
	camera_make TEXT NOT NULL DEFAULT '', camera_model TEXT NOT NULL DEFAULT '',
	lens_model TEXT NOT NULL DEFAULT '', focal_length REAL, f_number REAL,
	exposure_time TEXT NOT NULL DEFAULT '', iso INTEGER, flash INTEGER,
	duration_ms INTEGER, video_codec TEXT NOT NULL DEFAULT '',
	audio_codec TEXT NOT NULL DEFAULT '', place_name TEXT NOT NULL DEFAULT '',
	country TEXT NOT NULL DEFAULT '', region TEXT NOT NULL DEFAULT '',
	city TEXT NOT NULL DEFAULT '', recognition_state INTEGER NOT NULL DEFAULT 0,
	recognition_at INTEGER, recognition_error TEXT NOT NULL DEFAULT '',
	ocr_text TEXT NOT NULL DEFAULT '', fingerprint TEXT NOT NULL DEFAULT '', quality_score REAL NOT NULL DEFAULT -1
);
CREATE TABLE IF NOT EXISTS albums (
	id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
	album_type INTEGER NOT NULL DEFAULT 0, auto_key TEXT NOT NULL DEFAULT '',
	cover_media_id INTEGER, sort_order INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS album_media (
	album_id INTEGER NOT NULL, media_id INTEGER NOT NULL,
	sort_order INTEGER NOT NULL DEFAULT 0, added_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (album_id, media_id)
);
CREATE TABLE IF NOT EXISTS tags (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, tag_type INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS media_tags (media_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, confidence REAL NOT NULL DEFAULT 1.0, PRIMARY KEY (media_id, tag_id));
CREATE TABLE IF NOT EXISTS people (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL DEFAULT '', representative_face_id INTEGER, visible INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL DEFAULT 0, contact_uid TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS faces (id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, person_id INTEGER, x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL, embedding BLOB, confidence REAL NOT NULL DEFAULT 0.0, estimated_age REAL NOT NULL DEFAULT -1);
CREATE TABLE IF NOT EXISTS gallery_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS rename_log (id INTEGER PRIMARY KEY AUTOINCREMENT, batch_id INTEGER NOT NULL, file_id INTEGER NOT NULL, old_path TEXT NOT NULL, new_path TEXT NOT NULL, renamed_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS duplicate_groups (id INTEGER PRIMARY KEY AUTOINCREMENT, created_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS duplicate_members (group_id INTEGER NOT NULL, media_id INTEGER NOT NULL, is_keep INTEGER NOT NULL DEFAULT 0, distance REAL NOT NULL DEFAULT 0.0, PRIMARY KEY (group_id, media_id))`

// sessionAuth creates a session for the user and returns cogtest options
// for session cookie + CSRF header, needed for RegisterAction endpoints.
func sessionAuth(t testing.TB, env *cogtest.Env, u *cogtest.User) (cookie, csrf cogtest.HTTPOpt) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie = cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf = cogtest.Header("X-CSRF-Token", token)
	return
}

func TestGalleryAlbumsCRUD(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)
	_, err = db.Exec(fullSchema)
	require.NoError(t, err)

	cookie, csrf := sessionAuth(t, env, alice)

	// Create an album.
	env.PostForm(t, "/apps/gallery/albums", url.Values{
		"name":        {"Vacation"},
		"description": {"Summer trip"},
	}, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify album exists in the DB.
	albums, err := gallery.ListAlbums(db, -1)
	require.NoError(t, err)
	require.Equal(t, 1, len(albums))
	require.Equal(t, "Vacation", albums[0].Name)

	// Delete the album.
	env.Delete(t, fmt.Sprintf("/apps/gallery/albums/%d", albums[0].ID), cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify album is gone.
	albums, err = gallery.ListAlbums(db, -1)
	require.NoError(t, err)
	require.Equal(t, 0, len(albums))
}

func TestGalleryToggleStar(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)
	_, err = db.Exec(fullSchema)
	require.NoError(t, err)

	now := time.Now().Unix()
	id, err := gallery.InsertMedia(db, &gallery.Media{
		FileID: 42, FilePath: "/Photos/star.jpg", FileETag: "abc",
		MimeType: "image/jpeg", Width: 800, Height: 600, DateIndexed: now,
	})
	require.NoError(t, err)

	cookie, csrf := sessionAuth(t, env, alice)

	// Toggle star — the handler needs the files plugin, which isn't available
	// in this test (gallery is the only plugin). The handler returns 500 in
	// that case. Verify the endpoint is reachable (not 404).
	// Files plugin is nil so we get 500, but that proves the route + auth + CSRF all work.
	env.Post(t, fmt.Sprintf("/apps/gallery/media/%d/star", id), cookie, csrf).Status(500)
}

func TestGalleryTagMedia(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	db, err := env.Providers.Plugins.PluginDB(alice.Username, "gallery")
	require.NoError(t, err)
	_, err = db.Exec(fullSchema)
	require.NoError(t, err)

	now := time.Now().Unix()
	mediaID, err := gallery.InsertMedia(db, &gallery.Media{
		FileID: 50, FilePath: "/Photos/tagged.jpg", FileETag: "def",
		MimeType: "image/jpeg", Width: 800, Height: 600, DateIndexed: now,
	})
	require.NoError(t, err)

	cookie, csrf := sessionAuth(t, env, alice)

	// Add a tag to the media.
	env.PostForm(t, fmt.Sprintf("/apps/gallery/media/%d/tags", mediaID), url.Values{
		"tag_name": {"sunset"},
	}, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify tag was created.
	tags, err := gallery.ListTags(db)
	require.NoError(t, err)
	require.Equal(t, 1, len(tags))
	require.Equal(t, "sunset", tags[0].Name)

	// Remove the tag.
	env.PostForm(t, fmt.Sprintf("/apps/gallery/media/%d/remove-tag", mediaID), url.Values{
		"tag_id": {fmt.Sprintf("%d", tags[0].ID)},
	}, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify tag association is gone (tag still exists but not linked to media).
	mediaTags, err := gallery.TagsForMedia(db, mediaID)
	require.NoError(t, err)
	require.Equal(t, 0, len(mediaTags))
}
