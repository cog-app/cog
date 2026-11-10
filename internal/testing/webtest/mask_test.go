package webtest

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maskPages serves each page at its path, for the masking tests.
func maskPages(t *testing.T, pages map[string]string) *Browser {
	t.Helper()
	mux := http.NewServeMux()
	for path, body := range pages {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		})
	}
	eng := New(mux)
	t.Cleanup(eng.Close)
	return eng.Start(t)
}

// golden is the masked golden of the page at path, with the page put back.
func golden(t *testing.T, b *Browser, path string) string {
	t.Helper()
	b.Navigate(t, path)
	dom, unmask, err := b.maskedDOM()
	if err != nil {
		t.Fatal(err)
	}
	if err := unmask(); err != nil {
		t.Fatal(err)
	}
	return dom
}

// Per-run values stay out of the golden: without this every snapshot fails on
// every run for reasons that have nothing to do with the page.
func TestVolatileValuesAreMasked(t *testing.T) {
	page := func(token, avatar, id string) string {
		return `<html><body hx-headers='{"X-CSRF-Token":"` + token + `"}'>` +
			`<img src="/avatar/` + avatar + `"><input name="subject_id" value="` + id + `"></body></html>`
	}
	b := maskPages(t, map[string]string{
		"/1": page("2384570768059786728779548a95b3378fc1e82349589e917ee3ad1932b83b4b", "69474ee7baf0372a133b61095fe862d4", "0a5b6c7d-1e2f-3a4b-5c6d-7e8f9a0b1c2d"),
		"/2": page("3055b79337abad7b51c0c872848025e3044278b38cb1fb36f92f266e8afb9b11", "e0ecfd10232941bc982c313b01fd929e", "ffffffff-1111-2222-3333-444444444444"),
	})
	first, second := golden(t, b, "/1"), golden(t, b, "/2")
	if first != second {
		t.Fatalf("volatile values reached the golden:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(first, "2384570768") {
		t.Error("csrf token was not masked")
	}
}

// The test's temp directory, which has a random suffix, and the test server's
// random port stay out of pages that show a path or an absolute URL.
func TestTempDirAndPortAreMasked(t *testing.T) {
	page := func(dir, port string) string {
		return `<html><body><td>file://` + filepath.Join(os.TempDir(), dir, "001") + `</td>` +
			`<a href="http://127.0.0.1:` + port + `/invite/x">http://127.0.0.1:` + port + `/invite/x</a></body></html>`
	}
	b := maskPages(t, map[string]string{
		"/1": page("TestBrowserAdminCreateVolume2382680420", "40211"),
		"/2": page("TestBrowserAdminCreateVolume117", "3311"),
	})
	first, second := golden(t, b, "/1"), golden(t, b, "/2")
	if first != second {
		t.Fatalf("temp directory or port reached the golden:\n%s\n---\n%s", first, second)
	}
	for _, want := range []string{"file://«tmp»/001", `href="http://127.0.0.1:«port»/invite/x"`, ">\n      http://127.0.0.1:«port»/invite/x"} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q in:\n%s", want, first)
		}
	}
}

// The masking is not too greedy: the asset cache-busting hash is twelve
// characters, and masking it would hide a CSS edit from the golden.
func TestShortHashesSurvive(t *testing.T) {
	b := maskPages(t, map[string]string{
		"/": `<html><head><link rel="stylesheet" href="/static/style.css?v=014e31a1dbda"></head><body></body></html>`,
	})
	if out := golden(t, b, "/"); !strings.Contains(out, "014e31a1dbda") {
		t.Fatalf("the asset hash was masked away:\n%s", out)
	}
}

// The image is taken of the masked page, and the page is put back after: what
// the test does next reads and submits the real values, a field it typed into
// included, and scripts are never touched.
func TestMaskingIsUndone(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "TestSomething123", "001")
	b := maskPages(t, map[string]string{
		"/": `<html><body><p id="dir">Data in ` + dir + `</p>` +
			`<input id="typed"><input id="f" value="` + dir + `/x">` +
			`<script>var keep = "127.0.0.1:1";</script></body></html>`,
	})
	b.Navigate(t, "/")
	b.Type(t, "#typed", "http://127.0.0.1:5555/")

	_, unmask, err := b.maskedDOM()
	if err != nil {
		t.Fatal(err)
	}
	for sel, want := range map[string]string{"#typed": "http://127.0.0.1:«port»/", "#f": "«tmp»/001/x"} {
		if got := b.Value(t, sel); got != want {
			t.Errorf("masked %s = %q, want %q", sel, got, want)
		}
	}
	if got := b.Text(t, "#dir"); got != "Data in «tmp»/001" {
		t.Errorf("masked text = %q", got)
	}
	if got := b.Eval(t, "keep"); got != "127.0.0.1:1" {
		t.Errorf("script changed: %v", got)
	}

	if err := unmask(); err != nil {
		t.Fatal(err)
	}
	for sel, want := range map[string]string{"#typed": "http://127.0.0.1:5555/", "#f": dir + "/x"} {
		if got := b.Value(t, sel); got != want {
			t.Errorf("unmasked %s = %q, want %q", sel, got, want)
		}
	}
	if got := b.Text(t, "#dir"); got != "Data in "+dir {
		t.Errorf("unmasked text = %q", got)
	}
}
