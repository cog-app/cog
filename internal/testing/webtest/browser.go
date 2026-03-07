package webtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Browser controls a single browser tab.
type Browser struct {
	ctx     context.Context
	cancel  context.CancelFunc
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

func (b *Browser) run(t testing.TB, actions ...chromedp.Action) {
	t.Helper()
	ctx, cancel := context.WithTimeout(b.ctx, b.actionTimeout())
	defer cancel()
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatalf("webtest: %v", err)
	}
}

// --- Navigation ---

// Navigate goes to the given path on the test server.
func (b *Browser) Navigate(t testing.TB, path string) {
	t.Helper()
	b.run(t, chromedp.Navigate(b.baseURL+path))
}

// NavigateURL goes to an absolute URL.
func (b *Browser) NavigateURL(t testing.TB, url string) {
	t.Helper()
	b.run(t, chromedp.Navigate(url))
}

// --- Interaction ---

// Click waits for the element to be visible then clicks it.
func (b *Browser) Click(t testing.TB, sel string) {
	t.Helper()
	b.run(t,
		chromedp.WaitVisible(sel),
		chromedp.Click(sel),
	)
}

// ClickText finds an element containing the given text and clicks it.
func (b *Browser) ClickText(t testing.TB, text string) {
	t.Helper()
	sel := fmt.Sprintf(`//*[contains(text(),"%s")]`, text)
	b.run(t,
		chromedp.WaitVisible(sel),
		chromedp.Click(sel),
	)
}

// Type clears the input then types text into it.
func (b *Browser) Type(t testing.TB, sel, text string) {
	t.Helper()
	b.run(t,
		chromedp.WaitVisible(sel),
		chromedp.Clear(sel),
		chromedp.SendKeys(sel, text),
	)
}

// Submit submits the form containing the matched element.
func (b *Browser) Submit(t testing.TB, sel string) {
	t.Helper()
	b.run(t,
		chromedp.WaitVisible(sel),
		chromedp.Submit(sel),
	)
}

// Focus focuses the element.
func (b *Browser) Focus(t testing.TB, sel string) {
	t.Helper()
	b.run(t, chromedp.Focus(sel))
}

// Select picks an option value in a <select> element.
func (b *Browser) Select(t testing.TB, sel, value string) {
	t.Helper()
	b.run(t,
		chromedp.WaitVisible(sel),
		chromedp.SetValue(sel, value),
	)
}

// --- Waiting ---

// WaitVisible waits for the element to become visible.
func (b *Browser) WaitVisible(t testing.TB, sel string) {
	t.Helper()
	b.run(t, chromedp.WaitVisible(sel))
}

// WaitNotVisible waits for the element to disappear.
func (b *Browser) WaitNotVisible(t testing.TB, sel string) {
	t.Helper()
	b.run(t, chromedp.WaitNotPresent(sel))
}

// WaitReady waits for the element to exist in the DOM.
func (b *Browser) WaitReady(t testing.TB, sel string) {
	t.Helper()
	b.run(t, chromedp.WaitReady(sel))
}

// WaitText waits for text to appear anywhere on the page.
func (b *Browser) WaitText(t testing.TB, text string) {
	t.Helper()
	sel := fmt.Sprintf(`//*[contains(text(),"%s")]`, text)
	b.run(t, chromedp.WaitVisible(sel))
}

// --- Querying ---

// Text returns the text content of the element.
func (b *Browser) Text(t testing.TB, sel string) string {
	t.Helper()
	var s string
	b.run(t, chromedp.Text(sel, &s))
	return s
}

// Value returns the value of an input element.
func (b *Browser) Value(t testing.TB, sel string) string {
	t.Helper()
	var s string
	b.run(t, chromedp.Value(sel, &s))
	return s
}

// Attribute returns the named attribute of the element.
func (b *Browser) Attribute(t testing.TB, sel, name string) string {
	t.Helper()
	var attrs map[string]string
	b.run(t, chromedp.Attributes(sel, &attrs))
	return attrs[name]
}

// URL returns the current page URL.
func (b *Browser) URL(t testing.TB) string {
	t.Helper()
	var loc string
	b.run(t, chromedp.Location(&loc))
	return loc
}

// Title returns the current page title.
func (b *Browser) Title(t testing.TB) string {
	t.Helper()
	var title string
	b.run(t, chromedp.Title(&title))
	return title
}

// HasElement returns true if at least one element matches the selector.
func (b *Browser) HasElement(t testing.TB, sel string) bool {
	t.Helper()
	var nodes []*cdp.Node
	ctx, cancel := context.WithTimeout(b.ctx, b.actionTimeout())
	defer cancel()
	// Use Nodes with AtLeast(0) so it doesn't error on zero matches.
	if err := chromedp.Run(ctx, chromedp.Nodes(sel, &nodes, chromedp.AtLeast(0))); err != nil {
		t.Fatalf("webtest: HasElement: %v", err)
	}
	return len(nodes) > 0
}

// Count returns the number of elements matching the selector.
func (b *Browser) Count(t testing.TB, sel string) int {
	t.Helper()
	var nodes []*cdp.Node
	ctx, cancel := context.WithTimeout(b.ctx, b.actionTimeout())
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.Nodes(sel, &nodes, chromedp.AtLeast(0))); err != nil {
		t.Fatalf("webtest: Count: %v", err)
	}
	return len(nodes)
}

// --- Other ---

// Eval evaluates JavaScript in the page and returns the result.
func (b *Browser) Eval(t testing.TB, js string) any {
	t.Helper()
	var res any
	b.run(t, chromedp.Evaluate(js, &res))
	return res
}

// Screenshot captures a full-page screenshot and saves it to path.
func (b *Browser) Screenshot(t testing.TB, path string) {
	t.Helper()
	var buf []byte
	ctx, cancel := context.WithTimeout(b.ctx, b.actionTimeout())
	defer cancel()
	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
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

	var buf []byte
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()

	// Also capture the page source for debugging.
	var html string
	_ = chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		node, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		body, err := page.GetResourceContent(node.Frame.ID, "").Do(ctx)
		if err == nil {
			html = string(body)
		}
		return nil
	}))

	if err := chromedp.Run(ctx, chromedp.FullScreenshot(&buf, 90)); err != nil {
		t.Logf("webtest: failed to capture screenshot: %v", err)
		return
	}

	path := filepath.Join(dir, fmt.Sprintf("webtest-%s.png", t.Name()))
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
