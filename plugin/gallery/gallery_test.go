package gallery_test

import (
	"fmt"
	"testing"
	"time"

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
		ocr_text TEXT NOT NULL DEFAULT ''
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
		ocr_text TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS tags (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, tag_type INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS media_tags (media_id INTEGER NOT NULL, tag_id INTEGER NOT NULL, confidence REAL NOT NULL DEFAULT 1.0, PRIMARY KEY (media_id, tag_id));
	CREATE TABLE IF NOT EXISTS people (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL DEFAULT '', representative_face_id INTEGER, visible INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL DEFAULT 0);
	CREATE TABLE IF NOT EXISTS faces (id INTEGER PRIMARY KEY AUTOINCREMENT, media_id INTEGER NOT NULL, person_id INTEGER, x REAL NOT NULL, y REAL NOT NULL, w REAL NOT NULL, h REAL NOT NULL, embedding BLOB, confidence REAL NOT NULL DEFAULT 0.0);
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

	// Verify person detail page shows linked media.
	env.Get(t, "/apps/gallery/person/1", alice.Auth).Status(200).Contains("/apps/gallery/photo/")
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
