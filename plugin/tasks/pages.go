package tasks

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
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

func (h *Pages) buildSidebar(r *http.Request, activeListID int64) (sidebarData, error) {
	lists, err := h.plugin.Lists(plugin.UserFromContext(r.Context()).ID)
	if err != nil {
		return sidebarData{}, err
	}
	return sidebarData{
		Lists:      lists,
		ActiveList: activeListID,
	}, nil
}

// list returns the request user's list named by a form or path value.
func (h *Pages) list(r *http.Request, value string) (*TaskList, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, dav.ErrNotFound
	}
	return h.plugin.List(plugin.UserFromContext(r.Context()).ID, id)
}

// Index redirects to the first list or shows an empty state.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	sb, err := h.buildSidebar(r, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(sb.Lists) > 0 {
		http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(sb.Lists[0].ID, 10), http.StatusSeeOther)
		return
	}
	plugin.SetTitle(w, "Tasks")
	tasksIndexPage(sb, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ListView shows tasks for a specific list.
func (h *Pages) ListView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	list, err := h.list(r, r.PathValue("id"))
	if err != nil {
		http.Error(w, "list not found", http.StatusNotFound)
		return
	}

	allTasks, err := ListTasks(db, list.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sb, err := h.buildSidebar(r, list.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	allTasks, err := ListTasks(db, task.ListID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb, err := h.buildSidebar(r, task.ListID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, task.Summary)
	taskDetailPage(sb, task, sb.Lists, allTasks, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CreateTask handles POST to create a new task.
func (h *Pages) CreateTask(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	list, err := h.list(r, r.FormValue("list_id"))
	if err != nil {
		http.Error(w, "list not found", http.StatusBadRequest)
		return
	}
	listID := list.ID
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
	list, err := h.list(r, r.FormValue("list_id"))
	if err != nil {
		http.Error(w, "list not found", http.StatusBadRequest)
		return
	}
	listID := list.ID

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

	allTasks, err := ListTasks(db, task.ListID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	if err := DeleteTask(db, taskID, h.plugin.ctx.Now()); err != nil {
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
	name := r.FormValue("name")
	if name == "" {
		name = "New List"
	}
	list, err := h.plugin.CreateList(plugin.UserFromContext(r.Context()).ID, name, r.FormValue("color"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(list.ID, 10), http.StatusSeeOther)
}

// UpdateList handles POST to update a task list.
func (h *Pages) UpdateList(w http.ResponseWriter, r *http.Request) {
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	color := r.FormValue("color")
	if err := h.plugin.UpdateList(plugin.UserFromContext(r.Context()).ID, listID, name, color); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(listID, 10)+"/settings", http.StatusSeeOther)
}

// DeleteList handles DELETE for a task list.
func (h *Pages) DeleteList(w http.ResponseWriter, r *http.Request) {
	listID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid list id", http.StatusBadRequest)
		return
	}
	u := plugin.UserFromContext(r.Context())
	if err := h.plugin.DeleteList(u.Username, u.ID, listID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Redirect", "/apps/tasks/")
	w.WriteHeader(http.StatusOK)
}

// ListSettings shows the settings page for a list.
func (h *Pages) ListSettings(w http.ResponseWriter, r *http.Request) {
	list, err := h.list(r, r.PathValue("id"))
	if err != nil {
		http.Error(w, "list not found", http.StatusNotFound)
		return
	}
	sb, err := h.buildSidebar(r, list.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, list.Name+" Settings")
	taskListSettingsPage(sb, list, plugin.CSRFToken(r)).Render(r.Context(), w)
}
