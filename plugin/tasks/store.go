package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/icalsql"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// tables are where tasks and journal entries are stored: one resource table
// for both kinds, with the shared component layout (internal/dav/icalsql).
var tables = &icalsql.Tables{
	Objects: "tasks", Components: "task_components", Attendees: "task_attendees",
	Alarms: "task_alarms", Relations: "task_relations",
}

// TaskList is a DAV collection that holds tasks, as the tasks UI shows it.
// Collections belong to the DAV core (internal/dav); this is a view of one.
type TaskList struct {
	ID        int64  // dav.CollectionID
	UID       string // the collection's path name
	Name      string // display name
	Color     string
	SortOrder int
	Timezone  string // IANA zone for floating times and the web UI; "" = UTC
	OwnerID   string
}

func listFrom(c *dav.Collection) TaskList {
	return TaskList{
		ID: int64(c.ID), UID: c.Name, Name: c.Title(), Color: c.Color, SortOrder: c.SortOrder,
		Timezone: c.Timezone, OwnerID: c.OwnerID,
	}
}

// Zone returns the list's time zone, or UTC when unset or unknown.
func (l *TaskList) Zone() *time.Location {
	if loc := tz.Load(l.Timezone); loc != nil {
		return loc
	}
	return time.UTC
}

// Task is a stored task resource as the web UI shows it: the fields of its
// master component, and the whole object when it was loaded.
type Task struct {
	ID      int64
	ListID  int64 // dav.CollectionID
	UID     string
	URI     string
	Version int64

	Summary         string
	Description     string
	Location        string
	Priority        int // 0: undefined
	PercentComplete int
	Status          string // as stored; "" reads as NEEDS-ACTION
	DueAt           *int64
	DueIsDate       bool
	StartAt         *int64
	StartIsDate     bool
	CompletedAt     *int64
	RRule           string
	SortOrder       *int // X-APPLE-SORT-ORDER
	// ParentUID is the UID of the parent task (RELATED-TO;RELTYPE=PARENT);
	// ParentID is its row when it is in the same list.
	ParentUID string
	ParentID  *int64
	CreatedAt int64
	UpdatedAt int64

	Object *ical.Object // nil unless loaded
}

// Done reports whether the task is completed.
func (t *Task) Done() bool { return t.Status == "COMPLETED" }

// StatusOrDefault returns the status, NEEDS-ACTION when there is none.
func (t *Task) StatusOrDefault() string {
	if t.Status == "" {
		return "NEEDS-ACTION"
	}
	return t.Status
}

// TaskWithList extends Task with list metadata for display (e.g. widget).
type TaskWithList struct {
	Task
	ListName  string
	ListColor string
	ListZone  *time.Location
}

// --- Task queries ---

const taskColumns = `t.id, t.collection_id, t.uid, t.uri, t.version, t.created_at, t.updated_at,
	c.summary, c.description, c.location, coalesce(c.priority, 0), coalesce(c.percent_complete, 0), c.status,
	c.due_utc, c.due_is_date, c.dtstart_utc, c.dtstart_is_date, c.completed_utc, c.rrule, c.sort_order,
	coalesce(r.related_uid, '')`

const taskFrom = ` FROM tasks t JOIN task_components c ON c.object_id = t.id AND c.position = 0
	LEFT JOIN task_relations r ON r.component_id = c.id AND r.is_parent
	WHERE t.component = 'VTODO' AND `

// taskOrder puts tasks with a sort order first, in that order, then the
// rest by creation.
const taskOrder = ` ORDER BY c.sort_order IS NULL, c.sort_order, t.created_at, t.id`

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	var sortOrder sql.NullInt64
	if err := row.Scan(&t.ID, &t.ListID, &t.UID, &t.URI, &t.Version, &t.CreatedAt, &t.UpdatedAt,
		&t.Summary, &t.Description, &t.Location, &t.Priority, &t.PercentComplete, &t.Status,
		&t.DueAt, &t.DueIsDate, &t.StartAt, &t.StartIsDate, &t.CompletedAt, &t.RRule, &sortOrder, &t.ParentUID); err != nil {
		return nil, err
	}
	if sortOrder.Valid {
		n := int(sortOrder.Int64)
		t.SortOrder = &n
	}
	return &t, nil
}

