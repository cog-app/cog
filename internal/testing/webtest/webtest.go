// Package webtest provides lightweight browser testing using Chrome DevTools Protocol.
//
// It wraps go-rod to drive a headless Chrome against a test HTTP server.
// If Chrome is not installed, tests skip gracefully.
package webtest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Engine manages a test HTTP server and a shared Chrome process.
type Engine struct {
	handler      http.Handler
	dateOverride string // ISO date injected as JS Date override

	mu       sync.Mutex
	server   *httptest.Server
	browser  *rod.Browser
	startErr error
	started  bool
}

// New creates an Engine that serves the given handler.
// If the handler implements a Now() time.Time method (e.g. cogtest.Env),
// the browser's Date object is monkey-patched to return that time.
func New(handler http.Handler) *Engine {
	e := &Engine{handler: handler}
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
	if err := e.browser.SetCookies(nil); err != nil {
		page.Close()
		return nil, err
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
		page:    page,
		baseURL: e.server.URL,
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

// Close shuts down the Chrome process and test server.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser != nil {
		e.browser.Close()
		e.browser = nil
	}
	if e.server != nil {
		e.server.Close()
		e.server = nil
	}
}

// ensureStartedLocked initializes the server and Chrome allocator.
// Must be called with e.mu held.
func (e *Engine) ensureStartedLocked() error {
	if e.started {
		return e.startErr
	}
	e.started = true

	// Create test server.
	e.server = httptest.NewServer(e.handler)

	// Find Chrome. Don't auto-download; skip if not available.
	path, found := launcher.LookPath()
	if !found {
		e.server.Close()
		e.server = nil
		e.startErr = launcher.ErrAlreadyLaunched // reuse sentinel
		return e.startErr
	}

	u := launcher.New().Bin(path).Headless(true).NoSandbox(true).MustLaunch()
	browser := rod.New().ControlURL(u)
	if err := browser.Connect(); err != nil {
		e.server.Close()
		e.server = nil
		e.startErr = err
		return e.startErr
	}

	e.browser = browser
	return nil
}
