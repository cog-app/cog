package main

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/codec"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	_ "github.com/mattn/go-sqlite3"
)

// --- tasks ---

// oldList and oldTask are rows of the tasks plugin's schema before the DAV
// storage rewrite (lists and tasks with ics_data). Its web UI was the source
// of truth: ics_data lacked the parent and the order, and reordering never
// rewrote it, so the VTODOs are built from the columns.
type oldList struct {
	id         int64
	uid, name  string
	color      string
	tasks      []oldTask
	parentUIDs map[int64]string
}

type oldTask struct {
	id, listID                   int64
	uid                          string
	parentID                     sql.NullInt64
	summary, description, status string
	priority, percent, sortOrder int
	completedAt, dueAt, startAt  sql.NullInt64
	createdAt, updatedAt         int64
}

func runTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	dbPath := fs.String("db", "", "the old server's tasks database (<data>/<user>/app_data/tasks/plugin.db)")
	imp := fs.String("import", "tmp/dav-import", "output directory, one file per list under tasks/")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbPath == "" {
		return fmt.Errorf("-db is required")
	}
	if _, err := os.Stat(*dbPath); err != nil {
		return err
	}
	// Read-only, and waiting rather than failing if the server still has
	// the database open.
	db, err := sql.Open("sqlite3", "file:"+*dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return err
	}
	defer db.Close()

	lists, err := readOldTasks(db)
	if err != nil {
		return err
	}
	dir := filepath.Join(*imp, "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, l := range lists {
		var b bytes.Buffer
		if err := contentline.Write(&b, []*contentline.Component{l.calendar()}); err != nil {
			return fmt.Errorf("list %q: %w", l.name, err)
		}
		file := filepath.Join(dir, l.uid+".ics")
		if err := os.WriteFile(file, b.Bytes(), 0o600); err != nil {
			return err
		}
		fmt.Printf("%s: %d tasks (list %q)\n", file, len(l.tasks), l.name)
	}
	return nil
}

func readOldTasks(db *sql.DB) ([]*oldList, error) {
	rows, err := db.Query(`SELECT id, uid, name, color FROM lists ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("read lists: %w", err)
	}
	var lists []*oldList
	byID := map[int64]*oldList{}
	for rows.Next() {
		l := &oldList{parentUIDs: map[int64]string{}}
		if err := rows.Scan(&l.id, &l.uid, &l.name, &l.color); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read lists: %w", err)
		}
		lists = append(lists, l)
		byID[l.id] = l
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("read lists: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read lists: %w", err)
	}

	rows, err = db.Query(`SELECT id, list_id, uid, parent_id, summary, description, priority,
		percent_complete, status, completed_at, due_at, start_at, sort_order, created_at, updated_at
		FROM tasks ORDER BY list_id, sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	defer rows.Close()
	uids := map[int64]string{}
	var all []oldTask
	for rows.Next() {
		var t oldTask
		if err := rows.Scan(&t.id, &t.listID, &t.uid, &t.parentID, &t.summary, &t.description, &t.priority,
			&t.percent, &t.status, &t.completedAt, &t.dueAt, &t.startAt, &t.sortOrder, &t.createdAt, &t.updatedAt); err != nil {
			return nil, fmt.Errorf("read tasks: %w", err)
		}
		uids[t.id] = t.uid
		all = append(all, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	for _, t := range all {
		l := byID[t.listID]
		if l == nil {
			return nil, fmt.Errorf("task %s is in list %d, which does not exist", t.uid, t.listID)
		}
		if t.parentID.Valid {
			p, ok := uids[t.parentID.Int64]
			if !ok {
				return nil, fmt.Errorf("task %s has parent %d, which does not exist", t.uid, t.parentID.Int64)
			}
			l.parentUIDs[t.id] = p
		}
		l.tasks = append(l.tasks, t)
	}
	return lists, nil
}

func utc(sec int64) string { return time.Unix(sec, 0).UTC().Format("20060102T150405Z") }

// calendar is the list as one VCALENDAR of VTODOs.
func (l *oldList) calendar() *contentline.Component {
	cal := &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{
		{Name: "VERSION", Value: "2.0"},
		{Name: "PRODID", Value: "-//Cog//davfixtures tasks//EN"},
		{Name: "X-WR-CALNAME", Value: codec.EscapeText(l.name)},
		{Name: "X-APPLE-CALENDAR-COLOR", Value: l.color},
	}}
	for _, t := range l.tasks {
		p := []contentline.Property{
			{Name: "UID", Value: t.uid},
			{Name: "DTSTAMP", Value: utc(t.updatedAt)},
			{Name: "CREATED", Value: utc(t.createdAt)},
			{Name: "LAST-MODIFIED", Value: utc(t.updatedAt)},
			{Name: "SUMMARY", Value: codec.EscapeText(t.summary)},
		}
		if t.description != "" {
			p = append(p, contentline.Property{Name: "DESCRIPTION", Value: codec.EscapeText(t.description)})
		}
		if t.status != "" {
			p = append(p, contentline.Property{Name: "STATUS", Value: t.status})
		}
		if t.priority > 0 {
			p = append(p, contentline.Property{Name: "PRIORITY", Value: strconv.Itoa(t.priority)})
		}
		if t.percent > 0 {
			p = append(p, contentline.Property{Name: "PERCENT-COMPLETE", Value: strconv.Itoa(t.percent)})
		}
		for _, d := range []struct {
			name string
			v    sql.NullInt64
		}{{"DTSTART", t.startAt}, {"DUE", t.dueAt}, {"COMPLETED", t.completedAt}} {
			if d.v.Valid {
				p = append(p, contentline.Property{Name: d.name, Value: utc(d.v.Int64)})
			}
		}
		if parent, ok := l.parentUIDs[t.id]; ok {
			p = append(p, contentline.Property{Name: "RELATED-TO", Params: []contentline.Param{{Name: "RELTYPE", Values: []string{"PARENT"}}}, Value: parent})
		}
		p = append(p, contentline.Property{Name: "X-APPLE-SORT-ORDER", Value: strconv.Itoa(t.sortOrder)})
		cal.Components = append(cal.Components, &contentline.Component{Name: "VTODO", Props: p})
	}
	return cal
}
