package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// TaskList is a DAV collection that holds tasks, as the tasks UI shows it.
// Collections belong to the DAV core (internal/dav); this is a view of one.
type TaskList struct {
	ID        int64  // dav.CollectionID
	UID       string // the collection's path name
	Name      string // display name
	Color     string
	SortOrder int
	OwnerID   string
}

func listFrom(c *dav.Collection) TaskList {
	return TaskList{ID: int64(c.ID), UID: c.Name, Name: c.Title(), Color: c.Color, SortOrder: c.SortOrder, OwnerID: c.OwnerID}
}

// Task is a single task item with hierarchy and VTODO sync support.
type Task struct {
	ID              int64
	ListID          int64 // dav.CollectionID
	UID             string
	URI             string
	Version         int64
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

// --- Task queries ---

const taskColumns = `id, collection_id, uid, uri, version, parent_id, summary, description, priority, percent_complete, status, completed_at, due_at, start_at, sort_order, ics_data, created_at, updated_at`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ListID, &t.UID, &t.URI, &t.Version, &t.ParentID, &t.Summary, &t.Description,
		&t.Priority, &t.PercentComplete, &t.Status, &t.CompletedAt, &t.DueAt, &t.StartAt,
		&t.SortOrder, &t.ICSData, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func scanTasks(rows *sql.Rows) ([]Task, error) {
	defer rows.Close()
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
	rows, err := db.Query(`SELECT `+taskColumns+` FROM tasks WHERE collection_id = ? ORDER BY sort_order, created_at`, listID)
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

func GetTask(db *sql.DB, id int64) (*Task, error) {
	return scanTask(db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
}

func GetTaskByUID(db *sql.DB, listID int64, uid string) (*Task, error) {
	return scanTask(db.QueryRow(`SELECT `+taskColumns+` FROM tasks WHERE collection_id = ? AND uid = ?`, listID, uid))
}

// --- Writes ---
//
// Every write that changes a task's ICS bumps the collection's version in the
// same transaction (davsql), so DAV clients see web edits.

// taskRow is what a write stores for one task.
type taskRow struct {
	listID          int64
	uid, uri        string
	parentID        *int64
	summary         string
	description     string
	priority        int
	percentComplete int
	status          string
	completedAt     *int64
	dueAt, startAt  *int64
	ics             string
}

// errUIDConflict is returned when another resource in the list has the UID.
type errUIDConflict struct{ uri string }

func (e *errUIDConflict) Error() string { return "UID already used by " + e.uri }

// putTask stores a task at its URI inside tx and returns its ID, version and
// whether it was created. A new task goes to the end of its siblings.
func putTask(tx *sql.Tx, t taskRow, cond davsql.Conditions, now int64) (id, version int64, created bool, err error) {
	var cur *int64
	var curVersion int64
	switch err := tx.QueryRow(`SELECT version FROM tasks WHERE collection_id = ? AND uri = ?`, t.listID, t.uri).Scan(&curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, false, fmt.Errorf("read task %s: %w", t.uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return 0, 0, false, dav.ErrPrecondition
	}
	if other, err := otherURI(tx, t.listID, t.uid, t.uri); err != nil {
		return 0, 0, false, err
	} else if other != "" {
		return 0, 0, false, &errUIDConflict{other}
	}
	version, err = davsql.Bump(tx, t.listID)
	if err != nil {
		return 0, 0, false, err
	}
	var maxSort int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM tasks WHERE collection_id = ? AND parent_id IS ?`,
		t.listID, t.parentID).Scan(&maxSort); err != nil {
		return 0, 0, false, fmt.Errorf("next sort order: %w", err)
	}
	err = tx.QueryRow(`INSERT INTO tasks (collection_id, uid, uri, version, parent_id, summary, description, priority,
			percent_complete, status, completed_at, due_at, start_at, sort_order, ics_data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			uid = excluded.uid, version = excluded.version, parent_id = excluded.parent_id,
			summary = excluded.summary, description = excluded.description, priority = excluded.priority,
			percent_complete = excluded.percent_complete, status = excluded.status,
			completed_at = excluded.completed_at, due_at = excluded.due_at, start_at = excluded.start_at,
			ics_data = excluded.ics_data, updated_at = excluded.updated_at
		RETURNING id`,
		t.listID, t.uid, t.uri, version, t.parentID, t.summary, t.description, t.priority,
		t.percentComplete, t.status, t.completedAt, t.dueAt, t.startAt, maxSort+1, t.ics, now, now).Scan(&id)
	if err != nil {
		return 0, 0, false, fmt.Errorf("store task %s: %w", t.uri, err)
	}
	if err := davsql.Revive(tx, t.listID, t.uri); err != nil {
		return 0, 0, false, err
	}
	return id, version, cur == nil, nil
}

// otherURI returns the URI of another task or journal in the list holding
// uid, or "".
func otherURI(tx *sql.Tx, listID int64, uid, uri string) (string, error) {
	var other string
	err := tx.QueryRow(`SELECT uri FROM tasks WHERE collection_id = ? AND uid = ? AND uri != ?
		UNION ALL SELECT uri FROM journals WHERE collection_id = ? AND uid = ? AND uri != ? LIMIT 1`,
		listID, uid, uri, listID, uid, uri).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check UID %s: %w", uid, err)
	}
	return other, nil
}

// subtree returns the task and its descendants as (id, uri) pairs.
func subtree(tx *sql.Tx, id int64) (ids []int64, uris []string, err error) {
	rows, err := tx.Query(`WITH RECURSIVE sub(id) AS (
			SELECT ? UNION SELECT t.id FROM tasks t JOIN sub ON t.parent_id = sub.id
		) SELECT t.id, t.uri FROM tasks t JOIN sub ON t.id = sub.id`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("find subtasks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var i int64
		var u string
		if err := rows.Scan(&i, &u); err != nil {
			return nil, nil, fmt.Errorf("scan subtask: %w", err)
		}
		ids, uris = append(ids, i), append(uris, u)
	}
	return ids, uris, rows.Err()
}

func newTaskICS(t taskRow, now time.Time) string {
	f := VTodoFields{
		UID: t.uid, Summary: t.summary, Description: t.description, Priority: t.priority,
		PercentComplete: t.percentComplete, Status: t.status,
	}
	if t.dueAt != nil {
		f.Due = time.Unix(*t.dueAt, 0)
	}
	if t.startAt != nil {
		f.Start = time.Unix(*t.startAt, 0)
	}
	if t.completedAt != nil {
		f.Completed = time.Unix(*t.completedAt, 0)
	}
	return GenerateVTodoICS(t.uid, f, now)
}

func CreateTask(db *sql.DB, listID int64, uid string, parentID *int64, summary, description string, priority int, dueAt, startAt *int64, now time.Time) (*Task, error) {
	row := taskRow{
		listID: listID, uid: uid, uri: uid + ".ics", parentID: parentID, summary: summary,
		description: description, priority: priority, status: "NEEDS-ACTION", dueAt: dueAt, startAt: startAt,
	}
	row.ics = newTaskICS(row, now)
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	defer tx.Rollback()
	id, _, _, err := putTask(tx, row, davsql.Conditions{IfNoneMatchAny: true}, now.Unix())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("create task: %w", err)
	}
	return GetTask(db, id)
}

// UpdateTask saves a web edit. The ICS is regenerated from the form (lossy
// until milestone 4 models tasks); moving to another list deletes the task
// from the old one, leaving a tombstone there.
func UpdateTask(db *sql.DB, id int64, summary, description string, priority int, status string, percentComplete int, dueAt, startAt, completedAt *int64, listID int64, parentID *int64, now time.Time) error {
	old, err := GetTask(db, id)
	if err != nil {
		return err
	}
	row := taskRow{
		listID: listID, uid: old.UID, uri: old.URI, parentID: parentID, summary: summary, description: description,
		priority: priority, percentComplete: percentComplete, status: status, completedAt: completedAt,
		dueAt: dueAt, startAt: startAt,
	}
	row.ics = newTaskICS(row, now)
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	defer tx.Rollback()
	if old.ListID != listID {
		if _, err := tx.Exec(`DELETE FROM tasks WHERE id = ?`, id); err != nil {
			return fmt.Errorf("move task: %w", err)
		}
		if _, err := davsql.Delete(tx, old.ListID, old.URI, now.Unix()); err != nil {
			return err
		}
	}
	if _, _, _, err := putTask(tx, row, davsql.Conditions{}, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteTask removes a task and its subtasks, leaving a tombstone for each.
func DeleteTask(db *sql.DB, id int64, now time.Time) error {
	task, err := GetTask(db, id)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	defer tx.Rollback()
	ids, uris, err := subtree(tx, id)
	if err != nil {
		return err
	}
	version, err := davsql.Bump(tx, task.ListID)
	if err != nil {
		return err
	}
	for i := range ids {
		if _, err := tx.Exec(`DELETE FROM tasks WHERE id = ?`, ids[i]); err != nil {
			return fmt.Errorf("delete task %d: %w", ids[i], err)
		}
		if err := davsql.Tombstone(tx, task.ListID, uris[i], version, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleTask flips a task between COMPLETED and NEEDS-ACTION.
func ToggleTask(db *sql.DB, id int64, now time.Time) (*Task, error) {
	task, err := GetTask(db, id)
	if err != nil {
		return nil, err
	}
	status, percent := "COMPLETED", 100
	completed := now.Unix()
	completedAt := &completed
	if task.Status == "COMPLETED" {
		status, percent, completedAt = "NEEDS-ACTION", 0, nil
	}
	if err := UpdateTask(db, id, task.Summary, task.Description, task.Priority, status, percent,
		task.DueAt, task.StartAt, completedAt, task.ListID, task.ParentID, now); err != nil {
		return nil, err
	}
	return GetTask(db, id)
}

// --- Reorder ---
//
// Order and nesting are not in the generated ICS yet (milestone 4 adds
// X-APPLE-SORT-ORDER and RELATED-TO), so they do not change a task's version.

// ReorderTasks sets sort_order for tasks with the given parent in a list.
func ReorderTasks(db *sql.DB, listID int64, parentID *int64, orderedIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("reorder tasks: %w", err)
	}
	defer tx.Rollback()
	for i, id := range orderedIDs {
		if _, err := tx.Exec(`UPDATE tasks SET sort_order = ? WHERE id = ? AND collection_id = ?`, i, id, listID); err != nil {
			return fmt.Errorf("reorder task %d: %w", id, err)
		}
	}
	return tx.Commit()
}

// ReparentTask moves a task to a new parent at a given sort position.
func ReparentTask(db *sql.DB, taskID int64, newParentID *int64, sortOrder int) error {
	_, err := db.Exec(`UPDATE tasks SET parent_id = ?, sort_order = ? WHERE id = ?`, newParentID, sortOrder, taskID)
	return err
}

// --- Queries ---

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

// UpcomingTasks returns incomplete tasks in lists with the soonest due dates.
func UpcomingTasks(db *sql.DB, lists map[int64]TaskList, limit int) ([]TaskWithList, error) {
	if len(lists) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(lists)+1)
	for id := range lists {
		args = append(args, id)
	}
	rows, err := db.Query(`SELECT `+taskColumns+` FROM tasks
		WHERE status != 'COMPLETED' AND due_at IS NOT NULL AND collection_id IN (?`+strings.Repeat(",?", len(lists)-1)+`)
		ORDER BY due_at LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	tasks, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	out := make([]TaskWithList, len(tasks))
	for i, t := range tasks {
		l := lists[t.ListID]
		out[i] = TaskWithList{Task: t, ListName: l.Name, ListColor: l.Color}
	}
	return out, nil
}
