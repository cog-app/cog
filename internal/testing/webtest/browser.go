package webtest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	// state names one of several snapshots a test takes; suffix names the
	// device it was taken on.
	state  string
	suffix string
	width  int
	height int
	// noOverflow fails the snapshot when the page is wider than the
	// viewport, which on a phone means the whole page scrolls sideways.
	noOverflow bool
}

// State names a snapshot when a test takes more than one, appending
// "_<name>" to the filename before any device suffix.
func State(name string) SnapshotOption {
	return func(cfg *snapshotConfig) { cfg.state = name }
}

// Mobile captures at 375×812 (iPhone) and appends "_mobile" to the filename.
// It also fails the test when the document is wider than the viewport: a
// full-page screenshot of such a page silently comes out wider than a phone
// and is laid out at that width, which hides exactly the problem it shows.
func Mobile(cfg *snapshotConfig) {
	cfg.suffix = "mobile"
	cfg.width = 375
	cfg.height = 812
	cfg.noOverflow = true
}

// Browser controls a single browser tab.
type Browser struct {
	page     *rod.Page
	baseURL  string
	timeout  time.Duration
	sessions SessionMinter // nil when the handler cannot start sessions
}

// defaultTimeout bounds one action: a navigation, a wait, a click. It is
// generous because a whole test suite running beside the browser makes
// pages slow; a test that is merely slow still passes, and one waiting for
// something that never comes still fails. WEBTEST_TIMEOUT overrides it.
var defaultTimeout = timeoutFromEnv(30 * time.Second)

func timeoutFromEnv(d time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv("WEBTEST_TIMEOUT")); err == nil && v > 0 {
		return v
	}
	return d
}

// SetTimeout overrides the default action timeout.
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
	// 100ms) so we don't grab element handles from a page that is about to
	// be replaced by a navigation redirect. A redirect or an HTMX swap is a
	// request in flight, which the wait covers however long it takes, so the
	// quiet period only has to outlast the scripts that run when one lands.
	//
	// This gets its own budget. Sharing the action timeout means a page that
	// never goes quiet — which is what a machine running the whole test suite
	// in parallel looks like — burns the entire deadline here, leaving the
	// lookup below to fail immediately with a context error even though the
	// element was there all along.
	//
	// A page that never settles is not a failure in itself: the lookup below
	// waits for the element and reports if it is not there.
	_ = waitQuiet(b.page.Timeout(5*time.Second), 100*time.Millisecond)

	return b.firstVisible(sel)
}

