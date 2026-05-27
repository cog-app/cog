package feed_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestServePlayerJS(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol", "pass")
	resp := env.Get(t, "/apps/feed/static/player.js", u.Auth).Status(200)
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	resp.Contains("FeedPlayer")
}

func TestServeStaticMissingReturns404(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol2", "pass")
	env.Get(t, "/apps/feed/static/does-not-exist.js", u.Auth).Status(404)
}
