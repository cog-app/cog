package tasks_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

func TestBrowser(t *testing.T) {

	t.Run("CreateListAndTask", func(t *testing.T) {
		eng := browserEnv(t, "create")
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "My List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.Type(t, `[data-testid="new-task-summary"]`, "First task")
		b.Click(t, `[data-testid="new-task-submit"]`)
		b.WaitVisible(t, `#task-tree .task-item input[type="checkbox"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ToggleComplete", func(t *testing.T) {
		eng := browserEnv(t, "toggle")
		b := eng.Start(t)
		b.Login(t, "toggle", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "Toggle List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.Type(t, `[data-testid="new-task-summary"]`, "Toggle me")
		b.Click(t, `[data-testid="new-task-submit"]`)
		b.Click(t, `#task-tree .task-item input[type="checkbox"]`)
		b.WaitVisible(t, ".task-item.completed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteTask", func(t *testing.T) {
		eng := browserEnv(t, "delete-task")
		b := eng.Start(t)
		b.Login(t, "delete-task", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "Delete Task List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.Type(t, `[data-testid="new-task-summary"]`, "Delete me")
		b.Click(t, `[data-testid="new-task-submit"]`)
		b.WaitVisible(t, `#task-tree .task-item`)
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `#task-tree .task-item`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteList", func(t *testing.T) {
		eng := browserEnv(t, "delete-list")
		b := eng.Start(t)
		b.Login(t, "delete-list", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "Doomed List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.WaitText(t, "Doomed List")
		b.ClickText(t, "Settings")
		b.WaitVisible(t, `[hx-delete]`)
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `[hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// browserEnv starts a server and browser of its own for one subtest, with one
// user. Collection IDs are global, so a shared server would give a subtest
// different IDs, and a different golden, depending on which others ran.
func browserEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, tasks.New())
	// The first user is made an admin; the one under test is not.
	env.CreateUser(t, "admin", "pass")
	env.CreateUser(t, username, "pass")
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
}
