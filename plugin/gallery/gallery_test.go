package gallery_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

func TestGalleryPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/", alice.Auth).Status(200)
}

func TestGalleryAlbumsPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/albums", alice.Auth).Status(200)
}

func TestGallerySettingsPage(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/gallery/settings", alice.Auth).Status(200)
}
