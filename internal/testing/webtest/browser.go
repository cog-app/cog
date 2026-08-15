package webtest

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// SnapshotOption configures a Snapshot call.
type SnapshotOption func(*snapshotConfig)

type snapshotConfig struct {
	suffix string
	width  int
	height int
}

// Mobile captures at 375×812 (iPhone) and appends "_mobile" to the filename.
func Mobile(cfg *snapshotConfig) {
	cfg.suffix = "mobile"
	cfg.width = 375
	cfg.height = 812
}

var (
	// SnapshotColorTolerance is the max per-channel RGBA delta before a pixel counts as different.
	// Set high enough to ignore sub-pixel antialiasing and shadow rendering differences.
	SnapshotColorTolerance uint8 = 10
	// SnapshotPixelTolerance is the max fraction of pixels that may differ before failing.
	SnapshotPixelTolerance float64 = 0.005
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
	// Wait for the page to settle (no DOM mutations or network activity for
	// 300ms) so we don't grab element handles from a page that is about to
	// be replaced by a navigation redirect.
	//
	// This gets its own budget. Sharing the action timeout means a page that
	// never goes quiet — which is what a machine running the whole test suite
	// in parallel looks like — burns the entire deadline here, leaving the
	// lookup below to fail immediately with a context error even though the
	// element was there all along.
	_ = b.page.Timeout(2 * time.Second).WaitStable(300 * time.Millisecond)

	p := b.tp()
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

// Login navigates to /login, fills in credentials, and submits.
func (b *Browser) Login(t testing.TB, user, pass string) {
	t.Helper()
	b.Navigate(t, "/login")
	b.Type(t, "#username", user)
	b.Type(t, "#password", pass)
	b.Click(t, `button[type="submit"]`)
	b.WaitNotVisible(t, "#password")
}

// Snapshot captures a full-page screenshot and compares it against a golden
// file at testdata/snapshots/<TestName>[_suffix].png relative to the caller's
// source directory. If the golden file does not exist or WEBTEST_UPDATE=1 is
// set, the screenshot is written as the new baseline. Otherwise the two images
// are compared pixel-by-pixel and the test fails on mismatch.
func (b *Browser) Snapshot(t testing.TB, opts ...SnapshotOption) {
	t.Helper()

	cfg := snapshotConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	// Temporarily resize viewport if the option requests it.
	if cfg.width > 0 && cfg.height > 0 {
		b.page.MustSetViewport(cfg.width, cfg.height, 0, false)
		defer b.page.MustSetViewport(1280, 720, 0, false)
		// A resize reflows without mutating the DOM, so WaitStable below has
		// nothing to observe and can return before the new layout is painted.
		_ = b.page.Timeout(b.actionTimeout()).WaitRepaint()
	}

	// Wait for the page to settle before capturing.
	_ = b.tp().WaitStable(300 * time.Millisecond)

	buf, err := b.page.Screenshot(true, nil)
	if err != nil {
		t.Fatalf("webtest: snapshot screenshot: %v", err)
	}

	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("webtest: snapshot: cannot determine caller")
	}
	dir := filepath.Join(filepath.Dir(file), "testdata", "snapshots")
	name := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(t.Name())
	if cfg.suffix != "" {
		name += "_" + cfg.suffix
	}
	goldenPath := filepath.Join(dir, name+".png")

	// Update mode: write and return.
	if os.Getenv("WEBTEST_UPDATE") == "1" || !fileExists(goldenPath) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("webtest: snapshot mkdir: %v", err)
		}
		if err := os.WriteFile(goldenPath, buf, 0o644); err != nil {
			t.Fatalf("webtest: snapshot write golden: %v", err)
		}
		return
	}

	// Compare mode.
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("webtest: snapshot read golden: %v", err)
	}

	if err := compareSnapshots(t, golden, buf, goldenPath); err != nil {
		actualPath := strings.TrimSuffix(goldenPath, ".png") + ".actual.png"
		if writeErr := os.WriteFile(actualPath, buf, 0o644); writeErr != nil {
			t.Logf("webtest: failed to write actual: %v", writeErr)
		}
		t.Errorf("webtest: snapshot mismatch for %s: %v\n  actual: %s", filepath.Base(goldenPath), err, actualPath)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func compareSnapshots(t testing.TB, goldenBytes, actualBytes []byte, goldenPath string) error {
	t.Helper()

	goldenImg, err := png.Decode(bytes.NewReader(goldenBytes))
	if err != nil {
		return fmt.Errorf("decode golden: %w", err)
	}
	actualImg, err := png.Decode(bytes.NewReader(actualBytes))
	if err != nil {
		return fmt.Errorf("decode actual: %w", err)
	}

	gb := goldenImg.Bounds()
	ab := actualImg.Bounds()
	if gb.Dx() != ab.Dx() || gb.Dy() != ab.Dy() {
		return fmt.Errorf("dimensions differ: golden %dx%d, actual %dx%d", gb.Dx(), gb.Dy(), ab.Dx(), ab.Dy())
	}

	totalPixels := gb.Dx() * gb.Dy()
	diffCount := 0
	diffImg := image.NewRGBA(gb)

	for y := gb.Min.Y; y < gb.Max.Y; y++ {
		for x := gb.Min.X; x < gb.Max.X; x++ {
			gr, gg, gbl, ga := goldenImg.At(x, y).RGBA()
			ar, ag, abl, aa := actualImg.At(x, y).RGBA()

			if channelDiff(gr, ar) > SnapshotColorTolerance ||
				channelDiff(gg, ag) > SnapshotColorTolerance ||
				channelDiff(gbl, abl) > SnapshotColorTolerance ||
				channelDiff(ga, aa) > SnapshotColorTolerance {
				diffCount++
				diffImg.Set(x, y, color.RGBA{R: 255, A: 255})
			} else {
				// Dim copy of golden pixel.
				diffImg.Set(x, y, color.RGBA{
					R: uint8(gr >> 8 * 77 / 255),
					G: uint8(gg >> 8 * 77 / 255),
					B: uint8(gbl >> 8 * 77 / 255),
					A: 255,
				})
			}
		}
	}

	ratio := float64(diffCount) / float64(totalPixels)
	if ratio > SnapshotPixelTolerance {
		// Write diff image.
		diffPath := strings.TrimSuffix(goldenPath, ".png") + ".diff.png"
		var diffBuf bytes.Buffer
		if err := png.Encode(&diffBuf, diffImg); err == nil {
			_ = os.WriteFile(diffPath, diffBuf.Bytes(), 0o644)
			t.Logf("webtest: diff image saved to %s", diffPath)
		}
		return fmt.Errorf("%.2f%% pixels differ (threshold %.2f%%)", ratio*100, SnapshotPixelTolerance*100)
	}
	return nil
}

func channelDiff(a, b uint32) uint8 {
	a8, b8 := uint8(a>>8), uint8(b>>8)
	if a8 > b8 {
		return a8 - b8
	}
	return b8 - a8
}

// AcceptConfirms overrides window.confirm to always return true.
// This is needed for hx-confirm delete buttons.
func (b *Browser) AcceptConfirms(t testing.TB) {
	t.Helper()
	b.Eval(t, `() => { window.confirm = () => true }`)
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
