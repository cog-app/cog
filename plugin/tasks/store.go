package tasks

import (
	"crypto/md5"
	"database/sql"
	"time"
	"fmt"
)

// TaskList is a container for tasks (shareable resource).
type TaskList struct {
	ID        int64
	UID       string
	Name      string
	Color     string
	SortOrder int
	CTag      int64
	SyncToken int64
	CreatedAt int64
	UpdatedAt int64
}

// Task is a single task item with hierarchy and VTODO sync support.
type Task struct {
	ID              int64
	ListID          int64
	UID             string
	ETag            string
	ParentID        *int64
	Summary         string
	Description     string
	Priority        int
	PercentComplete int
	Status          string
	CompletedAt     *int64
	DueAt           *int64
	StartAt         *int64
	SortOrder       int
	ICSData         string
	CreatedAt       int64
	UpdatedAt       int64
}

// TaskWithList extends Task with list metadata for display (e.g. widget).
type TaskWithList struct {
	Task
	ListName  string
	ListColor string
}

// --- List CRUD ---

func ListTaskLists(db *sql.DB) ([]TaskList, error) {
	rows, err := db.Query(`SELECT id, uid, name, color, sort_order, ctag, sync_token, created_at, updated_at FROM lists ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lists []TaskList
	for rows.Next() {
		var l TaskList
		if err := rows.Scan(&l.ID, &l.UID, &l.Name, &l.Color, &l.SortOrder, &l.CTag, &l.SyncToken, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		lists = append(lists, l)
	}
	return lists, rows.Err()
}

func GetTaskList(db *sql.DB, id int64) (*TaskList, error) {
	var l TaskList
	err := db.QueryRow(`SELECT id, uid, name, color, sort_order, ctag, sync_token, created_at, updated_at FROM lists WHERE id = ?`, id).
		Scan(&l.ID, &l.UID, &l.Name, &l.Color, &l.SortOrder, &l.CTag, &l.SyncToken, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func GetTaskListByUID(db *sql.DB, uid string) (*TaskList, error) {
	var l TaskList
	err := db.QueryRow(`SELECT id, uid, name, color, sort_order, ctag, sync_token, created_at, updated_at FROM lists WHERE uid = ?`, uid).
		Scan(&l.ID, &l.UID, &l.Name, &l.Color, &l.SortOrder, &l.CTag, &l.SyncToken, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func CreateTaskList(db *sql.DB, uid, name, color string) (*TaskList, error) {
	if color == "" {
		color = "#0082c9"
	}
	res, err := db.Exec(`INSERT INTO lists (uid, name, color) VALUES (?, ?, ?)`, uid, name, color)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetTaskList(db, id)
}

func UpdateTaskList(db *sql.DB, id int64, name, color string) error {
	_, err := db.Exec(`UPDATE lists SET name = ?, color = ?, updated_at = unixepoch() WHERE id = ?`, name, color, id)
	return err
}

func DeleteTaskList(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM lists WHERE id = ?`, id)
	return err
}

func EnsureDefaultList(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM lists`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := CreateTaskList(db, "personal", "Personal", "#0082c9")
		return err
	}
	return nil
}

func BumpCTag(db *sql.DB, listID int64) error {
	_, err := db.Exec(`UPDATE lists SET ctag = ctag + 1, sync_token = sync_token + 1, updated_at = unixepoch() WHERE id = ?`, listID)
	return err
}

// TaskCount returns the number of incomplete tasks in a list.
func TaskCount(db *sql.DB, listID int64) int {
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE list_id = ? AND status != 'COMPLETED'`, listID).Scan(&count)
	return count
}

// --- Task CRUD ---

const taskColumns = `id, list_id, uid, etag, parent_id, summary, description, priority, percent_complete, status, completed_at, due_at, start_at, sort_order, ics_data, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ListID, &t.UID, &t.ETag, &t.ParentID, &t.Summary, &t.Description,
		&t.Priority, &t.PercentComplete, &t.Status, &t.CompletedAt, &t.DueAt, &t.StartAt,
		&t.SortOrder, &t.ICSData, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *t)
	}
	return tasks, rows.Err()
}

func ListTasks(db *sql.DB, listID int64) ([]Task, error) {
	rows, err := db.Query(`SELECT `+taskColumns+` FROM tasks WHERE list_id = ? ORDER BY sort_order, created_at`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func GetTask(db *sql.DB, id int64) (*Task, error) {
	return scanTask(db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
}

func GetTaskByUID(db *sql.DB, listID int64, uid string) (*Task, error) {
	return scanTask(db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE list_id = ? AND uid = ?`, listID, uid))
}

