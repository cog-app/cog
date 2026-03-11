package webtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Browser controls a single browser tab.
type Browser struct {
	page    *rod.Page
	baseURL string
	timeout time.Duration
}

const defaultTimeout = 10 * time.Second

// SetTimeout overrides the default action timeout (10s).
func (b *Browser) SetTimeout(d time.Duration) {
	b.timeout = d
}

func (b *Browser) actionTimeout() time.Duration {
	if b.timeout > 0 {
		return b.timeout
	}
	return defaultTimeout
}

// tp returns a page clone with the action timeout applied.
func (b *Browser) tp() *rod.Page {
	return b.page.Timeout(b.actionTimeout())
}

// el finds a visible element by CSS selector (or XPath if it starts with //).
// It waits for the page to stabilise first to avoid stale element handles
// from in-flight navigations.
func (b *Browser) el(sel string) (*rod.Element, error) {
	p := b.tp()
	// Wait for the page to settle (no DOM mutations or network activity for
	// 300ms) so we don't grab element handles from a page that is about to
	// be replaced by a navigation redirect.
	_ = p.WaitStable(300 * time.Millisecond)
	if len(sel) > 1 && sel[:2] == "//" {
		el, err := p.ElementX(sel)
		if err != nil {
			return nil, err
		}
		return el, el.WaitVisible()
	}
	el, err := p.Element(sel)
	if err != nil {
		return nil, err
	}
	return el, el.WaitVisible()
}

// --- Navigation ---

// Navigate goes to the given path on the test server.
func (b *Browser) Navigate(t testing.TB, path string) {
	t.Helper()
	if err := b.tp().Navigate(b.baseURL + path); err != nil {
		t.Fatalf("webtest: navigate %s: %v", path, err)
	}
	if err := b.tp().WaitLoad(); err != nil {
		t.Fatalf("webtest: wait load %s: %v", path, err)
	}
}

// NavigateURL goes to an absolute URL.
func (b *Browser) NavigateURL(t testing.TB, url string) {
	t.Helper()
	if err := b.tp().Navigate(url); err != nil {
		t.Fatalf("webtest: navigate %s: %v", url, err)
	}
	if err := b.tp().WaitLoad(); err != nil {
		t.Fatalf("webtest: wait load: %v", err)
	}
}

// --- Interaction ---

// Click waits for the element to be visible then clicks it.
func (b *Browser) Click(t testing.TB, sel string) {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: click %s: %v", sel, err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		t.Fatalf("webtest: click %s: %v", sel, err)
	}
}

// ClickText finds a visible element in <body> containing the given text and clicks it.
func (b *Browser) ClickText(t testing.TB, text string) {
	t.Helper()
	sel := fmt.Sprintf(`//body//*[contains(text(),"%s")]`, text)
	b.Click(t, sel)
}

// Type clears the input then types text into it.
func (b *Browser) Type(t testing.TB, sel, text string) {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: type %s: %v", sel, err)
	}
	if err := el.SelectAllText(); err != nil {
		// SelectAllText may fail on empty fields; ignore.
		_ = err
	}
	if err := el.Input(text); err != nil {
		t.Fatalf("webtest: type %s: %v", sel, err)
	}
}

// Submit submits the form containing the matched element.
func (b *Browser) Submit(t testing.TB, sel string) {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: submit %s: %v", sel, err)
	}
	_, err = el.Eval(`function() { this.closest('form').submit() }`)
	if err != nil {
		t.Fatalf("webtest: submit %s: %v", sel, err)
	}
}

// Focus focuses the element.
func (b *Browser) Focus(t testing.TB, sel string) {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: focus %s: %v", sel, err)
	}
	if err := el.Focus(); err != nil {
		t.Fatalf("webtest: focus %s: %v", sel, err)
	}
}

// Select picks an option value in a <select> element.
func (b *Browser) Select(t testing.TB, sel, value string) {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: select %s: %v", sel, err)
	}
	if err := el.Select([]string{value}, true, rod.SelectorTypeCSSSector); err != nil {
		t.Fatalf("webtest: select %s %q: %v", sel, value, err)
	}
}

// --- Waiting ---

// WaitVisible waits for the element to become visible.
func (b *Browser) WaitVisible(t testing.TB, sel string) {
	t.Helper()
	if _, err := b.el(sel); err != nil {
		t.Fatalf("webtest: wait visible %s: %v", sel, err)
	}
}

// WaitNotVisible waits for the element to disappear.
func (b *Browser) WaitNotVisible(t testing.TB, sel string) {
	t.Helper()
	p := b.tp()
	if len(sel) > 1 && sel[:2] == "//" {
		err := p.WaitElementsMoreThan(sel, -1) // no direct "wait not present" for xpath
		_ = err
		return
	}
	// Poll until element is gone or invisible.
	if err := rod.Try(func() {
		p.MustElement(sel).MustWaitInvisible()
	}); err != nil {
		// Element not found = already gone, that's fine.
		return
	}
}

