package tasks_test

import (
	"strconv"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
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

	t.Run("PopulatedList", func(t *testing.T) {
		eng := populatedEnv(t, "list")
		b := eng.Start(t)
		b.Login(t, "list", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.ClickText(t, "Home Renovation")
		b.WaitText(t, "Choose paint colours")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SubtaskForm", func(t *testing.T) {
		eng := populatedEnv(t, "subform")
		b := eng.Start(t)
		b.Login(t, "subform", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.ClickText(t, "Home Renovation")
		b.WaitText(t, "Choose paint colours")
		b.Click(t, "#task-tree .subtask-add summary")
		b.WaitVisible(t, `#task-tree .subtask-add input[name="summary"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditTask", func(t *testing.T) {
		eng := populatedEnv(t, "edit")
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.ClickText(t, "Home Renovation")
		b.WaitText(t, "Choose paint colours")
		b.ClickText(t, "Choose paint colours")
		b.WaitText(t, "Edit Task")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ListSettings", func(t *testing.T) {
		eng := populatedEnv(t, "settings")
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.ClickText(t, "Home Renovation")
		b.WaitText(t, "Choose paint colours")
		b.Click(t, `a[href$="/settings"]`)
		b.WaitText(t, "Danger Zone")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// populatedEnv is browserEnv with a second list holding a tree of tasks:
// every priority, due dates, a completed task, three levels of subtasks and a
// summary long enough to wrap. Due dates are in 2025 — the page formats them
// against the wall clock, and a year that is never the current one keeps the
// golden stable.
func populatedEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, tasks.New())
	env.CreateUser(t, "admin", "pass")
	u := env.CreateUser(t, username, "pass")
	plug, _ := env.Providers.Plugins.Plugin("tasks")
	tp := plug.(*tasks.Plugin)
	db, err := tp.DB(username)
	require.NoError(t, err)
	_, err = tp.Lists(u.ID)
	require.NoError(t, err)
	list, err := tp.CreateList(u.ID, "Home Renovation", "#43a047")
	require.NoError(t, err)

	day := func(d int) *time.Time {
		t := time.Date(2025, time.March, d, 0, 0, 0, 0, time.UTC)
		return &t
	}
	n := 0
	add := func(nt tasks.NewTask) *tasks.Task {
		n++
		task, err := tasks.CreateTask(db, list, "task-"+strconv.Itoa(n), nt, env.Now())
		require.NoError(t, err)
		return task
	}
	kitchen := add(tasks.NewTask{Summary: "Kitchen", Priority: 1, Due: day(28)})
	paint := add(tasks.NewTask{Summary: "Choose paint colours", Description: "Sample pots from the shop on Elm Street; compare in daylight.", Priority: 5, Due: day(10), ParentUID: kitchen.UID})
	add(tasks.NewTask{Summary: "Order sample pots", ParentUID: paint.UID})
	add(tasks.NewTask{Summary: "Get three quotes for new cabinets", Priority: 9, ParentUID: kitchen.UID})
	done := add(tasks.NewTask{Summary: "Measure the windows", Due: day(3)})
	_, err = tasks.ToggleTask(db, list, done, env.Now())
	require.NoError(t, err)
	add(tasks.NewTask{Summary: "Call the electrician about rewiring the upstairs bedrooms before the plasterer arrives, and ask whether the old fuse box needs replacing too", Priority: 1, Due: day(5)})
	add(tasks.NewTask{Summary: "Pick up tiles"})

	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
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
