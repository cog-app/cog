package gallery_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/input"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "page", "pass")
	env.CreateUser(t, "settings", "pass")

	t.Run("PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.WaitText(t, "Gallery Settings")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("AlbumsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/albums")
		b.WaitText(t, "Albums")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("StarredPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/starred")
		b.WaitText(t, "Starred")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("VideosPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/videos")
		b.WaitText(t, "Videos")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("PeoplePage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/people")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("PeoplePageWithPeople", func(t *testing.T) {
		user := env.CreateUser(t, "faces", "pass")
		seedPeople(t, env, user.Username, 6)

		b := eng.Start(t)
		b.Login(t, "faces", "pass")
		b.Navigate(t, "/apps/gallery/people")
		b.WaitText(t, "Unnamed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("TagsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/tags")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DuplicatesPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/duplicates")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// seedPeople gives a user n people sharing one photo, half of them named. The
// face thumbnails 404 — the media row points at no real file — which is what we
// want for a layout snapshot: the tiles size from CSS, not from image data.
func seedPeople(t *testing.T, env *cogtest.Env, username string, n int) {
	t.Helper()
	db, err := env.Providers.Plugins.PluginDB(username, "gallery")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_indexed)
		VALUES (1, '/Photos/group.jpg', 'etag', 'image/jpeg', 1920, 1080, 1700000000)`)
	require.NoError(t, err)

	names := []string{"Dieter", "Ada", "Grace"}
	for i := range n {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		res, err := db.Exec(`INSERT INTO people (name, visible) VALUES (?, 1)`, name)
		require.NoError(t, err)
		personID, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence)
			VALUES (1, ?, 0.1, 0.1, 0.2, 0.2, 5.0)`, personID)
		require.NoError(t, err)
	}
}

