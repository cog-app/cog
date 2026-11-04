package gallery_test

import (
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

// The scan worker's profiles are an admin's to read.
func TestWorkerPprofNeedsAdmin(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	env.CreateUser(t, "admin", "pass") // the first user is the admin
	bob := env.CreateUser(t, "bob", "pass")

	env.Get(t, "/apps/gallery/api/worker/pprof/").Status(http.StatusUnauthorized)
	env.Get(t, "/apps/gallery/api/worker/pprof/heap", bob.Auth).Status(http.StatusForbidden)
	env.Get(t, "/apps/memories/api/days").Status(http.StatusUnauthorized)
}