// firstVisible waits for an element matching sel (CSS, or XPath when it
// starts with //) that is visible, and returns the first such one. Matching
// only the first element in the document would wait forever on a hidden
// copy of the same text earlier in the page, such as a navbar menu that is
// collapsed at this width.
func (b *Browser) firstVisible(sel string) (*rod.Element, error) {
	deadline := time.Now().Add(b.actionTimeout())
	xpath := len(sel) > 1 && sel[:2] == "//"
	for {
		var (
			els rod.Elements
			err error
		)
		if xpath {
			els, err = b.page.ElementsX(sel)
		} else {
			els, err = b.page.Elements(sel)
		}
		if err != nil {
			// A page mid-navigation has no document to search; try again.
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("find %s: %w", sel, err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, el := range els {
			visible, err := el.Visible()
			if err == nil && visible {
				return el, nil
			}
		}
		if time.Now().After(deadline) {
			if len(els) == 0 {
				return nil, fmt.Errorf("no element matches %s: %w", sel, context.DeadlineExceeded)
			}
			return nil, fmt.Errorf("%d elements match %s, none visible: %w", len(els), sel, context.DeadlineExceeded)
		}
		time.Sleep(100 * time.Millisecond)
	}
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
	// SelectAllText fails on an empty field, which has nothing to replace.
	_ = el.SelectAllText()
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

// WaitNotVisible waits until no element matching sel (CSS, or XPath when it
// starts with //) is visible: every match is hidden, or none is left.
func (b *Browser) WaitNotVisible(t testing.TB, sel string) {
	t.Helper()
	if err := b.noneVisible(sel); err != nil {
		t.Fatalf("webtest: wait not visible %s: %v", sel, err)
	}
}

// noneVisible polls until nothing matching sel is visible. It used to wait
// for a match to exist and then for it to hide, so an element that was
// already removed held the test for the whole action timeout.
//
// Each poll is one script returning a count. Fetching the matches as
// element handles broke when the action navigated, as submitting a login
// form does: rod retries an evaluation whose page context is gone, but not
// the call that reads the handles it returned, so every poll failed with
// "Cannot find context" until the timeout.
func (b *Browser) noneVisible(sel string) error {
	deadline := time.Now().Add(b.actionTimeout())
	xpath := len(sel) > 1 && sel[:2] == "//"
	for {
		res, err := b.page.Eval(countVisible, sel, xpath)
		if err == nil && res.Value.Int() == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("find %s: %w", sel, err)
			}
			return fmt.Errorf("%d elements matching %s still visible: %w", res.Value.Int(), sel, context.DeadlineExceeded)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// countVisible counts the nodes matching a CSS selector, or an XPath
// expression, that are visible by rod's test (lib/js helper.js visible):
// displayed, not hidden, and with a box.
const countVisible = `(sel, xpath) => {
	let els = [];
	if (xpath) {
		const r = document.evaluate(sel, document, null, XPathResult.ORDERED_NODE_SNAPSHOT_TYPE, null);
		for (let i = 0; i < r.snapshotLength; i++) els.push(r.snapshotItem(i));
	} else {
		els = Array.from(document.querySelectorAll(sel));
	}
	return els.filter(node => {
		// A text node an XPath matched is judged by its element, as rod does.
		const el = node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement;
		if (!el) return false;
		const box = el.getBoundingClientRect();
		const style = getComputedStyle(el);
		return style.display !== 'none' && style.visibility !== 'hidden' && !!(box.top || box.bottom || box.width || box.height);
	}).length;
}`

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
	sel := fmt.Sprintf(`//body//*[text()[contains(.,"%s")]]`, text)
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
	// Counting a page that is still loading counts part of it.
	if err := p.WaitLoad(); err != nil {
		t.Fatalf("webtest: count %s: page did not load: %v", sel, err)
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("webtest: mkdir for screenshot: %v", err)
	}
	if err := b.captureImage(path); err != nil {
		t.Fatalf("webtest: screenshot: %v", err)
	}
}

// SessionMinter is implemented by a handler that can start a signed-in
// session itself (cogtest.Env does). BrowserSession checks the password and
// returns the session cookie, recording userAgent as the browser's, or nil
// when the form would not have signed the user straight in: a wrong
// password, or a second factor still to give.
type SessionMinter interface {
	BrowserSession(username, password, userAgent string) (*http.Cookie, error)
}

// Login signs the browser in as user and opens the page the login form
// would have landed on.
//
// When the handler is a SessionMinter the session is started directly and
// its cookie set: a sign-in through the form is five actions and three page
// loads, and nearly every browser test begins with one. Otherwise, or when
// the minter declines, it is LoginForm, so a wrong password or an MFA user
// behaves exactly as the form does.
func (b *Browser) Login(t testing.TB, user, pass string) {
	t.Helper()
	if b.sessions != nil {
		ua, err := b.page.Eval(`() => navigator.userAgent`)
		if err != nil {
			t.Fatalf("webtest: login: user agent: %v", err)
		}
		c, err := b.sessions.BrowserSession(user, pass, ua.Value.Str())
		if err != nil {
			t.Fatalf("webtest: login: %v", err)
		}
		if c != nil {
			if _, err := (proto.NetworkSetCookie{
				Name:     c.Name,
				Value:    c.Value,
				URL:      b.baseURL,
				HTTPOnly: true,
				SameSite: proto.NetworkCookieSameSiteLax,
			}).Call(b.page); err != nil {
				t.Fatalf("webtest: login: set cookie: %v", err)
			}
			b.Navigate(t, "/")
			return
		}
	}
	b.LoginForm(t, user, pass)
}

// LoginForm navigates to /login, fills in credentials, and submits. Use it
// in a test of the form itself; Login is quicker for one that only needs to
// be signed in.
func (b *Browser) LoginForm(t testing.TB, user, pass string) {
	t.Helper()
	b.Navigate(t, "/login")
	b.Type(t, "#username", user)
	b.Type(t, "#password", pass)
	b.Click(t, `button[type="submit"]`)
	b.WaitNotVisible(t, "#password")
	// The page logged in to may still be arriving; a test that counts or
	// snapshots it next would see part of it.
	if err := b.tp().WaitLoad(); err != nil {
		t.Fatalf("webtest: login: page after login did not load: %v", err)
	}
}

// Snapshot compares the page against a golden pair in testdata/snapshots,
// relative to the caller's source directory: <TestName>[_suffix].html holds
// the normalised DOM and is the assertion, and the .png beside it is a
// rendering of that DOM for a human to look at in review.
//
// The DOM is what the application controls. Pixels also depend on the browser
// that drew them, so asserting on those makes a Chromium upgrade indis-
// tinguishable from a change to the page — a screenshot suite drifts on
// somebody else's release schedule.
//
// A screenshot is only taken when the DOM golden does not match, which is why
// the image stays in step with the markup without ever being compared. CSS is
// covered by the same mechanism: stylesheets are served with a content hash in
// their URL, so editing one changes the DOM.
//
// With WEBTEST_UPDATE=1, or when no golden exists, both files are written.
// imagesSettledJS reports whether every image in the viewport has loaded
// or failed.
//
// A video or audio element counts too: until it has its metadata or has
// given up on its source, the player draws a loading spinner, caught at a
// different angle in every picture.
const imagesSettledJS = `() => {
	const inView = (el) => {
		const r = el.getBoundingClientRect();
		return r.bottom > 0 && r.right > 0 && r.top < innerHeight && r.left < innerWidth;
	};
	const imagesDone = [...document.images].every((img) => !inView(img) || img.complete);
	const mediaDone = [...document.querySelectorAll("video, audio")].every((m) =>
		!inView(m) || m.readyState >= HTMLMediaElement.HAVE_METADATA || m.error ||
		m.networkState === HTMLMediaElement.NETWORK_NO_SOURCE || m.networkState === HTMLMediaElement.NETWORK_IDLE);
	return imagesDone && mediaDone;
}`

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
		// A resize reflows without mutating the DOM, so waitQuiet below has
		// nothing to observe and can return before the new layout is painted.
		if err := b.page.Timeout(b.actionTimeout()).WaitRepaint(); err != nil {
			t.Logf("webtest: snapshot: repaint after resize: %v", err)
		}
	}

	// A page still loading has a truncated DOM and no scripts run yet; on a
	// loaded machine an element the test waited for can appear well before
	// the rest of the document. Capturing that would record a page nobody
	// sees, so it is an error, not a golden.
	if err := b.tp().WaitLoad(); err != nil {
		t.Fatalf("webtest: snapshot: page did not finish loading: %v", err)
	}
	// Wait for the page to settle before capturing. A page that never
	// settles is still captured, and the golden will say what moved.
	if err := waitQuiet(b.tp(), 300*time.Millisecond); err != nil {
		t.Logf("webtest: snapshot: page did not settle: %v", err)
	}
	// A lazy image does not hold up the load event, and on a loaded machine
	// its request can outlast the quiet period above; its load or error
	// handler (a placeholder for a missing thumbnail) then changes the page
	// after it was captured. Wait for every image in view to finish, then
	// for what its handlers do. Lazy images out of view never start.
	if err := b.tp().Wait(rod.Eval(imagesSettledJS)); err != nil {
		t.Logf("webtest: snapshot: images did not finish loading: %v", err)
	} else if err := waitQuiet(b.tp(), 100*time.Millisecond); err != nil {
		t.Logf("webtest: snapshot: page did not settle after images: %v", err)
	}

	if cfg.noOverflow {
		if msg := b.horizontalOverflow(t); msg != "" {
			t.Errorf("webtest: page is wider than the %dpx viewport, so it scrolls sideways: %s", cfg.width, msg)
		}
	}

	dom, unmask, err := b.maskedDOM()
	if err != nil {
		t.Fatalf("webtest: snapshot: %v", err)
	}
	defer func() {
		if err := unmask(); err != nil {
			t.Errorf("webtest: snapshot: %v", err)
		}
	}()

	_, file, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("webtest: snapshot: cannot determine caller")
	}
	dir := filepath.Join(filepath.Dir(file), "testdata", "snapshots")
	name := strings.NewReplacer("/", "_", "\\", "_", " ", "_").Replace(t.Name())
	if cfg.state != "" {
		name += "_" + cfg.state
	}
	if cfg.suffix != "" {
		name += "_" + cfg.suffix
	}
	domPath := filepath.Join(dir, name+".html")
	imagePath := filepath.Join(dir, name+".png")

	golden, readErr := os.ReadFile(domPath)
	unchanged := readErr == nil && string(golden) == dom

	if unchanged && os.Getenv("WEBTEST_UPDATE") != "1" {
		// The page is what it was. Rendering it would cost a screenshot and
		// tell us nothing.
		return
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("webtest: snapshot mkdir: %v", err)
	}

	// Accept a change when asked to, or when there is no golden to compare
	// against. Otherwise record what the page looks like now and fail: a DOM
	// change is for a person to approve.
	accept := os.Getenv("WEBTEST_UPDATE") == "1" || readErr != nil
	domOut, imageOut := domPath, imagePath
	if !accept {
		domOut = filepath.Join(dir, name+".actual.html")
		imageOut = filepath.Join(dir, name+".actual.png")
	}

	if err := os.WriteFile(domOut, []byte(dom), 0o644); err != nil {
		t.Fatalf("webtest: snapshot write dom: %v", err)
	}
	if err := b.captureImage(imageOut); err != nil {
		t.Fatalf("webtest: snapshot screenshot: %v", err)
	}

	if !accept {
		t.Errorf("webtest: page changed for %s\n  golden: %s\n  actual: %s\n  image:  %s\nRe-run with WEBTEST_UPDATE=1 to accept.",
			name, domPath, domOut, imageOut)
	}
}

