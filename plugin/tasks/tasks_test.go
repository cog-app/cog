package tasks_test

import (
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

func setupDB(t *testing.T) (*cogtest.Env, *tasks.Plugin, *cogtest.User) {
	t.Helper()
	env := cogtest.New(t, tasks.New())
	alice := env.CreateUser(t, "alice", "pass")
	plug, _ := env.Providers.Plugins.Plugin("tasks")
	tp := plug.(*tasks.Plugin)
	return env, tp, alice
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// --- Store tests ---

func TestCreateListAndGet(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "test-uid", "Test List", "#ff0000")
	require.NoError(t, err)
	require.Equal(t, "Test List", list.Name)
	require.Equal(t, "#ff0000", list.Color)

	got, err := tasks.GetTaskList(db, list.ID)
	require.NoError(t, err)
	require.Equal(t, "Test List", got.Name)

	byUID, err := tasks.GetTaskListByUID(db, "test-uid")
	require.NoError(t, err)
	require.Equal(t, list.ID, byUID.ID)
}

func TestEnsureDefaultList(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	require.NoError(t, tasks.EnsureDefaultList(db))
	lists, err := tasks.ListTaskLists(db)
	require.NoError(t, err)
	require.Equal(t, 1, len(lists))
	require.Equal(t, "Personal", lists[0].Name)

	// Calling again should not create another.
	require.NoError(t, tasks.EnsureDefaultList(db))
	lists, err = tasks.ListTaskLists(db)
	require.NoError(t, err)
	require.Equal(t, 1, len(lists))
}

func TestCreateTaskAndToggle(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "list1", "My List", "")
	require.NoError(t, err)

	task, err := tasks.CreateTask(db, list.ID, "task-uid-1", nil, "Buy milk", "From store", 1, nil, nil)
	require.NoError(t, err)
	require.Equal(t, "Buy milk", task.Summary)
	require.Equal(t, "NEEDS-ACTION", task.Status)
	require.Equal(t, 1, task.Priority)

	// Toggle to completed.
	toggled, err := tasks.ToggleTask(db, task.ID)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", toggled.Status)
	require.Equal(t, 100, toggled.PercentComplete)

	// Toggle back.
	toggled2, err := tasks.ToggleTask(db, task.ID)
	require.NoError(t, err)
	require.Equal(t, "NEEDS-ACTION", toggled2.Status)
	require.Equal(t, 0, toggled2.PercentComplete)
}

func TestDeleteTask(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "list1", "List", "")
	require.NoError(t, err)

	task, err := tasks.CreateTask(db, list.ID, "del-uid", nil, "Delete me", "", 0, nil, nil)
	require.NoError(t, err)

	require.NoError(t, tasks.DeleteTask(db, task.ID))
	_, err = tasks.GetTask(db, task.ID)
	require.True(t, err != nil)
}

func TestHierarchy_CreateAndCascadeDelete(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "list1", "List", "")
	require.NoError(t, err)

	parent, err := tasks.CreateTask(db, list.ID, "parent-uid", nil, "Parent Task", "", 0, nil, nil)
	require.NoError(t, err)

	child1, err := tasks.CreateTask(db, list.ID, "child1-uid", &parent.ID, "Child 1", "", 0, nil, nil)
	require.NoError(t, err)
	require.True(t, child1.ParentID != nil)
	require.Equal(t, parent.ID, *child1.ParentID)

	_, err = tasks.CreateTask(db, list.ID, "child2-uid", &parent.ID, "Child 2", "", 0, nil, nil)
	require.NoError(t, err)

	// Grandchild under child1.
	_, err = tasks.CreateTask(db, list.ID, "grandchild-uid", &child1.ID, "Grandchild", "", 0, nil, nil)
	require.NoError(t, err)

	allTasks, err := tasks.ListTasks(db, list.ID)
	require.NoError(t, err)
	require.Equal(t, 4, len(allTasks))

	roots := tasks.RootTasks(allTasks)
	require.Equal(t, 1, len(roots))
	require.Equal(t, "Parent Task", roots[0].Summary)

	children := tasks.ChildrenOf(allTasks, parent.ID)
	require.Equal(t, 2, len(children))

	grandchildren := tasks.ChildrenOf(allTasks, child1.ID)
	require.Equal(t, 1, len(grandchildren))

	// Delete parent — cascade deletes all children.
	require.NoError(t, tasks.DeleteTask(db, parent.ID))
	allTasks, err = tasks.ListTasks(db, list.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(allTasks))
}

