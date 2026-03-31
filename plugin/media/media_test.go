package media_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/media"
)

func TestMediaPages(t *testing.T) {
	env := cogtest.New(t, media.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/media/", alice.Auth).Status(200).Contains("Queue")
	env.Get(t, "/apps/media/browse", alice.Auth).Status(200).Contains("Browse")
	env.Get(t, "/apps/media/settings", alice.Auth).Status(200).Contains("Add Source")
	env.Get(t, "/apps/media/history", alice.Auth).Status(200).Contains("History")
}

func TestMediaSeed(t *testing.T) {
	env := cogtest.New(t, media.New())
	admin := env.CreateUser(t, "admin", "pass")
	admin.Seed(t)

	env.Get(t, "/apps/media/settings", admin.Auth).Status(200).Contains("Cog Dev Podcast")
	env.Get(t, "/apps/media/", admin.Auth).Status(200).Contains("Queue")
}

func TestMediaUserIsolation(t *testing.T) {
	env := cogtest.New(t, media.New())
	admin := env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	admin.Seed(t)

	env.Get(t, "/apps/media/settings", admin.Auth).Status(200).Contains("Cog Dev Podcast")

	body := env.Get(t, "/apps/media/settings", bob.Auth).Status(200).ReadBody()
	if strings.Contains(body, "Cog Dev Podcast") {
		t.Error("Bob should not see admin's seeded data")
	}
}
