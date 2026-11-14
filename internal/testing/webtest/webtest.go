// Package webtest provides lightweight browser testing using Chrome DevTools Protocol.
//
// It wraps go-rod to drive a headless Chrome against a test HTTP server.
// If Chrome is not installed, tests skip gracefully.
package webtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// sharedBrowser is one Chrome process for every engine in the test binary.
// It is launched when the first engine needs it. When the last engine closes
// it lingers for browserIdle before closing, and a later engine launches it
// again. (It used to launch once, so every engine created after the first one
// closed got a dead browser and its tests skipped.) While it runs, the binary
// holds a browser slot (slot.go).
//
// Most suites give each subtest an engine of its own, so without the linger
// every subtest relaunched Chrome and queued for a slot again: binaries took
// turns subtest by subtest, each paid for every other's runtime against its
// own go test timeout, and the largest package timed out. Lingering keeps a
// binary's run of subtests together. Leakless (the launcher's default) kills
// Chrome if the binary exits while it lingers.
var sharedBrowser struct {
	mu       sync.Mutex
	browser  *rod.Browser
	refCount int
	release  func()      // frees this binary's browser slot
	idle     *time.Timer // closes the browser once no engine has used it for browserIdle
}

// browserIdle is how long an unused browser keeps its slot, waiting for the
// next subtest's engine.
const browserIdle = 2 * time.Second

func acquireBrowser() (*rod.Browser, error) {
	sharedBrowser.mu.Lock()
	defer sharedBrowser.mu.Unlock()
	if sharedBrowser.idle != nil {
		sharedBrowser.idle.Stop()
		sharedBrowser.idle = nil
	}
	if sharedBrowser.browser == nil {
		path, found := launcher.LookPath()
		if !found {
			return nil, launcher.ErrAlreadyLaunched
		}
		release, err := acquireSlot()
		if err != nil {
			return nil, err
		}
		// Rasterise the same page the same way every run: decode images
		// before painting rather than beside it, repaint whole tiles, and
		// use one colour profile whatever the machine's is. Otherwise a
		// snapshot re-taken for an unrelated reason differs from the last
		// in a few pixels of an image's edge.
		u, err := launcher.New().Bin(path).Headless(true).NoSandbox(true).
			Set("disable-checker-imaging").
			Set("disable-partial-raster").
			Set("force-color-profile", "srgb").
			Launch()
		if err != nil {
			release()
			return nil, err
		}
		b := rod.New().ControlURL(u)
		if err := b.Connect(); err != nil {
			release()
			return nil, err
		}
		sharedBrowser.browser, sharedBrowser.release = b, release
	}
	sharedBrowser.refCount++
	return sharedBrowser.browser, nil
}

func releaseBrowser() {
	sharedBrowser.mu.Lock()
	defer sharedBrowser.mu.Unlock()
	sharedBrowser.refCount--
	if sharedBrowser.refCount == 0 && sharedBrowser.browser != nil {
		var t *time.Timer
		t = time.AfterFunc(browserIdle, func() { closeIdleBrowser(t) })
		sharedBrowser.idle = t
	}
}

// closeIdleBrowser closes the browser and frees the slot, unless an engine
// took the browser again after timer t was set.
func closeIdleBrowser(t *time.Timer) {
	sharedBrowser.mu.Lock()
	defer sharedBrowser.mu.Unlock()
	if sharedBrowser.idle != t || sharedBrowser.refCount != 0 || sharedBrowser.browser == nil {
		return
	}
	sharedBrowser.idle = nil
	if err := sharedBrowser.browser.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "webtest: close browser: %v\n", err)
	}
	sharedBrowser.browser = nil
	sharedBrowser.release()
}

// Engine manages a test HTTP server and opens tabs on the shared Chrome process.
type Engine struct {
	handler      http.Handler
	dateOverride string        // ISO date injected as JS Date override
	sessions     SessionMinter // the handler, when it can start sessions

	mu      sync.Mutex
	server  *httptest.Server
	browser *rod.Browser
	started bool
	err     error
}

// New creates an Engine that serves the given handler.
// If the handler implements a Now() time.Time method (e.g. cogtest.Env),
// the browser's Date object is monkey-patched to return that time. If it is
// a SessionMinter, Login signs in without the form.
func New(handler http.Handler) *Engine {
	e := &Engine{handler: handler}
	e.sessions, _ = handler.(SessionMinter)
	type clock interface{ Now() time.Time }
	if c, ok := handler.(clock); ok {
		e.dateOverride = c.Now().Format(time.RFC3339)
	}
	return e
}

func (e *Engine) start() (*Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.ensureStartedLocked(); err != nil {
		return nil, err
	}

	page, err := e.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, err
	}

	// Clear cookies so each tab starts with a clean session.
	// Scoped to this page's browser context via CDP.
	cookies, err := proto.NetworkGetAllCookies{}.Call(page)
	if err == nil && len(cookies.Cookies) > 0 {
		var params proto.NetworkClearBrowserCookies
		params.Call(page)
	}

	page.MustSetViewport(1280, 720, 0, false)

	if e.dateOverride != "" {
		js := fmt.Sprintf(`(() => {
			const __cogFixedNow = new Date(%q).getTime();
			const __OrigDate = Date;
			function CogDate(...args) {
				if (args.length === 0) return new __OrigDate(__cogFixedNow);
				return new __OrigDate(...args);
			}
			CogDate.prototype = __OrigDate.prototype;
			CogDate.now = () => __cogFixedNow;
			CogDate.parse = __OrigDate.parse;
			CogDate.UTC = __OrigDate.UTC;
			Date = CogDate;
		})()`, e.dateOverride)
		if _, err := page.EvalOnNewDocument(js); err != nil {
			page.Close()
			return nil, err
		}
	}

	return &Browser{
		page:     page,
		baseURL:  e.server.URL,
		sessions: e.sessions,
	}, nil
}

// Start launches a browser tab and returns a Browser. The Chrome process
// and HTTP server are created lazily on the first call and reused.
// It skips if Chrome is unavailable and registers cleanup that screenshots
// on failure.
func (e *Engine) Start(t testing.TB) *Browser {
	t.Helper()

	b, err := e.start()
	if err != nil {
		t.Skipf("webtest: %v", err)
	}

	t.Cleanup(func() {
		if t.Failed() {
			b.screenshotOnFailure(t)
		}
		b.page.Close()
	})

	return b
}

// Close shuts down the test server and releases the shared Chrome reference.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser != nil {
		releaseBrowser()
		e.browser = nil
	}
	if e.server != nil {
		e.server.Close()
		e.server = nil
	}
}

// ensureStartedLocked initializes the server and acquires the shared Chrome.
// Must be called with e.mu held.
func (e *Engine) ensureStartedLocked() error {
	if e.started {
		return e.err
	}
	e.started = true

	e.server = httptest.NewServer(e.handler)

	browser, err := acquireBrowser()
	if err != nil {
		e.server.Close()
		e.server = nil
		e.err = err
		return err
	}

	e.browser = browser
	return nil
}
