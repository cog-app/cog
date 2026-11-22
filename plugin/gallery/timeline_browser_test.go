package gallery_test

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

// waitTrue polls js until it is true. Scrolling is what loads the timeline,
// so a test scrolls inside js and waits for what it loaded.
func waitTrue(t *testing.T, b *webtest.Browser, what, js string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for b.Eval(t, js) != true {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestBrowserTimelineScroll(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	user := env.CreateUser(t, "scroll", "pass")
	db, err := env.Providers.Plugins.PluginDB(user.Username, "gallery")
	require.NoError(t, err)
	seedTimeline(t, db, time.Date(2024, 3, 31, 12, 0, 0, 0, time.UTC), 300)

	t.Run("ScrollsToTheEnd", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "scroll", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
		if got := b.Text(t, "[data-scrubber-current]"); got != "March 2024" {
			t.Errorf("scrubber at the top says %q", got)
		}
		waitTrue(t, b, "every photo", `(window.scrollTo(0, document.documentElement.scrollHeight),
			document.querySelectorAll(".gallery-thumb").length === 300 &&
			!document.querySelector("[data-timeline-more]"))`)
		waitTrue(t, b, "the scrubber to reach January",
			`document.querySelector("[data-scrubber-current]").textContent === "January 2024"`)
	})

	// A month that has not loaded yet opens the timeline there; the months
	// after it load above without moving what is on screen.
	t.Run("JumpsToAMonth", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "scroll", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
		b.Click(t, `.gallery-scrubber-month[data-month="2024-01"]`)
		waitTrue(t, b, "January", `document.querySelector("[data-scrubber-current]").textContent === "January 2024"`)
		if !strings.Contains(b.URL(t), "?before=") {
			// January was already on the page and the click scrolled to it.
			return
		}
		waitTrue(t, b, "February to load above", `!!document.querySelector('.gallery-day[data-month="2024-02"]')`)
		time.Sleep(300 * time.Millisecond) // let a wrong scroll show
		// Without the correction January would be pushed a page down the
		// screen; with it, January's first day stays at the top, where the
		// jump put it.
		top := b.Eval(t, `document.querySelector('.gallery-day[data-month="2024-01"]').getBoundingClientRect().top`)
		if f, ok := top.(float64); !ok || f < -2 || f > 2 {
			t.Errorf("after loading above, January's first day is at %v", top)
		}
		if got := b.Text(t, "[data-scrubber-current]"); got != "January 2024" {
			t.Errorf("after loading above, the scrubber says %q", got)
		}
	})
}