func queryTasks(q davsql.Querier, where string, args ...any) ([]Task, error) {
	rows, err := q.Query(`SELECT `+taskColumns+taskFrom+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, *t)
	}
	return tasks, rows.Err()
}

// ListTasks returns a list's tasks in order, each with ParentID set when its
// parent is in the list. A task whose parents lead back to it is shown as a
// root, so a cycle sent by a client cannot hide tasks.
func ListTasks(db *sql.DB, listID int64) ([]Task, error) {
	tasks, err := queryTasks(db, `t.collection_id = ?`+taskOrder, listID)
	if err != nil {
		return nil, err
	}
	byUID := make(map[string]int, len(tasks))
	for i := range tasks {
		byUID[tasks[i].UID] = i
	}
	parent := make([]int, len(tasks))
	for i := range tasks {
		parent[i] = -1
		if j, ok := byUID[tasks[i].ParentUID]; ok && tasks[i].ParentUID != "" && j != i {
			parent[i] = j
		}
	}
	for i := range tasks {
		j := parent[i]
		for steps := 0; j >= 0 && j != i && steps < len(tasks); steps++ {
			j = parent[j]
		}
		if j == i {
			parent[i] = -1
		}
		if parent[i] >= 0 {
			id := tasks[parent[i]].ID
			tasks[i].ParentID = &id
		}
	}
	return tasks, nil
}

// GetTask returns a task by row ID, with its object and its parent's row
// when the parent is in the same list.
func GetTask(db *sql.DB, id int64) (*Task, error) {
	t, err := scanTask(db.QueryRow(`SELECT `+taskColumns+taskFrom+`t.id = ?`, id))
	if err != nil {
		return nil, err
	}
	objs, err := tables.Load(db, []int64{t.ID})
	if err != nil {
		return nil, err
	}
	t.Object = objs[t.ID]
	if t.ParentUID != "" {
		var pid int64
		switch err := db.QueryRow(`SELECT id FROM tasks WHERE collection_id = ? AND uid = ? AND id != ?`, t.ListID, t.ParentUID, t.ID).Scan(&pid); {
		case err == nil:
			t.ParentID = &pid
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, fmt.Errorf("find parent of task %d: %w", id, err)
		}
	}
	return t, nil
}