func CreateTask(db *sql.DB, listID int64, uid string, parentID *int64, summary, description string, priority int, dueAt, startAt *int64, now time.Time) (*Task, error) {
	// Get next sort_order for this parent.
	var maxSort int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM tasks WHERE list_id = ? AND parent_id IS ?`, listID, parentID).Scan(&maxSort)

	icsData := GenerateVTodoICS(uid, VTodoFields{
		UID:     uid,
		Summary: summary, Description: description,
		Priority: priority,
		Status:   "NEEDS-ACTION",
	}, now)
	etag := fmt.Sprintf("%x", md5.Sum([]byte(icsData)))

	res, err := db.Exec(`INSERT INTO tasks (list_id, uid, etag, parent_id, summary, description, priority, due_at, start_at, sort_order, ics_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		listID, uid, etag, parentID, summary, description, priority, dueAt, startAt, maxSort+1, icsData)
	if err != nil {
		return nil, err
	}
	if err := BumpCTag(db, listID); err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetTask(db, id)
}

func UpdateTask(db *sql.DB, id int64, summary, description string, priority int, status string, percentComplete int, dueAt, startAt, completedAt *int64, listID int64, parentID *int64, now time.Time) error {
	icsData := GenerateVTodoICS("", VTodoFields{
		Summary: summary, Description: description,
		Priority: priority, Status: status, PercentComplete: percentComplete,
	}, now)
	etag := fmt.Sprintf("%x", md5.Sum([]byte(icsData)))

	// Get the current task to find the uid for ICS regeneration and the list for ctag bump.
	old, err := GetTask(db, id)
	if err != nil {
		return err
	}

	icsData = GenerateVTodoICS(old.UID, VTodoFields{
		UID: old.UID, Summary: summary, Description: description,
		Priority: priority, Status: status, PercentComplete: percentComplete,
	}, now)
	etag = fmt.Sprintf("%x", md5.Sum([]byte(icsData)))

	_, err = db.Exec(`UPDATE tasks SET summary = ?, description = ?, priority = ?, status = ?,
		percent_complete = ?, due_at = ?, start_at = ?, completed_at = ?, list_id = ?, parent_id = ?,
		etag = ?, ics_data = ?, updated_at = unixepoch() WHERE id = ?`,
		summary, description, priority, status, percentComplete, dueAt, startAt, completedAt,
		listID, parentID, etag, icsData, id)
	if err != nil {
		return err
	}
	if err := BumpCTag(db, listID); err != nil {
		return err
	}
	if old.ListID != listID {
		return BumpCTag(db, old.ListID)
	}
	return nil
}

