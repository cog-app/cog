package ui

import (
	"io/fs"

	"git.duckfam.us/jj-software/cog/internal/assets"
)

// Static serves the embedded core assets (CSS, JS, vendor libs) with
// content-hash cache busting. Use Static.URL(name) in templates instead of
// hardcoding "/static/..." paths, and mount Static at GET /static/.
var Static = func() *assets.Assets {
	sub, err := fs.Sub(StaticFS, "static")
	if err != nil {
		panic("ui: sub static FS: " + err.Error())
	}
	a, err := assets.New(sub, "/static")
	if err != nil {
		panic("ui: build static assets: " + err.Error())
	}
	return a
}()
