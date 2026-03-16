package tasks

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"github.com/google/uuid"
)

// parseDatetimeLocal parses an HTML datetime-local input value to a unix timestamp pointer.
func parseDatetimeLocal(s string) *int64 {
	if s == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02T15:04", s)
	if err != nil {
		return nil
	}
	ts := t.Unix()
	return &ts
}

// Pages holds the web UI handlers for the tasks plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

func (h *Pages) wrap(fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(fn)
}

type sidebarData struct {
	Lists      []TaskList
	ActiveList int64
}

func (h *Pages) buildSidebar(db *sql.DB, activeListID int64) sidebarData {
	lists, _ := ListTaskLists(db)
	return sidebarData{
		Lists:      lists,
		ActiveList: activeListID,
	}
}

// Index redirects to the first list or shows an empty state.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	EnsureDefaultList(db)

	lists, _ := ListTaskLists(db)
	if len(lists) > 0 {
		http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(lists[0].ID, 10), http.StatusSeeOther)
		return
	}
	plugin.SetTitle(w, "Tasks")
	sb := h.buildSidebar(db, 0)
	tasksIndexPage(sb, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ListView shows tasks for a specific list.
func (h *Pages) ListView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}
	list, err := GetTaskList(db, listID)
	if err != nil {
		http.Error(w, "list not found", http.StatusNotFound)
		return
	}

	_, allTasks, err := ChildMap(db, listID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sb := h.buildSidebar(db, listID)
	plugin.SetTitle(w, list.Name)
	tasksListPage(sb, list, allTasks, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// TaskDetail shows a single task edit form.
func (h *Pages) TaskDetail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	task, err := GetTask(db, taskID)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	lists, _ := ListTaskLists(db)
	allTasks, _ := ListTasks(db, task.ListID)

	sb := h.buildSidebar(db, task.ListID)
	plugin.SetTitle(w, task.Summary)
	taskDetailPage(sb, task, lists, allTasks, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CreateTask handles POST to create a new task.
func (h *Pages) CreateTask(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	listID, _ := strconv.ParseInt(r.FormValue("list_id"), 10, 64)
	summary := r.FormValue("summary")
	if summary == "" {
		http.Error(w, "summary required", http.StatusBadRequest)
		return
	}
	description := r.FormValue("description")
	priority, _ := strconv.Atoi(r.FormValue("priority"))

	var parentID *int64
	if pid := r.FormValue("parent_id"); pid != "" {
		p, err := strconv.ParseInt(pid, 10, 64)
		if err == nil && p > 0 {
			parentID = &p
		}
	}

	var dueAt *int64
	if d := r.FormValue("due_at"); d != "" {
		if ts, err := strconv.ParseInt(d, 10, 64); err == nil {
			dueAt = &ts
		}
	}

	var startAt *int64
	if s := r.FormValue("start_at"); s != "" {
		if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
			startAt = &ts
		}
	}

	uid := uuid.New().String()
	_, err = CreateTask(db, listID, uid, parentID, summary, description, priority, dueAt, startAt, h.plugin.ctx.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(listID, 10), http.StatusSeeOther)
}

// UpdateTask handles POST to update a task.
func (h *Pages) UpdateTask(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	summary := r.FormValue("summary")
	description := r.FormValue("description")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	status := r.FormValue("status")
	if status == "" {
		status = "NEEDS-ACTION"
	}
	percentComplete, _ := strconv.Atoi(r.FormValue("percent_complete"))
	listID, _ := strconv.ParseInt(r.FormValue("list_id"), 10, 64)

	var parentID *int64
	if pid := r.FormValue("parent_id"); pid != "" {
		p, err := strconv.ParseInt(pid, 10, 64)
		if err == nil && p > 0 {
			parentID = &p
		}
	}

	dueAt := parseDatetimeLocal(r.FormValue("due_at_date"))
	startAt := parseDatetimeLocal(r.FormValue("start_at_date"))

	var completedAt *int64
	if status == "COMPLETED" {
		now := h.plugin.ctx.Now().Unix()
		completedAt = &now
	}

	if err := UpdateTask(db, taskID, summary, description, priority, status, percentComplete, dueAt, startAt, completedAt, listID, parentID, h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(listID, 10), http.StatusSeeOther)
}

// ToggleTask handles POST to toggle task completion (HTMX partial).
func (h *Pages) ToggleTask(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	task, err := ToggleTask(db, taskID, h.plugin.ctx.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allTasks, _ := ListTasks(db, task.ListID)
	taskItem(task, allTasks, 0, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// DeleteTask handles DELETE (HTMX).
func (h *Pages) DeleteTask(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	taskID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	if err := DeleteTask(db, taskID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Reorder handles POST JSON for drag-and-drop reordering.
func (h *Pages) Reorder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req struct {
		ListID   int64   `json:"list_id"`
		ParentID *int64  `json:"parent_id"`
		TaskIDs  []int64 `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := ReorderTasks(db, req.ListID, req.ParentID, req.TaskIDs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// CreateList handles POST to create a new task list.
func (h *Pages) CreateList(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		name = "New List"
	}
	color := r.FormValue("color")
	uid := uuid.New().String()

	list, err := CreateTaskList(db, uid, name, color)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(list.ID, 10), http.StatusSeeOther)
}

// UpdateList handles POST to update a task list.
func (h *Pages) UpdateList(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	color := r.FormValue("color")
	if err := UpdateTaskList(db, listID, name, color); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(listID, 10)+"/settings", http.StatusSeeOther)
}

// DeleteList handles DELETE for a task list.
func (h *Pages) DeleteList(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}
	if err := DeleteTaskList(db, listID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/apps/tasks/")
	w.WriteHeader(http.StatusOK)
}

// ListSettings shows the settings page for a list.
func (h *Pages) ListSettings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}
	list, err := GetTaskList(db, listID)
	if err != nil {
		http.Error(w, "list not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, listID)
	plugin.SetTitle(w, list.Name+" Settings")
	taskListSettingsPage(sb, list, plugin.CSRFToken(r)).Render(r.Context(), w)
}