func DeleteTask(db *sql.DB, id int64) error {
	task, err := GetTask(db, id)
	if err != nil {
		return err
	}

	// Record deletion for sync-token tracking.
	var syncToken int64
	if err := db.QueryRow(`SELECT sync_token FROM lists WHERE id = ?`, task.ListID).Scan(&syncToken); err != nil {
		return fmt.Errorf("get sync token: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO deleted_tasks (list_id, uid, deleted_at_token) VALUES (?, ?, ?)`,
		task.ListID, task.UID, syncToken+1); err != nil {
		return fmt.Errorf("record deletion: %w", err)
	}

	// CASCADE will delete subtasks.
	if _, err := db.Exec(`DELETE FROM tasks WHERE id = ?`, id); err != nil {
		return err
	}
	return BumpCTag(db, task.ListID)
}

// ToggleTask flips a task between COMPLETED and NEEDS-ACTION.
func ToggleTask(db *sql.DB, id int64, now time.Time) (*Task, error) {
	task, err := GetTask(db, id)
	if err != nil {
		return nil, err
	}

	var newStatus string
	var completedAt *int64
	var percentComplete int
	if task.Status == "COMPLETED" {
		newStatus = "NEEDS-ACTION"
		percentComplete = 0
	} else {
		newStatus = "COMPLETED"
		now := task.UpdatedAt // will be overwritten
		completedAt = &now
		percentComplete = 100
	}

	icsData := GenerateVTodoICS(task.UID, VTodoFields{
		UID: task.UID, Summary: task.Summary, Description: task.Description,
		Priority: task.Priority, Status: newStatus, PercentComplete: percentComplete,
	}, now)
	etag := fmt.Sprintf("%x", md5.Sum([]byte(icsData)))

	_, err = db.Exec(`UPDATE tasks SET status = ?, completed_at = ?, percent_complete = ?,
		etag = ?, ics_data = ?, updated_at = unixepoch() WHERE id = ?`,
		newStatus, completedAt, percentComplete, etag, icsData, id)
	if err != nil {
		return nil, err
	}
	if err := BumpCTag(db, task.ListID); err != nil {
		return nil, err
	}
	return GetTask(db, id)
}

// --- Reorder ---

// ReorderTasks sets sort_order for tasks with the given parent in a list.
func ReorderTasks(db *sql.DB, listID int64, parentID *int64, orderedIDs []int64) error {
	for i, id := range orderedIDs {
		if _, err := db.Exec(`UPDATE tasks SET sort_order = ?, updated_at = unixepoch() WHERE id = ? AND list_id = ?`, i, id, listID); err != nil {
			return err
		}
	}
	return BumpCTag(db, listID)
}

// ReparentTask moves a task to a new parent at a given sort position.
func ReparentTask(db *sql.DB, taskID int64, newParentID *int64, sortOrder int) error {
	task, err := GetTask(db, taskID)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tasks SET parent_id = ?, sort_order = ?, updated_at = unixepoch() WHERE id = ?`,
		newParentID, sortOrder, taskID)
	if err != nil {
		return err
	}
	return BumpCTag(db, task.ListID)
}

// --- Queries ---

// ChildMap returns tasks grouped by parent_id for recursive rendering.
func ChildMap(db *sql.DB, listID int64) (map[*int64][]Task, []Task, error) {
	tasks, err := ListTasks(db, listID)
	if err != nil {
		return nil, nil, err
	}
	m := make(map[int64][]Task) // parent_id -> children; 0 means root
	var roots []Task
	for _, t := range tasks {
		if t.ParentID == nil {
			roots = append(roots, t)
		} else {
			m[*t.ParentID] = append(m[*t.ParentID], t)
		}
	}
	// Convert to the expected format.
	result := make(map[*int64][]Task)
	result[nil] = roots
	for pid, children := range m {
		p := pid
		result[&p] = children
	}
	return result, tasks, nil
}

// ChildrenOf returns children for a given parent task from the flat task list.
func ChildrenOf(tasks []Task, parentID int64) []Task {
	var children []Task
	for _, t := range tasks {
		if t.ParentID != nil && *t.ParentID == parentID {
			children = append(children, t)
		}
	}
	return children
}

// RootTasks returns tasks with no parent from the flat task list.
func RootTasks(tasks []Task) []Task {
	var roots []Task
	for _, t := range tasks {
		if t.ParentID == nil {
			roots = append(roots, t)
		}
	}
	return roots
}

// UpcomingTasks returns incomplete tasks with the soonest due dates, joined with list info.
func UpcomingTasks(db *sql.DB, limit int) ([]TaskWithList, error) {
	rows, err := db.Query(`SELECT t.id, t.list_id, t.uid, t.etag, t.parent_id, t.summary, t.description,
		t.priority, t.percent_complete, t.status, t.completed_at, t.due_at, t.start_at,
		t.sort_order, t.ics_data, t.created_at, t.updated_at,
		l.name, l.color
		FROM tasks t JOIN lists l ON t.list_id = l.id
		WHERE t.status != 'COMPLETED' AND t.due_at IS NOT NULL
		ORDER BY t.due_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TaskWithList
	for rows.Next() {
		var tw TaskWithList
		if err := rows.Scan(&tw.ID, &tw.ListID, &tw.UID, &tw.ETag, &tw.ParentID, &tw.Summary, &tw.Description,
			&tw.Priority, &tw.PercentComplete, &tw.Status, &tw.CompletedAt, &tw.DueAt, &tw.StartAt,
			&tw.SortOrder, &tw.ICSData, &tw.CreatedAt, &tw.UpdatedAt,
			&tw.ListName, &tw.ListColor); err != nil {
			return nil, err
		}
		result = append(result, tw)
	}
	return result, rows.Err()
}

// --- Sync ---

func TasksChangedSince(db *sql.DB, listID, sinceToken int64) ([]Task, error) {
	return ListTasks(db, listID)
}

func DeletedTasksSince(db *sql.DB, listID, sinceToken int64) ([]string, error) {
	rows, err := db.Query(`SELECT uid FROM deleted_tasks WHERE list_id = ? AND deleted_at_token > ?`,
		listID, sinceToken)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}