// GetTaskByUID returns a list's task with this UID, with its object.
func GetTaskByUID(db *sql.DB, listID int64, uid string) (*Task, error) {
	var id int64
	if err := db.QueryRow(`SELECT id FROM tasks WHERE collection_id = ? AND uid = ?`, listID, uid).Scan(&id); err != nil {
		return nil, err
	}
	return GetTask(db, id)
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

// RootTasks returns tasks with no parent in the list from the flat task list.
func RootTasks(tasks []Task) []Task {
	var roots []Task
	for _, t := range tasks {
		if t.ParentID == nil {
			roots = append(roots, t)
		}
	}
	return roots
}

// UpcomingTasks returns open tasks in lists with the soonest due dates.
func UpcomingTasks(db *sql.DB, lists map[int64]TaskList, limit int) ([]TaskWithList, error) {
	if len(lists) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(lists)+1)
	for id := range lists {
		args = append(args, id)
	}
	tasks, err := queryTasks(db, `c.status NOT IN ('COMPLETED', 'CANCELLED') AND c.due_utc IS NOT NULL
		AND t.collection_id IN (`+icalsql.Placeholders(len(lists))+`) ORDER BY c.due_utc LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	out := make([]TaskWithList, len(tasks))
	for i, t := range tasks {
		l := lists[t.ListID]
		out[i] = TaskWithList{Task: t, ListName: l.Name, ListColor: l.Color, ListZone: l.Zone()}
	}
	return out, nil
}

// --- Writes ---
//
// Every write goes through icalsql, which bumps the collection's version in
// the same transaction, so DAV clients see web edits.

// ParseObject reads one task or journal resource from iCalendar text.
func ParseObject(ics string, kind ical.Kind) (*ical.Object, error) {
	comps, err := contentline.Parse(strings.NewReader(ics))
	if err != nil {
		return nil, err
	}
	if len(comps) != 1 || !comps[0].Is("VCALENDAR") {
		return nil, fmt.Errorf("not one VCALENDAR")
	}
	o, err := ical.Decode(comps[0], kind)
	if err != nil {
		return nil, err
	}
	if o.UID == "" {
		return nil, fmt.Errorf("%s missing UID", kind)
	}
	return o, nil
}

// RenderTask returns the iCalendar text Cog serves for a loaded task.
func RenderTask(t *Task) (string, error) {
	if t.Object == nil {
		return "", fmt.Errorf("task %d: object not loaded", t.ID)
	}
	data, err := icalsql.Render(t.Object)
	return string(data), err
}

// NewTask is what the web UI sets on a task it creates.
type NewTask struct {
	Summary     string
	Description string
	Priority    int
	Due, Start  *time.Time // in the list's zone
	DueDate     bool       // Due is a whole day: its date, at midnight UTC
	ParentUID   string
}

// CreateTask stores a new task with uid in a list.
func CreateTask(db *sql.DB, list *TaskList, uid string, nt NewTask, now time.Time) (*Task, error) {
	stamp := ical.DateTimeOf(now.UTC(), false)
	seq := 0
	e := &ical.Component{
		Kind: ical.VTODO, Summary: nt.Summary, Description: nt.Description, Status: "NEEDS-ACTION",
		Sequence: &seq, DTStamp: stamp, Created: stamp, LastModified: stamp,
	}
	if nt.Priority > 0 {
		p := nt.Priority
		e.Priority = &p
	}
	if nt.Due != nil && nt.DueDate {
		e.Due = ical.DateTimeOf(nt.Due.UTC(), true)
	} else if nt.Due != nil {
		e.Due = ical.DateTimeOf(nt.Due.In(list.Zone()), false)
	}
	if nt.Start != nil {
		e.Start = ical.DateTimeOf(nt.Start.In(list.Zone()), false)
	}
	e.SetParent(nt.ParentUID)
	o := &ical.Object{
		UID:        uid,
		Props:      []contentline.Property{{Name: "PRODID", Value: ical.ProdID}},
		Components: []*ical.Component{e},
	}
	return SaveTask(db, list, nil, o, now)
}

// SaveTask stores an object written by Cog in a list. from is the task
// being edited, or nil for a new one; moving it to another list deletes it
// from the old one, leaving a tombstone there.
func SaveTask(db *sql.DB, list *TaskList, from *Task, o *ical.Object, now time.Time) (*Task, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("save task: %w", err)
	}
	defer tx.Rollback()
	uri := o.UID + ".ics"
	cond := davsql.Conditions{IfNoneMatchAny: true}
	if from != nil {
		uri, cond = from.URI, davsql.Conditions{}
		if from.ListID != list.ID {
			if err := tables.Delete(tx, from.ListID, from.URI, davsql.Conditions{}, now.Unix()); err != nil {
				return nil, err
			}
		}
	}
	st, err := tables.Store(tx, list.ID, list.Zone(), uri, o, cond, now.Unix())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("save task: %w", err)
	}
	return GetTask(db, st.ID)
}

// DeleteTask removes a task and its subtasks in the same list, leaving a
// tombstone for each.
func DeleteTask(db *sql.DB, id int64, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.Query(`WITH RECURSIVE sub(id, uid, collection_id) AS (
			SELECT id, uid, collection_id FROM tasks WHERE id = ?
			UNION
			SELECT t.id, t.uid, t.collection_id FROM sub
			JOIN task_relations r ON r.related_uid = sub.uid AND r.is_parent
			JOIN task_components c ON c.id = r.component_id AND c.position = 0
			JOIN tasks t ON t.id = c.object_id AND t.collection_id = sub.collection_id
		) SELECT collection_id, uri FROM tasks WHERE id IN (SELECT id FROM sub)`, id)
	if err != nil {
		return fmt.Errorf("find subtasks: %w", err)
	}
	type res struct {
		list int64
		uri  string
	}
	var doomed []res
	for rows.Next() {
		var r res
		if err := rows.Scan(&r.list, &r.uri); err != nil {
			rows.Close()
			return fmt.Errorf("scan subtask: %w", err)
		}
		doomed = append(doomed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("find subtasks: %w", err)
	}
	if len(doomed) == 0 {
		return sql.ErrNoRows
	}
	for _, r := range doomed {
		if err := tables.Delete(tx, r.list, r.uri, davsql.Conditions{}, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ToggleTask completes an open task or reopens a completed one. Completing
// one occurrence of a recurring task moves the series on to the next
// (Complete).
func ToggleTask(db *sql.DB, list *TaskList, t *Task, now time.Time) (*Task, error) {
	o := t.Object
	z := ical.ZonesFor(o, list.Zone())
	e := o.Components[0]
	if e.Status == "COMPLETED" {
		setStatus(e, "NEEDS-ACTION", now)
	} else if err := Complete(o, z, now); err != nil {
		return nil, err
	}
	o.Components[0].Stamp(now, true)
	return SaveTask(db, list, t, o, now)
}

// Complete marks an object's task done. A recurring task is moved on to its
// next occurrence instead, as tasks.org does (ical.Object.Advance), and
// stays open; only its last occurrence completes the task.
func Complete(o *ical.Object, z ical.Zones, now time.Time) error {
	m := o.Components[0]
	if !m.IsOverride() && m.Recurs() {
		ok, err := o.Advance(z)
		if err != nil {
			return fmt.Errorf("complete occurrence: %w", err)
		}
		if ok {
			m = o.Components[0]
			if m.Status != "" {
				m.Status = "NEEDS-ACTION"
			}
			m.Completed = ical.DateTime{}
			if m.PercentComplete != nil {
				zero := 0
				m.PercentComplete = &zero
			}
			m.Changed("STATUS", "TIME")
			return nil
		}
	}
	setStatus(m, "COMPLETED", now)
	return nil
}

// setStatus sets a task's STATUS with what goes with it: COMPLETED and 100%
// when it is done; no COMPLETED, and not 100%, when it is not.
func setStatus(e *ical.Component, status string, now time.Time) {
	e.Status = status
	if status == "COMPLETED" {
		e.Completed = ical.DateTimeOf(now.UTC(), false)
		full := 100
		e.PercentComplete = &full
	} else {
		e.Completed = ical.DateTime{}
		if e.PercentComplete != nil && *e.PercentComplete == 100 {
			zero := 0
			e.PercentComplete = &zero
		}
	}
	e.Changed("STATUS")
}

// ReorderTasks puts tasks in the given order under one parent (nil for the
// top level), writing X-APPLE-SORT-ORDER and RELATED-TO so DAV clients see
// the same order and tree. Tasks whose order and parent are unchanged are
// not written.
func ReorderTasks(db *sql.DB, list *TaskList, parentID *int64, orderedIDs []int64, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("reorder tasks: %w", err)
	}
	defer tx.Rollback()
	parentUID := ""
	if parentID != nil {
		if err := tx.QueryRow(`SELECT uid FROM tasks WHERE id = ? AND collection_id = ?`, *parentID, list.ID).Scan(&parentUID); err != nil {
			return fmt.Errorf("reorder tasks: parent %d: %w", *parentID, err)
		}
	}
	objs, err := tables.Load(tx, orderedIDs)
	if err != nil {
		return err
	}
	uris := map[int64]string{}
	rows, err := tx.Query(`SELECT id, uri FROM tasks WHERE collection_id = ? AND id IN (`+icalsql.Placeholders(max(len(orderedIDs), 1))+`)`,
		append([]any{list.ID}, icalsql.Anys(orderedIDs)...)...)
	if err != nil {
		return fmt.Errorf("reorder tasks: %w", err)
	}
	for rows.Next() {
		var id int64
		var uri string
		if err := rows.Scan(&id, &uri); err != nil {
			rows.Close()
			return fmt.Errorf("reorder tasks: %w", err)
		}
		uris[id] = uri
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reorder tasks: %w", err)
	}
	for i, id := range orderedIDs {
		o, uri := objs[id], uris[id]
		if o == nil || uri == "" || o.UID == parentUID {
			return fmt.Errorf("reorder tasks: task %d is not in list %d", id, list.ID)
		}
		e := o.Components[0]
		changed := e.SetParent(parentUID)
		if e.SortOrder == nil || *e.SortOrder != i {
			n := i
			e.SortOrder = &n
			changed = true
		}
		if !changed {
			continue
		}
		e.Stamp(now, false)
		if _, err := tables.Store(tx, list.ID, list.Zone(), uri, o, davsql.Conditions{}, now.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
