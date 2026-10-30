package files_test

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

func TestBrowser(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "page", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "rename", "pass")
	env.CreateUser(t, "favorites", "pass")

	t.Run("PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Test Folder")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitVisible(t, "#files-table")
		b.WaitText(t, "Test Folder")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Doomed Folder")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitText(t, "Doomed Folder")
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `[hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("RenameFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "rename", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Old Name")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitText(t, "Old Name")
		// Override prompt to return the new name.
		b.Eval(t, `() => { window.prompt = () => "New Name" }`)
		b.Click(t, `[onclick*="renameFile"]`)
		b.WaitText(t, "New Name")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FavoritesPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "favorites", "pass")
		b.Navigate(t, "/apps/files/favorites")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// The subtests below look at the file browser with something in it: the
	// empty-state snapshots above say nothing about how rows, icons, long
	// names, previews or dialogs lay out.
	env.CreateUser(t, "full", "pass")
	seedFiles(t, fp, "full")

	t.Run("ListWithFiles", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeepFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files"+deepDir)
		b.WaitVisible(t, "#files-table")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("BulkSelection", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		b.Click(t, `.file-check[value="/notes.txt"]`)
		b.Click(t, `.file-check[value="/photo.png"]`)
		b.WaitVisible(t, "#bulk-delete-btn")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DropOverlay", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		// Dragging real files cannot be scripted; show the overlay the dragenter
		// handler would.
		b.Eval(t, `() => { document.getElementById("drop-overlay").style.display = "" }`)
		b.WaitVisible(t, "#drop-overlay")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ViewText", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/notes.txt")
		b.WaitVisible(t, "textarea")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ViewImage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/photo.png")
		b.WaitVisible(t, "main img")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ViewBinaryLongName", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files"+longFile)
		b.WaitText(t, "No preview available")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ShareDialog", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/notes.txt")
		// The share panel scopes its element IDs with Math.random; pin it so
		// the golden is stable.
		b.Eval(t, `() => { Math.random = () => 0.5 }`)
		b.Click(t, "cog-share-button button")
		b.WaitText(t, "Not shared with anyone")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ShareDialogExpanded", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/notes.txt")
		b.Eval(t, `() => { Math.random = () => 0.5 }`)
		b.Click(t, "cog-share-button button")
		b.WaitText(t, "Not shared with anyone")
		b.Eval(t, `() => { document.querySelectorAll(".modal-content details").forEach(d => d.open = true) }`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Archive", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/backup.zip")
		b.WaitText(t, "Extract All")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FavoritesWithItems", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/favorites")
		b.WaitText(t, "notes.txt")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Volumes", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/files/volumes")
		b.WaitText(t, "Volumes")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

const (
	deepDir  = "/Projects/2026/Client Work/A Rather Long Folder Name For Final Deliverables"
	longFile = "/Quarterly financial report for the board - final version (copy 2) - DO NOT EDIT.bin"
)

// seedFiles gives a user a root folder with one of each kind of file the
// listing has an icon for, a name long enough to wrap, a favourite, and a
// folder nested deep enough to stretch the breadcrumbs.
func seedFiles(t *testing.T, fp *files.Plugin, username string) {
	t.Helper()

	for _, dir := range []string{"/Documents", "/Photos", "/Projects", "/Projects/2026", "/Projects/2026/Client Work", deepDir} {
		_, err := fp.CreateDir(username, dir)
		require.NoError(t, err)
	}

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, name := range []string{"readme.txt", "src/main.go", "src/util.go", "assets/logo.png"} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(strings.Repeat("x", 100+len(name)*10)))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	// A slice, not a map: file IDs follow upload order and appear in the DOM.
	uploads := []struct {
		path string
		data []byte
	}{
		{"/notes.txt", []byte("Meeting notes\n\n- Ship the gallery\n- Review the file browser on mobile\n\tIndented with a tab\n")},
		{"/photo.png", testPNG(t, 320, 200, color.RGBA{R: 40, G: 120, B: 200, A: 255})},
		{"/song.mp3", bytes.Repeat([]byte{0xff}, 3*1024*1024)},
		{"/clip.mp4", bytes.Repeat([]byte{0x00}, 12*1024*1024)},
		{"/backup.zip", zipBuf.Bytes()},
		{longFile, bytes.Repeat([]byte{0x42}, 2048)},
		{deepDir + "/plan.txt", []byte("Deliverables plan\n")},
	}
	for _, u := range uploads {
		_, err := fp.Upload(username, u.path, bytes.NewReader(u.data))
		require.NoError(t, err)
	}

	require.NoError(t, fp.SetFavorite(username, "/notes.txt", true))
	require.NoError(t, fp.SetFavorite(username, "/Documents", true))
}

// testPNG draws a two-tone image so a preview visibly shows its content.
func testPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/2 {
				img.Set(x, y, c)
			} else {
				img.Set(x, y, color.RGBA{R: 255 - c.R, G: 255 - c.G, B: 255 - c.B, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
