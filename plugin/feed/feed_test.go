package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestRegister(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "u", "pass")
	if _, ok := env.Providers.Plugins.Plugin("feed"); !ok {
		t.Fatal("feed plugin not registered")
	}
}