// overflowJS reports what makes the document wider than the viewport, or ""
// when nothing does. It names the outermost offending elements, skipping any
// inside a box that clips or scrolls horizontally: a wide table inside a
// .table-wrap scrolls on its own and is not a page overflow.
const overflowJS = `() => {
	const doc = document.documentElement;
	const vw = doc.clientWidth;
	if (doc.scrollWidth <= vw) return "";
	const clipped = (el) => {
		for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
			const ox = getComputedStyle(p).overflowX;
			if (ox !== "visible") return true;
		}
		return false;
	};
	const describe = (el) => {
		let d = el.tagName.toLowerCase();
		if (el.id) d += "#" + el.id;
		if (typeof el.className === "string" && el.className.trim()) d += "." + el.className.trim().split(/\s+/).join(".");
		return d;
	};
	const out = [];
	for (const el of document.body.querySelectorAll("*")) {
		const r = el.getBoundingClientRect();
		if (r.width === 0 || r.right <= vw + 0.5) continue;
		const p = el.parentElement;
		if (p && p !== document.body && p.getBoundingClientRect().right > vw + 0.5) continue;
		if (getComputedStyle(el).position === "fixed" || clipped(el)) continue;
		out.push(describe(el) + " (right edge " + Math.round(r.right) + "px)");
		if (out.length >= 5) break;
	}
	return "document is " + doc.scrollWidth + "px wide; " + (out.join(", ") || "no single element found");
}`

