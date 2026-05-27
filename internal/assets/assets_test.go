package assets

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"style.css":     {Data: []byte("body{color:red}")},
		"app.js":        {Data: []byte("console.log(1)")},
		"vendor/lib.js": {Data: []byte("var x=1")},
		"icon.svg":      {Data: []byte("<svg></svg>")},
	}
}

func newAssets(t *testing.T) *Assets {
	t.Helper()
	a, err := New(testFS(), "/static")
	require.NoError(t, err)
	return a
}

func TestURLShapeAndStability(t *testing.T) {
	a := newAssets(t)
	u1 := a.URL("style.css")
	u2 := a.URL("style.css")
	assert.Equal(t, u1, u2) // stable
	if !strings.HasPrefix(u1, "/static/style.css?v=") {
		t.Fatalf("URL = %q, want /static/style.css?v=...", u1)
	}
	v := u1[strings.Index(u1, "v=")+2:]
	if len(v) != hashLen {
		t.Errorf("version len = %d, want %d", len(v), hashLen)
	}
}

func TestURLUnknownAssetBarePath(t *testing.T) {
	a := newAssets(t)
	if got := a.URL("nope.css"); got != "/static/nope.css" {
		t.Errorf("unknown URL = %q, want bare /static/nope.css", got)
	}
}

func TestURLTrimsTrailingSlashPrefix(t *testing.T) {
	a, err := New(testFS(), "/apps/feed/static/")
	require.NoError(t, err)
	if got := a.URL("app.js"); !strings.HasPrefix(got, "/apps/feed/static/app.js?v=") {
		t.Errorf("URL = %q", got)
	}
}

// serve mounts the handler the way callers must (StripPrefix) and returns the
// recorded response for GET path (e.g. "/static/style.css?v=xxx").
func serve(t *testing.T, a *Assets, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h := http.StripPrefix("/static/", a)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServeImmutableWhenVersionMatches(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, a.URL("style.css"), nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "text/css; charset=utf-8", rec.Header().Get("Content-Type"))
	if rec.Header().Get("ETag") == "" {
		t.Error("missing ETag")
	}
}

func TestServeNoCacheWithoutVersion(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, "/static/style.css", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestServeNoCacheWithStaleVersion(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, "/static/style.css?v=deadbeefdead", nil)
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

func TestServeNotModifiedOnMatchingETag(t *testing.T) {
	a := newAssets(t)
	etag := strconv.Quote(a.hashes["style.css"][:hashLen])
	rec := serve(t, a, a.URL("style.css"), map[string]string{"If-None-Match": etag})
	assert.Equal(t, http.StatusNotModified, rec.Code)
}

func TestServeSubdirectoryAsset(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, a.URL("vendor/lib.js"), nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/javascript; charset=utf-8", rec.Header().Get("Content-Type"))
}

func TestServeUnknownAsset404(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, "/static/missing.js", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSVGContentType(t *testing.T) {
	a := newAssets(t)
	rec := serve(t, a, a.URL("icon.svg"), nil)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Errorf("Content-Type = %q, want svg", ct)
	}
}
