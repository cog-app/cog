package dashboard_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
)

func TestDashboardPage(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Dashboard is the root route.
	env.Get(t, "/", alice.Auth).Status(200)
}

func TestDashboardEditPage(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/dashboard/edit", alice.Auth).Status(200)
}