func TestReorderTasks(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "list1", "List", "")
	require.NoError(t, err)

	t1, err := tasks.CreateTask(db, list.ID, "t1", nil, "First", "", 0, nil, nil)
	require.NoError(t, err)
	t2, err := tasks.CreateTask(db, list.ID, "t2", nil, "Second", "", 0, nil, nil)
	require.NoError(t, err)
	t3, err := tasks.CreateTask(db, list.ID, "t3", nil, "Third", "", 0, nil, nil)
	require.NoError(t, err)

	// Reverse order.
	require.NoError(t, tasks.ReorderTasks(db, list.ID, nil, []int64{t3.ID, t2.ID, t1.ID}))

	reordered, err := tasks.ListTasks(db, list.ID)
	require.NoError(t, err)
	require.Equal(t, 3, len(reordered))
	require.Equal(t, "Third", reordered[0].Summary)
	require.Equal(t, "Second", reordered[1].Summary)
	require.Equal(t, "First", reordered[2].Summary)
}

func TestReparentTask(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "list1", "List", "")
	require.NoError(t, err)

	parent, err := tasks.CreateTask(db, list.ID, "p1", nil, "Parent", "", 0, nil, nil)
	require.NoError(t, err)
	child, err := tasks.CreateTask(db, list.ID, "c1", nil, "Child", "", 0, nil, nil)
	require.NoError(t, err)

	require.NoError(t, tasks.ReparentTask(db, child.ID, &parent.ID, 0))

	updated, err := tasks.GetTask(db, child.ID)
	require.NoError(t, err)
	require.True(t, updated.ParentID != nil)
	require.Equal(t, parent.ID, *updated.ParentID)
}

// --- ICS tests ---

func TestVTodoRoundTrip(t *testing.T) {
	fields := tasks.VTodoFields{
		UID:             "todo-123",
		Summary:         "Test Task",
		Description:     "A description with, commas",
		Priority:        5,
		PercentComplete: 50,
		Status:          "IN-PROCESS",
	}

	ics := tasks.GenerateVTodoICS("todo-123", fields)
	require.True(t, strings.Contains(ics, "UID:todo-123"))
	require.True(t, strings.Contains(ics, "SUMMARY:Test Task"))
	require.True(t, strings.Contains(ics, "PRIORITY:5"))
	require.True(t, strings.Contains(ics, "STATUS:IN-PROCESS"))
	require.True(t, strings.Contains(ics, "BEGIN:VTODO"))

	parsed, err := tasks.ParseVTodo(ics)
	require.NoError(t, err)
	require.Equal(t, "todo-123", parsed.UID)
	require.Equal(t, "Test Task", parsed.Summary)
	require.Equal(t, "A description with, commas", parsed.Description)
	require.Equal(t, 5, parsed.Priority)
	require.Equal(t, 50, parsed.PercentComplete)
	require.Equal(t, "IN-PROCESS", parsed.Status)
}

func TestParseVTodoNoTodo(t *testing.T) {
	_, err := tasks.ParseVTodo("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")
	require.True(t, err != nil)
}

func TestParseVTodoNoUID(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VTODO\r\nSUMMARY:No UID\r\nEND:VTODO\r\nEND:VCALENDAR\r\n"
	_, err := tasks.ParseVTodo(ics)
	require.True(t, err != nil)
}

// --- Page tests ---

func TestTasksIndexPage(t *testing.T) {
	env, _, alice := setupDB(t)
	env.Get(t, "/apps/tasks/", alice.Auth).Status(200)
}

func TestTasksListPage(t *testing.T) {
	env, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "test-list", "Test", "")
	require.NoError(t, err)

	env.Get(t, "/apps/tasks/list/"+itoa(list.ID), alice.Auth).
		Status(200).Contains("Test")
}

func TestTasksTaskDetailPage(t *testing.T) {
	env, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "detail-list", "Detail", "")
	require.NoError(t, err)
	task, err := tasks.CreateTask(db, list.ID, "detail-uid", nil, "Detail Task", "A description", 5, nil, nil)
	require.NoError(t, err)

	env.Get(t, "/apps/tasks/task/"+itoa(task.ID), alice.Auth).
		Status(200).Contains("Detail Task")
}

func TestTasksListSettingsPage(t *testing.T) {
	env, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "settings-list", "Settings", "#ff0000")
	require.NoError(t, err)

	env.Get(t, "/apps/tasks/list/"+itoa(list.ID)+"/settings", alice.Auth).
		Status(200).Contains("Settings")
}

func TestUpcomingTasks(t *testing.T) {
	_, tp, alice := setupDB(t)
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	list, err := tasks.CreateTaskList(db, "upcoming-list", "Upcoming", "")
	require.NoError(t, err)

	// Task with a due date.
	due := int64(9999999999)
	_, err = tasks.CreateTask(db, list.ID, "due-uid", nil, "Due Task", "", 0, &due, nil)
	require.NoError(t, err)

	// Task without a due date — should not appear.
	_, err = tasks.CreateTask(db, list.ID, "nodue-uid", nil, "No Due", "", 0, nil, nil)
	require.NoError(t, err)

	upcoming, err := tasks.UpcomingTasks(db, 10)
	require.NoError(t, err)
	require.Equal(t, 1, len(upcoming))
	require.Equal(t, "Due Task", upcoming[0].Summary)
	require.Equal(t, "Upcoming", upcoming[0].ListName)
}
