package tasks

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"github.com/google/uuid"
)

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
	list, err := h.list(r, strconv.FormatInt(task.ListID, 10))
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
	z := ical.ZonesFor(task.Object, list.Zone())
	m := task.Object.Components[0]
	times := formTimes{Due: inputValue(m.Due, z, list.Zone()), Start: inputValue(m.Start, z, list.Zone()), Zone: list.Zone().String()}
	plugin.SetTitle(w, task.Summary)
	if err := taskDetailPage(sb, task, sb.Lists, allTasks, times, plugin.CSRFToken(r)).Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render task", "error", err)
	}
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
	summary := r.FormValue("summary")
	if summary == "" {
		http.Error(w, "summary required", http.StatusBadRequest)
		return
	}
	nt := NewTask{Summary: summary, Description: r.FormValue("description")}
	if nt.Priority, err = formInt(r, "priority", 0, 9); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if pid := r.FormValue("parent_id"); pid != "" {
		p, err := strconv.ParseInt(pid, 10, 64)
		if err != nil {
			http.Error(w, "invalid parent", http.StatusBadRequest)
			return
		}
		parent, err := GetTask(db, p)
		if err != nil || parent.ListID != list.ID {
			http.Error(w, "parent not found", http.StatusBadRequest)
			return
		}
		nt.ParentUID = parent.UID
	}
	for _, f := range []struct {
		name string
		dst  **time.Time
	}{{"due_at", &nt.Due}, {"start_at", &nt.Start}} {
		if v := r.FormValue(f.name); v != "" {
			ts, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, "invalid "+f.name, http.StatusBadRequest)
				return
			}
			t := time.Unix(ts, 0)
			*f.dst = &t
		}
	}

	if _, err := CreateTask(db, list, uuid.New().String(), nt, h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(list.ID, 10), http.StatusSeeOther)
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

	task, err := GetTask(db, taskID)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	oldList, err := h.list(r, strconv.FormatInt(task.ListID, 10))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	list, err := h.list(r, r.FormValue("list_id"))
	if err != nil {
		http.Error(w, "list not found", http.StatusBadRequest)
		return
	}
	f, err := parseTaskForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if pid := r.FormValue("parent_id"); pid != "" {
		p, err := strconv.ParseInt(pid, 10, 64)
		if err != nil {
			http.Error(w, "invalid parent", http.StatusBadRequest)
			return
		}
		parent, err := GetTask(db, p)
		if err != nil || parent.ListID != task.ListID || parent.ID == task.ID {
			http.Error(w, "parent not found", http.StatusBadRequest)
			return
		}
		f.parentUID = parent.UID
	} else if task.ParentID == nil {
		// The form only offers parents in the same list; one elsewhere,
		// or not yet synced, stays.
		f.parentUID = task.ParentUID
	}

	// Times in the form are shown in the list's zone the task is in.
	if err := applyForm(task.Object, f, oldList.Zone(), h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := SaveTask(db, list, task, task.Object, h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/tasks/list/"+strconv.FormatInt(list.ID, 10), http.StatusSeeOther)
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

	task, err := GetTask(db, taskID)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	list, err := h.list(r, strconv.FormatInt(task.ListID, 10))
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	task, err = ToggleTask(db, list, task, h.plugin.ctx.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allTasks, err := ListTasks(db, task.ListID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for i := range allTasks {
		if allTasks[i].ID == task.ID {
			task = &allTasks[i]
		}
	}
	if err := taskItem(task, allTasks, 0, plugin.CSRFToken(r)).Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render task", "error", err)
	}
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

	list, err := h.list(r, strconv.FormatInt(req.ListID, 10))
	if err != nil {
		http.Error(w, "list not found", http.StatusBadRequest)
		return
	}
	if err := ReorderTasks(db, list, req.ParentID, req.TaskIDs, h.plugin.ctx.Now()); err != nil {
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