// horizontalOverflow returns a description of what makes the page wider than
// the viewport, or "" when it fits.
func (b *Browser) horizontalOverflow(t testing.TB) string {
	t.Helper()
	res, err := b.tp().Evaluate(rod.Eval(overflowJS))
	if err != nil {
		t.Fatalf("webtest: measure overflow: %v", err)
	}
	return res.Value.Str()
}

// captureImage writes a full-page screenshot. It exists so the DOM comparison
// above never pays for one it does not need.
func (b *Browser) captureImage(path string) error {
	// Stop the document's clock first: what page CSS cannot reach, such as
	// a video player's loading spinner in the browser's own shadow tree,
	// stops with it, and stillJS restarts the player so it stops at its
	// first frame.
	if err := (proto.AnimationSetPlaybackRate{PlaybackRate: 0}).Call(b.page); err != nil {
		return fmt.Errorf("stop animations: %w", err)
	}
	buf, err := func() ([]byte, error) {
		if _, err := b.tp().Eval(stillJS); err != nil {
			return nil, fmt.Errorf("still page: %w", err)
		}
		return b.page.Screenshot(true, nil)
	}()
	if _, stirErr := b.page.Eval(unstillJS); stirErr != nil {
		err = errors.Join(err, fmt.Errorf("unstill page: %w", stirErr))
	}
	if stirErr := (proto.AnimationSetPlaybackRate{PlaybackRate: 1}).Call(b.page); stirErr != nil {
		err = errors.Join(err, fmt.Errorf("restart animations: %w", stirErr))
	}
	if err != nil {
		return err
	}

	return os.WriteFile(path, buf, 0o644)
}