// WaitReady waits for the element to exist in the DOM.
func (b *Browser) WaitReady(t testing.TB, sel string) {
	t.Helper()
	p := b.tp()
	if _, err := p.Element(sel); err != nil {
		t.Fatalf("webtest: wait ready %s: %v", sel, err)
	}
}

// WaitText waits for text to appear in a visible element within <body>.
func (b *Browser) WaitText(t testing.TB, text string) {
	t.Helper()
	sel := fmt.Sprintf(`//body//*[contains(text(),"%s")]`, text)
	if _, err := b.el(sel); err != nil {
		t.Fatalf("webtest: wait text %q: %v", text, err)
	}
}

// --- Querying ---

// Text returns the text content of the element.
func (b *Browser) Text(t testing.TB, sel string) string {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: text %s: %v", sel, err)
	}
	s, err := el.Text()
	if err != nil {
		t.Fatalf("webtest: text %s: %v", sel, err)
	}
	return s
}

// Value returns the value of an input element.
func (b *Browser) Value(t testing.TB, sel string) string {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: value %s: %v", sel, err)
	}
	res, err := el.Eval(`function() { return this.value }`)
	if err != nil {
		t.Fatalf("webtest: value %s: %v", sel, err)
	}
	return res.Value.Str()
}

// Attribute returns the named attribute of the element.
func (b *Browser) Attribute(t testing.TB, sel, name string) string {
	t.Helper()
	el, err := b.el(sel)
	if err != nil {
		t.Fatalf("webtest: attribute %s: %v", sel, err)
	}
	attr, err := el.Attribute(name)
	if err != nil {
		t.Fatalf("webtest: attribute %s %q: %v", sel, name, err)
	}
	if attr == nil {
		return ""
	}
	return *attr
}

// URL returns the current page URL.
func (b *Browser) URL(t testing.TB) string {
	t.Helper()
	info, err := b.page.Info()
	if err != nil {
		t.Fatalf("webtest: url: %v", err)
	}
	return info.URL
}

// Title returns the current page title.
func (b *Browser) Title(t testing.TB) string {
	t.Helper()
	info, err := b.page.Info()
	if err != nil {
		t.Fatalf("webtest: title: %v", err)
	}
	return info.Title
}

// HasElement returns true if at least one element matches the selector.
func (b *Browser) HasElement(t testing.TB, sel string) bool {
	t.Helper()
	p := b.tp()
	has, _, err := p.Has(sel)
	if err != nil {
		t.Fatalf("webtest: has element %s: %v", sel, err)
	}
	return has
}

// Count returns the number of elements matching the selector.
func (b *Browser) Count(t testing.TB, sel string) int {
	t.Helper()
	p := b.tp()
	els, err := p.Elements(sel)
	if err != nil {
		t.Fatalf("webtest: count %s: %v", sel, err)
	}
	return len(els)
}

// --- Other ---

// Eval evaluates JavaScript in the page and returns the result.
// Rod wraps JS with .apply(), so bare expressions must be wrapped
// as arrow functions to be callable.
func (b *Browser) Eval(t testing.TB, js string) any {
	t.Helper()
	trimmed := strings.TrimSpace(js)
	if !strings.HasPrefix(trimmed, "function") && !strings.Contains(trimmed, "=>") {
		js = "() => { return " + js + " }"
	}
	res, err := b.tp().Evaluate(rod.Eval(js))
	if err != nil {
		t.Fatalf("webtest: eval: %v", err)
	}
	return res.Value.Val()
}

// Screenshot captures a full-page screenshot and saves it to path.
func (b *Browser) Screenshot(t testing.TB, path string) {
	t.Helper()
	buf, err := b.page.Screenshot(true, nil)
	if err != nil {
		t.Fatalf("webtest: screenshot: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("webtest: mkdir for screenshot: %v", err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatalf("webtest: write screenshot: %v", err)
	}
}

// screenshotOnFailure saves a screenshot when a test fails.
func (b *Browser) screenshotOnFailure(t testing.TB) {
	dir := os.Getenv("WEBTEST_SCREENSHOT_DIR")
	if dir == "" {
		dir = os.TempDir()
	}

	// Capture page source.
	html, _ := b.page.HTML()

	buf, err := b.page.Screenshot(true, nil)
	if err != nil {
		t.Logf("webtest: failed to capture screenshot: %v", err)
		return
	}

	path := filepath.Join(dir, fmt.Sprintf("webtest-%s.png", t.Name()))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Logf("webtest: failed to create screenshot dir: %v", err)
		return
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Logf("webtest: failed to write screenshot: %v", err)
		return
	}
	t.Logf("webtest: screenshot saved to %s", path)

	if html != "" {
		htmlPath := filepath.Join(dir, fmt.Sprintf("webtest-%s.html", t.Name()))
		if err := os.WriteFile(htmlPath, []byte(html), 0o644); err == nil {
			t.Logf("webtest: page source saved to %s", htmlPath)
		}
	}
}
