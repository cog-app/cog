package tasks

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Seed creates sample task lists and tasks for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}

	userID, err := p.userID(username)
	if err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&count); err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}
	if count > 0 {
		return nil
	}

	lists, err := p.Lists(userID)
	if err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}
	if len(lists) == 0 {
		return fmt.Errorf("seed tasks: no default list")
	}
	personal := lists[0]
	work, err := p.CreateList(userID, "Work Tasks", "#e64a19")
	if err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}
	p.log.Info("seeded task lists", "lists", "Personal, Work")

	now := p.ctx.Now().UTC()
	tomorrow := now.AddDate(0, 0, 1)
	nextWeek := now.AddDate(0, 0, 7)
	yesterday := now.AddDate(0, 0, -1)

	type seedTask struct {
		list     *TaskList
		summary  string
		desc     string
		priority int
		due      *time.Time
		done     bool
		subtasks []string
	}
	tasks := []seedTask{
		{&personal, "Buy groceries", "Milk, bread, eggs, cheese", 0, &tomorrow, false, nil},
		{&personal, "Clean the house", "", 5, nil, false, nil},
		{&personal, "Read a book", "Finish chapter 7", 0, &nextWeek, false, nil},
		{&personal, "Exercise", "30 min jog", 1, &tomorrow, false, nil},
		{&personal, "Call dentist", "Schedule checkup", 0, &yesterday, true, nil},
		{work, "Project Alpha", "Main project deliverable", 1, &nextWeek, false, []string{"Design mockups", "Implement backend", "Write tests"}},
		{work, "Review PRs", "", 5, &tomorrow, false, nil},
		{work, "Update documentation", "API docs need refresh", 9, nil, false, nil},
		{work, "Team meeting prep", "Prepare slides for Friday", 0, &nextWeek, false, nil},
	}

	create := func(list *TaskList, nt NewTask) (*Task, error) {
		t, err := CreateTask(db, list, uuid.New().String(), nt, p.ctx.Now())
		if err != nil {
			return nil, fmt.Errorf("seed task %q: %w", nt.Summary, err)
		}
		return t, nil
	}
	for _, st := range tasks {
		t, err := create(st.list, NewTask{Summary: st.summary, Description: st.desc, Priority: st.priority, Due: st.due})
		if err != nil {
			return err
		}
		if st.done {
			if _, err := ToggleTask(db, st.list, t, p.ctx.Now()); err != nil {
				return fmt.Errorf("seed task %q: %w", st.summary, err)
			}
		}
		for _, summary := range st.subtasks {
			sub, err := create(st.list, NewTask{Summary: summary, ParentUID: t.UID})
			if err != nil {
				return err
			}
			if summary == "Implement backend" {
				if _, err := create(st.list, NewTask{Summary: "Set up database schema", ParentUID: sub.UID}); err != nil {
					return err
				}
			}
		}
	}
	p.log.Info("seeded tasks", "count", len(tasks))
	return nil
}