// stillJS holds the page still for its picture. A transition or animation
// caught part way (a field's focus ring easing in, a spinner, a shimmer) and
// a blinking caret make the same page look different from one run to the
// next. The sheet is adopted rather than inserted, so it never appears in the
// DOM, and it waits two frames so what it stopped is painted stopped.
const stillJS = `async () => {
	const sheet = new CSSStyleSheet();
	sheet.replaceSync("*, *::before, *::after { animation: none !important; transition: none !important; caret-color: transparent !important; scroll-behavior: auto !important; }");
	document.adoptedStyleSheets = [...document.adoptedStyleSheets, sheet];
	window.__webtestStill = sheet;
	// A player still loading, or with nothing it can play, spins. The
	// clock is stopped, so taking its controls away and giving them back
	// starts the spinner again at its first frame and holds it there.
	for (const m of document.querySelectorAll("video[controls], audio[controls]")) {
		if (m.readyState >= HTMLMediaElement.HAVE_METADATA) continue;
		m.controls = false;
		m.getBoundingClientRect();
		m.controls = true;
	}
	// An image decoded while it was painted may have been decoded at the
	// size it was drawn, or at its own, depending on timing, and the two
	// scale to slightly different pixels. Decode them all whole first. One
	// that has not loaded (a lazy image out of view) would hold decode()
	// until it does, which is never; it is not in the picture anyway.
	await Promise.all([...document.images]
		.filter((img) => img.complete && img.naturalWidth > 0)
		.map((img) => img.decode().catch(() => {})));
	await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)));
}`

// unstillJS lets the page move again, for whatever the test does next.
const unstillJS = `() => {
	const sheet = window.__webtestStill;
	document.adoptedStyleSheets = document.adoptedStyleSheets.filter((s) => s !== sheet);
	delete window.__webtestStill;
}`

