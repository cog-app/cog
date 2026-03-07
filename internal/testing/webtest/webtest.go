// Package webtest provides lightweight browser testing using Chrome DevTools Protocol.
//
// It wraps chromedp to drive a headless Chrome against a test HTTP server.
// If Chrome is not installed, tests skip gracefully.
package webtest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
)

// Engine manages a test HTTP server and a shared Chrome process.
type Engine struct {
	handler http.Handler

	mu        sync.Mutex
	server    *httptest.Server
	allocCtx  context.Context
	allocDone context.CancelFunc
	startErr  error
	started   bool
}

// New creates an Engine that serves the given handler.
func New(handler http.Handler) *Engine {
	return &Engine{handler: handler}
}

// Start launches a browser tab and returns a Browser. The Chrome process
// and HTTP server are created lazily on the first call and reused.
func (e *Engine) Start(ctx context.Context) (*Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.ensureStartedLocked(); err != nil {
		return nil, err
	}

	tabCtx, tabCancel := chromedp.NewContext(e.allocCtx)
	// Navigate to about:blank to ensure the tab is alive.
	if err := chromedp.Run(tabCtx, chromedp.Navigate("about:blank")); err != nil {
		tabCancel()
		return nil, fmt.Errorf("webtest: new tab: %w", err)
	}

	return &Browser{
		ctx:     tabCtx,
		cancel:  tabCancel,
		baseURL: e.server.URL,
	}, nil
}

// StartT is like Start but uses testing helpers: it skips if Chrome is
// unavailable and registers cleanup that screenshots on failure.
func (e *Engine) StartT(t testing.TB) *Browser {
	t.Helper()

	b, err := e.Start(t.Context())
	if err != nil {
		t.Skipf("webtest: %v", err)
	}

	t.Cleanup(func() {
		if t.Failed() {
			b.screenshotOnFailure(t)
		}
		b.cancel()
	})

	return b
}

// Close shuts down the Chrome process and test server.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.allocDone != nil {
		e.allocDone()
		e.allocDone = nil
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

	// Create Chrome allocator (one headless process, shared across tabs).
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.NoSandbox,
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	// Verify Chrome works by creating a test tab.
	testCtx, testCancel := chromedp.NewContext(allocCtx)
	err := chromedp.Run(testCtx, chromedp.Navigate("about:blank"))
	testCancel()

	if err != nil {
		allocCancel()
		e.server.Close()
		e.server = nil
		e.startErr = fmt.Errorf("chrome unavailable: %w", err)
		return e.startErr
	}

	e.allocCtx = allocCtx
	e.allocDone = allocCancel
	return nil
}