// TestBrowserWithPhotos looks at the gallery with something in it. The files
// plugin is registered so thumbnails are real images rather than 404s; the
// gallery rows are inserted directly because the scan worker is a subprocess
// the harness does not start.
func TestBrowserWithPhotos(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp, gallery.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	user := env.CreateUser(t, "photos", "pass")
	s := seedPhotos(t, env, fp, user.Username)

	pages := []struct {
		name, path, wait string
	}{
		{"Timeline", "/apps/gallery/", "Timeline"},
		{"Starred", "/apps/gallery/starred", "Starred"},
		{"Videos", "/apps/gallery/videos", "Videos"},
		{"Albums", "/apps/gallery/albums", "Summer"},
		{"AlbumView", fmt.Sprintf("/apps/gallery/album/%d", s.album), "Summer"},
		{"AlbumEmpty", fmt.Sprintf("/apps/gallery/album/%d", s.emptyAlbum), "This album is empty"},
		{"Tags", "/apps/gallery/tags", "beach"},
		{"TagView", "/apps/gallery/tag/beach", "beach"},
		{"People", "/apps/gallery/people", "3 photos"},
		{"PersonView", fmt.Sprintf("/apps/gallery/person/%d", s.person), "Hide"},
		{"Duplicates", "/apps/gallery/duplicates", "similar photos"},
		{"DuplicateGroup", fmt.Sprintf("/apps/gallery/duplicates/%d", s.dupGroup), "Duplicate group"},
		{"PhotoView", fmt.Sprintf("/apps/gallery/photo/%d", s.detailed), "Details"},
		{"PhotoViewMinimal", fmt.Sprintf("/apps/gallery/photo/%d", s.plain), "Details"},
		{"VideoView", fmt.Sprintf("/apps/gallery/photo/%d", s.video), "Details"},
		{"Settings", "/apps/gallery/settings", "Gallery Settings"},
	}
	for _, pg := range pages {
		t.Run(pg.name, func(t *testing.T) {
			b := eng.Start(t)
			b.Login(t, "photos", "pass")
			b.Navigate(t, pg.path)
			b.WaitText(t, pg.wait)
			b.Snapshot(t)
			b.Snapshot(t, webtest.Mobile)
		})
	}

	// The photo itself opens the lightbox: the face boxes no longer cover it
	// until "Show faces" turns them on.
	t.Run("Lightbox", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/photo/%d", s.detailed))
		b.Click(t, "[data-lightbox-open]")
		b.WaitVisible(t, ".gallery-lightbox img")
		waitImageLoaded(t, b, ".gallery-lightbox img")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// A name with "&" and "#" in it used to cut the download URL short, so
	// the lightbox stayed black.
	t.Run("LightboxSpecialChars", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/photo/%d", s.special))
		b.Click(t, "[data-lightbox-open]")
		b.WaitVisible(t, ".gallery-lightbox img")
		if w := waitImageLoaded(t, b, ".gallery-lightbox img"); w != 600 {
			t.Fatalf("full-size image is %dpx wide, want the 600px original", w)
		}
		b.Snapshot(t)
	})

	t.Run("ShowFaces", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/photo/%d", s.detailed))
		b.Click(t, "[data-toggle-faces]")
		b.WaitVisible(t, ".show-faces")
		b.Snapshot(t)
	})

	t.Run("RenamePreview", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.Click(t, `[data-testid="rename-preview"]`)
		b.WaitText(t, "will be renamed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FolderPreview", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.Click(t, `[hx-get$="scan-folders-preview"]`)
		b.WaitText(t, "matching files")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("NewAlbumDialog", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, "/apps/gallery/albums")
		b.Click(t, `[data-modal-open="gallery-new-album"]`)
		b.WaitVisible(t, "#new-album-name")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("AddPhotosDialog", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/album/%d", s.emptyAlbum))
		b.Click(t, `[data-modal-open="gallery-add-photos"]`)
		b.WaitVisible(t, "#gallery-add-photos .gallery-pick")
		b.Eval(t, `() => document.querySelector("#gallery-add-photos .gallery-pick input").click()`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("NamePersonDialog", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/person/%d", s.person))
		b.Click(t, `[data-modal-open="gallery-name-person"]`)
		b.WaitVisible(t, "#gallery-person-name")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ConfirmDialog", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/duplicates/%d", s.dupGroup))
		b.Click(t, `form[action$="/delete"] button[type="submit"]`)
		b.WaitVisible(t, "#gallery-confirm")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// Creating a second manual album used to fail on a unique index that
	// treated every manual album as the same one.
	t.Run("CreateAlbum", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, "/apps/gallery/albums")
		b.Click(t, `[data-modal-open="gallery-new-album"]`)
		b.Type(t, "#new-album-name", "Birthdays")
		b.Click(t, `#gallery-new-album button[type="submit"]`)
		b.WaitText(t, "Created “Birthdays”")
		b.WaitText(t, "This album is empty")
	})

	// A photo's corner menu on the album page. IMG_0004 is in the first
	// column on a phone, where the menu has to open rightwards to stay on
	// screen.
	t.Run("AlbumTileMenu", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/album/%d", s.album))
		openTileMenu(t, b, "IMG_0004.png")
		b.WaitVisible(t, ".gallery-tile-menu.open [data-set-cover]")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// The cover mark moves to the chosen photo, and the flash says so.
	t.Run("SetAlbumCover", func(t *testing.T) {
		db, err := env.Providers.Plugins.PluginDB(user.Username, "gallery")
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, gallery.SetAlbumCover(db, s.album, s.detailed))
		})
		b := eng.Start(t)
		b.Login(t, "photos", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/album/%d", s.album))
		openTileMenu(t, b, "IMG_0004.png")
		b.Click(t, ".gallery-tile-menu.open [data-set-cover]")
		b.WaitText(t, "“IMG_0004.png” is now the cover of “Summer 2025”.")
		if got := b.Attribute(t, ".gallery-cover-badge", "class"); got != "gallery-cover-badge" {
			t.Fatalf("no cover mark: %q", got)
		}
		if n := b.Count(t, `.gallery-tile:has([title="IMG_0004.png"]) .gallery-cover-badge`); n != 1 {
			t.Fatalf("IMG_0004.png has %d cover marks, want 1", n)
		}
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

type seededPhotos struct {
	album, emptyAlbum, person, dupGroup, detailed, plain, video, special int64
}

// seedPhotos uploads a handful of small, distinctly coloured PNGs of different
// shapes into /Photos and indexes them, with tags, people, albums, a duplicate
// group, a starred photo and a video row. Every timestamp is fixed so the
// goldens do not move.
func seedPhotos(t *testing.T, env *cogtest.Env, fp *files.Plugin, username string) seededPhotos {
	t.Helper()
	db, err := env.Providers.Plugins.PluginDB(username, "gallery")
	require.NoError(t, err)
	_, err = fp.CreateDir(username, "/Photos")
	require.NoError(t, err)

	type photo struct {
		name string
		w, h int
		c    color.RGBA
	}
	photos := []photo{
		{"IMG_0001.png", 400, 300, color.RGBA{220, 80, 60, 255}},
		{"IMG_0002.png", 300, 400, color.RGBA{60, 160, 90, 255}},
		{"IMG_0003.png", 400, 400, color.RGBA{50, 90, 200, 255}},
		{"IMG_0004.png", 480, 270, color.RGBA{230, 180, 40, 255}},
		{"IMG_0005.png", 270, 480, color.RGBA{140, 70, 180, 255}},
		{"Beach day & sunset #2 - the long panoramic one.png", 600, 200, color.RGBA{250, 130, 80, 255}},
		{"IMG_0007.png", 400, 300, color.RGBA{40, 170, 170, 255}},
		{"IMG_0007 (copy).png", 400, 300, color.RGBA{40, 170, 170, 255}},
	}
	taken := time.Date(2025, time.July, 4, 15, 30, 0, 0, time.UTC).Unix()
	ids := make([]int64, len(photos))
	for i, p := range photos {
		meta, err := fp.Upload(username, "/Photos/"+p.name, bytes.NewReader(testPNG(t, p.w, p.h, p.c)))
		require.NoError(t, err)
		when := taken - int64(i)*86400
		m := &gallery.Media{
			FileID: meta.ID, FilePath: meta.Path, FileETag: meta.ETag,
			MimeType: "image/png", Width: p.w, Height: p.h,
			DateTaken: &when, DateIndexed: taken,
		}
		if i == 0 {
			f, iso, focal := 1.8, 100, 26.0
			m.CameraMake, m.CameraModel = "Apple", "iPhone 15 Pro"
			m.LensModel = "iPhone 15 Pro back triple camera 6.765mm f/1.78"
			m.FNumber, m.ISO, m.FocalLength = &f, &iso, &focal
			m.ExposureTime = "1/120"
			m.PlaceName = "Santa Cruz, California, United States"
			m.Fingerprint = "a1b2c3d4e5f60718"
			m.OCRText = "SANTA CRUZ\nBEACH BOARDWALK"
		}
		ids[i], err = gallery.InsertMedia(db, m)
		require.NoError(t, err)
	}

	dur := int64(83_000)
	_, err = fp.Upload(username, "/Photos/VID_0001.mp4", bytes.NewReader(bytes.Repeat([]byte{0}, 4096)))
	require.NoError(t, err)
	vmeta, err := fp.GetByPath(username, "/Photos/VID_0001.mp4")
	require.NoError(t, err)
	videoID, err := gallery.InsertMedia(db, &gallery.Media{
		FileID: vmeta.ID, FilePath: vmeta.Path, FileETag: vmeta.ETag,
		MimeType: "video/mp4", Width: 1920, Height: 1080,
		DateTaken: &taken, DateIndexed: taken, DurationMs: &dur, VideoCodec: "h264",
	})
	require.NoError(t, err)

	for _, tag := range []struct {
		name  string
		media []int64
	}{
		{"beach", []int64{ids[0], ids[5], ids[3]}},
		{"sunset", []int64{ids[5]}},
		{"family", []int64{ids[0], ids[1]}},
		{"golden retriever", []int64{ids[2]}},
		{"architecture-and-buildings", []int64{ids[4]}},
	} {
		tagID, err := gallery.UpsertTag(db, tag.name, 1)
		require.NoError(t, err)
		for _, m := range tag.media {
			require.NoError(t, gallery.AddMediaTag(db, m, tagID, 0.87))
		}
	}

	// Two named people and one unnamed face on the detailed photo.
	var person int64
	for i, name := range []string{"Ada", "Grace Hopper-Lovelace", ""} {
		res, err := db.Exec(`INSERT INTO people (name, visible) VALUES (?, 1)`, name)
		require.NoError(t, err)
		pid, err := res.LastInsertId()
		require.NoError(t, err)
		if i == 0 {
			person = pid
		}
		x := 0.1 + float64(i)*0.3
		for _, m := range []int64{ids[0], ids[1], ids[2]}[:3-i] {
			_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence, estimated_age)
				VALUES (?, ?, ?, 0.2, 0.2, 0.3, 0.93, 34)`, m, pid, x)
			require.NoError(t, err)
		}
	}
	_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence) VALUES (?, NULL, 0.7, 0.5, 0.15, 0.2, 0.71)`, ids[0])
	require.NoError(t, err)

	album, err := gallery.CreateAlbumRow(db, "Summer 2025", "Road trip down the coast")
	require.NoError(t, err)
	for _, m := range ids[:6] {
		require.NoError(t, gallery.AddMediaToAlbum(db, album, m))
	}
	require.NoError(t, gallery.SetAlbumCover(db, album, ids[0]))
	emptyAlbum, err := gallery.CreateAlbumRow(db, "A weekend in the mountains with the whole extended family", "")
	require.NoError(t, err)

	group, err := gallery.InsertDuplicateGroup(db)
	require.NoError(t, err)
	require.NoError(t, gallery.AddDuplicateMember(db, group, ids[6], true, 0))
	require.NoError(t, gallery.AddDuplicateMember(db, group, ids[7], false, 2))

	require.NoError(t, fp.SetFavorite(username, "/Photos/IMG_0001.png", true))
	require.NoError(t, fp.SetFavorite(username, "/Photos/IMG_0003.png", true))

	return seededPhotos{
		album: album, emptyAlbum: emptyAlbum, person: person, dupGroup: group,
		detailed: ids[0], plain: ids[2], video: videoID, special: ids[5],
	}
}

// waitImageLoaded waits for the image to finish loading and returns its
// natural width, 0 if it failed. Eval does not await a promise, so it polls.
func waitImageLoaded(t *testing.T, b *webtest.Browser, sel string) int {
	t.Helper()
	js := `() => { const i = document.querySelector(` + "`" + sel + "`" + `); return i && i.complete ? i.naturalWidth : -1 }`
	deadline := time.Now().Add(20 * time.Second)
	for {
		var w int
		switch v := b.Eval(t, js).(type) {
		case float64:
			w = int(v)
		case int:
			w = v
		default:
			t.Fatalf("image width: got %T %v", v, v)
		}
		if w >= 0 {
			return w
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not finish loading", sel)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// testPNG draws a two-tone image so a thumbnail visibly shows its crop.
func testPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	inv := color.RGBA{255 - c.R, 255 - c.G, 255 - c.B, 255}
	for y := range h {
		for x := range w {
			if x < w/2 {
				img.Set(x, y, c)
			} else {
				img.Set(x, y, inv)
			}
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// openTileMenu opens a photo's corner menu on the album page. The button
// shows on hover, and rod's click waits on it to become interactable while
// it fades in; a script click is what a pointer's click does.
func openTileMenu(t *testing.T, b *webtest.Browser, name string) {
	t.Helper()
	b.WaitReady(t, `[aria-label="Actions for `+name+`"]`)
	b.Eval(t, `() => document.querySelector('[aria-label="Actions for `+name+`"]').click()`)
}

// TestBrowserNameSuggestions opens the name dialog of an unnamed person whose
// face is close to two named people's, and names them with one click.
func TestBrowserNameSuggestions(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	user := env.CreateUser(t, "suggest", "pass")
	db, err := env.Providers.Plugins.PluginDB(user.Username, "gallery")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_indexed)
		VALUES (1, '/Photos/group.jpg', 'etag', 'image/jpeg', 1920, 1080, 1700000000)`)
	require.NoError(t, err)

	// Unit embeddings at a given cosine distance from axis 0.
	embedding := func(axis int, dist float64) []byte {
		v := make([]float32, 512)
		v[0] = float32(1 - dist)
		v[axis] = float32(math.Sqrt(1 - (1-dist)*(1-dist)))
		b := make([]byte, 4*len(v))
		for i, f := range v {
			binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
		}
		return b
	}
	person := func(name string, emb []byte) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO people (name, visible) VALUES (?, 1)`, name)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence, embedding)
			VALUES (1, ?, 0.1, 0.1, 0.2, 0.2, 0.9, ?)`, id, emb)
		require.NoError(t, err)
		return id
	}
	ada := person("Ada", embedding(1, 0.1))
	person("Grace", embedding(2, 0.3))
	person("Linus", embedding(3, 0.9))

	open := func(t *testing.T, id int64) *webtest.Browser {
		t.Helper()
		b := eng.Start(t)
		b.Login(t, "suggest", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/gallery/person/%d", id))
		b.Click(t, `[data-modal-open="gallery-name-person"]`)
		b.WaitVisible(t, ".gallery-suggestion")
		return b
	}

	t.Run("Merge", func(t *testing.T) {
		unnamed := person("", embedding(4, 0))
		b := open(t, unnamed)
		if n := b.Count(t, ".gallery-suggestion"); n != 2 {
			t.Fatalf("%d suggestions, want Ada and Grace", n)
		}
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)

		b.Click(t, ".gallery-suggestion")
		b.WaitText(t, "Hide")
		if got, want := b.URL(t), fmt.Sprintf("/apps/gallery/person/%d", ada); !strings.HasSuffix(got, want) {
			t.Fatalf("landed on %s, want %s", got, want)
		}
		var left int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM people WHERE id = ?`, unnamed).Scan(&left))
		if left != 0 {
			t.Fatal("unnamed person was not merged into Ada")
		}
	})

	// Enter in the name field saves the typed name. The suggestions are
	// submit buttons ahead of Save; if they were the name form's, Enter
	// would merge into the first of them instead.
	t.Run("EnterSavesTypedName", func(t *testing.T) {
		unnamed := person("", embedding(4, 0))
		b := open(t, unnamed)
		b.Type(t, "#gallery-person-name", "Margaret")
		b.Press(t, "#gallery-person-name", input.Enter)
		b.WaitText(t, "Rename")
		var name string
		require.NoError(t, db.QueryRow(`SELECT name FROM people WHERE id = ?`, unnamed).Scan(&name))
		if name != "Margaret" {
			t.Fatalf("person is named %q, want Margaret", name)
		}
	})
}