// maskedDOM masks the volatile values in the page and returns its golden
// form, with a function that puts the page back. The golden and the image are
// both taken while it is masked, so they say the same thing; restoring it
// keeps what the test does next from reading or submitting a masked value.
func (b *Browser) maskedDOM() (dom string, unmask func() error, err error) {
	masks := make([][2]string, len(volatileMasks))
	for i, m := range volatileMasks {
		masks[i] = [2]string{m.re.String(), m.with}
	}
	if _, err := b.page.Eval(maskJS, masks); err != nil {
		return "", nil, fmt.Errorf("mask page: %w", err)
	}
	unmask = func() error {
		if _, err := b.page.Eval(unmaskJS); err != nil {
			return fmt.Errorf("unmask page: %w", err)
		}
		return nil
	}
	raw, err := b.page.HTML()
	if err != nil {
		return "", nil, errors.Join(fmt.Errorf("snapshot html: %w", err), unmask())
	}
	dom, err = normalizeDOM(raw)
	if err != nil {
		return "", nil, errors.Join(err, unmask())
	}
	return dom, unmask, nil
}

// maskJS applies the masks, given as [regexp source, replacement] pairs, to
// the page: its text, its attributes, and the values in its fields, which a
// field the test typed into no longer keeps in its attribute. What it changed
// is kept for unmaskJS.
//
// Attributes that make the browser fetch something are left alone: changing
// an avatar's src loads /avatar/«hex», which is nobody's, and the picture
// shows a "?" where the user's initial was. Those are masked in the golden
// alone (attrs).
const maskJS = `(masks) => {
	const res = masks.map(([src, with_]) => [new RegExp(src, "g"), with_]);
	const mask = (s) => res.reduce((out, [re, with_]) => out.replace(re, with_), s);
	const loads = (el, name) => name === "src" || name === "srcset" || name === "poster" ||
		(name === "href" && el.tagName === "LINK");
	const undo = [];
	const walker = document.createTreeWalker(document.documentElement, NodeFilter.SHOW_TEXT, {
		acceptNode: (n) => n.parentElement && n.parentElement.closest("script, style")
			? NodeFilter.FILTER_REJECT : NodeFilter.FILTER_ACCEPT,
	});
	for (let n = walker.nextNode(); n; n = walker.nextNode()) {
		const old = n.data, masked = mask(old);
		if (masked !== old) {
			n.data = masked;
			undo.push(() => { n.data = old; });
		}
	}
	for (const el of document.querySelectorAll("*")) {
		for (const a of [...el.attributes]) {
			if (loads(el, a.name)) continue;
			const old = a.value, masked = mask(old);
			if (masked !== old) {
				el.setAttribute(a.name, masked);
				undo.push(() => el.setAttribute(a.name, old));
			}
		}
	}
	for (const el of document.querySelectorAll("input, textarea")) {
		const old = el.value, masked = mask(old);
		if (masked !== old) {
			el.value = masked;
			undo.push(() => { el.value = old; });
		}
	}
	window.__webtestUnmask = undo;
}`

// unmaskJS puts back what maskJS changed, last change first.
const unmaskJS = `() => {
	for (const f of (window.__webtestUnmask || []).reverse()) f();
	delete window.__webtestUnmask;
}`

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

	path := filepath.Join(dir, fmt.Sprintf("webtest-%s.png", t.Name()))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Logf("webtest: failed to create screenshot dir: %v", err)
		return
	}

	// The page source first: it is what tells a server-side error from a
	// slow page, so it should not depend on the screenshot working.
	if html, err := b.page.HTML(); err != nil {
		t.Logf("webtest: failed to capture page source: %v", err)
	} else {
		htmlPath := filepath.Join(dir, fmt.Sprintf("webtest-%s.html", t.Name()))
		if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
			t.Logf("webtest: failed to write page source: %v", err)
		} else {
			t.Logf("webtest: page source saved to %s", htmlPath)
		}
	}

	buf, err := b.page.Screenshot(true, nil)
	if err != nil {
		t.Logf("webtest: failed to capture screenshot: %v", err)
		return
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Logf("webtest: failed to write screenshot: %v", err)
		return
	}
	t.Logf("webtest: screenshot saved to %s", path)
}
