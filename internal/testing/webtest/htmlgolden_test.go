package webtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func normalize(t *testing.T, raw string) string {
	t.Helper()
	out, err := normalizeDOM(raw)
	if err != nil {
		t.Fatalf("normalizeDOM: %v", err)
	}

	return out
}

// TestStylesheetHashReachesTheGolden is what makes the screenshot honest about
// CSS. A stylesheet edit changes no markup, so without the content hash in the
// asset URL the golden would match and the committed image would keep showing
// the old styling.
func TestStylesheetHashReachesTheGolden(t *testing.T) {
	page := func(hash string) string {
		return `<html><head><link rel="stylesheet" href="/static/style.css?v=` + hash + `"></head><body><p>hi</p></body></html>`
	}

	before := normalize(t, page("014e31a1dbda"))
	after := normalize(t, page("99887766aabb"))

	if before == after {
		t.Fatal("a changed stylesheet hash left the golden identical; a CSS edit would not be noticed")
	}
}

// TestVolatileValuesAreMasked keeps per-run values out of the golden. Without
// this every snapshot fails on every run for reasons that have nothing to do
// with the page.
func TestVolatileValuesAreMasked(t *testing.T) {
	first := normalize(t, `<html><body hx-headers='{"X-CSRF-Token":"2384570768059786728779548a95b3378fc1e82349589e917ee3ad1932b83b4b"}'>`+
		`<img src="/avatar/69474ee7baf0372a133b61095fe862d4">`+
		`<input name="subject_id" value="0a5b6c7d-1e2f-3a4b-5c6d-7e8f9a0b1c2d">`+
		`</body></html>`)
	second := normalize(t, `<html><body hx-headers='{"X-CSRF-Token":"3055b79337abad7b51c0c872848025e3044278b38cb1fb36f92f266e8afb9b11"}'>`+
		`<img src="/avatar/e0ecfd10232941bc982c313b01fd929e">`+
		`<input name="subject_id" value="ffffffff-1111-2222-3333-444444444444">`+
		`</body></html>`)

	if first != second {
		t.Fatalf("volatile values reached the golden:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(first, "2384570768") {
		t.Error("csrf token was not masked")
	}
}

// TestTempDirsAreMasked keeps the test's temp directory, which has a random
// suffix, out of pages that show a path.
func TestTempDirsAreMasked(t *testing.T) {
	page := func(dir string) string {
		return normalize(t, `<html><body><td>file://`+filepath.Join(os.TempDir(), dir, "001")+`</td></body></html>`)
	}
	first, second := page("TestBrowserAdminCreateVolume2382680420"), page("TestBrowserAdminCreateVolume117")
	if first != second {
		t.Fatalf("temp directory reached the golden:\n%s\n---\n%s", first, second)
	}
	if !strings.Contains(first, "file://«tmp»/001") {
		t.Fatalf("the path under the temp directory was not kept:\n%s", first)
	}
}

// TestShortHashesSurvive guards the masking against being too greedy: the
// asset cache-busting hash is twelve characters, and masking it would undo
// TestStylesheetHashReachesTheGolden.
func TestShortHashesSurvive(t *testing.T) {
	out := normalize(t, `<html><head><link rel="stylesheet" href="/static/style.css?v=014e31a1dbda"></head><body></body></html>`)

	if !strings.Contains(out, "014e31a1dbda") {
		t.Fatalf("the asset hash was masked away:\n%s", out)
	}
}

// TestAttributeOrderIsStable stops a reordering in the serialiser from reading
// as a change to the page.
func TestAttributeOrderIsStable(t *testing.T) {
	a := normalize(t, `<html><body><div class="x" id="y" data-z="1"></div></body></html>`)
	b := normalize(t, `<html><body><div data-z="1" id="y" class="x"></div></body></html>`)

	if a != b {
		t.Fatalf("attribute order changed the golden:\n%s\n---\n%s", a, b)
	}
}

// TestGoldenIsLineOriented keeps the format reviewable: a diff should point at
// the element that changed rather than at one enormous line.
func TestGoldenIsLineOriented(t *testing.T) {
	out := normalize(t, `<html><body><div><p>one</p><p>two</p></div></body></html>`)

	if lines := strings.Count(out, "\n"); lines < 6 {
		t.Fatalf("expected one node per line, got %d lines:\n%s", lines, out)
	}
	// Text sits one level deeper than the element containing it.
	var paraIndent, textIndent int
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch trimmed {
		case "<p>":
			if paraIndent == 0 {
				paraIndent = len(line) - len(trimmed)
			}
		case "one":
			textIndent = len(line) - len(trimmed)
		}
	}
	if textIndent <= paraIndent {
		t.Fatalf("expected content indented inside its element (<p> at %d, text at %d):\n%s", paraIndent, textIndent, out)
	}
}

// TestScriptsAreDropped keeps library upgrades out of the goldens. The DOM is
// captured after the page settles, so what a script did is already in the tree
// around it — the element itself only carries an asset hash that would change
// every golden when nothing visible changed.
func TestScriptsAreDropped(t *testing.T) {
	out := normalize(t, `<html><head><script src="/static/htmx.min.js?v=e209dda5c823"></script>`+
		`<script>window.x = 1</script></head><body><p>hi</p></body></html>`)

	if strings.Contains(out, "script") || strings.Contains(out, "htmx.min.js") {
		t.Fatalf("script survived normalisation:\n%s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("dropping scripts took the page with it:\n%s", out)
	}
}

// TestStylesSurvive guards the other half: stylesheets are the visual
// mechanism, and their content hash is what makes a CSS edit reach the golden.
func TestStylesSurvive(t *testing.T) {
	out := normalize(t, `<html><head><link rel="stylesheet" href="/static/style.css?v=014e31a1dbda">`+
		`<style>.a{color:red}</style></head><body></body></html>`)

	if !strings.Contains(out, "014e31a1dbda") {
		t.Errorf("stylesheet link was dropped:\n%s", out)
	}
	if !strings.Contains(out, "color:red") {
		t.Errorf("inline style was dropped:\n%s", out)
	}
}

func TestElementsAreClosed(t *testing.T) {
	out := normalize(t, `<html><body><div class="a"><p>text</p></div></body></html>`)

	for _, want := range []string{"<div class=\"a\">", "</div>", "<p>", "</p>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestChildlessAndVoidElementsSelfClose(t *testing.T) {
	out := normalize(t, `<html><body><div class="empty"></div><br><img src="/a.png"></body></html>`)

	for _, want := range []string{`<div class="empty" />`, "<br />", `<img src="/a.png" />`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "</br>") || strings.Contains(out, "</img>") {
		t.Errorf("void element was given a closing tag:\n%s", out)
	}
}
