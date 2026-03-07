//go:build seed

package tasks

import (
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

// Seed creates sample task lists and tasks for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed tasks: %w", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM lists`).Scan(&count)
	if count > 0 {
		return nil
	}

	personal, err := CreateTaskList(db, "personal", "Personal", "#0082c9")
	if err != nil {
		return err
	}
	work, err := CreateTaskList(db, "work", "Work", "#e64a19")
	if err != nil {
		return err
	}
	log.Printf("Seeded task lists: Personal, Work")

	now := time.Now().UTC()
	tomorrow := now.AddDate(0, 0, 1).Unix()
	nextWeek := now.AddDate(0, 0, 7).Unix()
	yesterday := now.AddDate(0, 0, -1).Unix()

	type seedTask struct {
		listID   int64
		parentID *int64
		summary  string
		desc     string
		priority int
		dueAt    *int64
		status   string
	}

	tasks := []seedTask{
		{personal.ID, nil, "Buy groceries", "Milk, bread, eggs, cheese", 0, &tomorrow, "NEEDS-ACTION"},
		{personal.ID, nil, "Clean the house", "", 5, nil, "NEEDS-ACTION"},
		{personal.ID, nil, "Read a book", "Finish chapter 7", 0, &nextWeek, "IN-PROCESS"},
		{personal.ID, nil, "Exercise", "30 min jog", 1, &tomorrow, "NEEDS-ACTION"},
		{personal.ID, nil, "Call dentist", "Schedule checkup", 0, &yesterday, "COMPLETED"},
		{work.ID, nil, "Project Alpha", "Main project deliverable", 1, &nextWeek, "IN-PROCESS"},
		{work.ID, nil, "Review PRs", "", 5, &tomorrow, "NEEDS-ACTION"},
		{work.ID, nil, "Update documentation", "API docs need refresh", 9, nil, "NEEDS-ACTION"},
		{work.ID, nil, "Team meeting prep", "Prepare slides for Friday", 0, &nextWeek, "NEEDS-ACTION"},
	}

	var projectAlphaID int64
	for _, st := range tasks {
		uid := uuid.New().String()
		t, err := CreateTask(db, st.listID, uid, st.parentID, st.summary, st.desc, st.priority, st.dueAt, nil)
		if err != nil {
			log.Printf("seed task %s: %v", st.summary, err)
			continue
		}
		if st.status == "COMPLETED" {
			ToggleTask(db, t.ID)
		}
		if st.summary == "Project Alpha" {
			projectAlphaID = t.ID
		}
		log.Printf("Seeded task: %s", st.summary)
	}

	// Create subtasks for Project Alpha.
	if projectAlphaID > 0 {
		subtasks := []string{"Design mockups", "Implement backend", "Write tests"}
		for _, summary := range subtasks {
			uid := uuid.New().String()
			st, err := CreateTask(db, work.ID, uid, &projectAlphaID, summary, "", 0, nil, nil)
			if err != nil {
				log.Printf("seed subtask %s: %v", summary, err)
				continue
			}
			log.Printf("Seeded subtask: %s", summary)
			// Add a nested subtask under "Implement backend".
			if summary == "Implement backend" {
				uid2 := uuid.New().String()
				_, err := CreateTask(db, work.ID, uid2, &st.ID, "Set up database schema", "", 0, nil, nil)
				if err != nil {
					log.Printf("seed nested subtask: %v", err)
				} else {
					log.Printf("Seeded nested subtask: Set up database schema")
				}
			}
		}
	}

	return nil
}
