// Package activity provides a centralized audit/activity log for user resource mutations.
package activity

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// Activity represents a single audit log entry.
type Activity struct {
	ID              int64
	UserID          string // affected user (resource owner)
	ActorID         string // who performed the action (username)
	App             string // "files", "sharing", "calendar", etc.
	Type            string // "file_created", "share_created", etc.
	Subject         string // human-readable description
	SubjectTemplate string // rich template: "{user} uploaded {file}"
	SubjectParams   string // JSON-encoded rich object params
	ObjectType      string // "files", "calendar", etc.
	ObjectID        string // resource ID
	ObjectName      string // display name (path, title)
	// Notify marks activity addressed to the user, such as something shared
	// with them, as opposed to a record of what happened to their things.
	Notify    bool
	CreatedAt int64
}

// Store provides CRUD on the activities table.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// NewStore creates a new activity store.
func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

// Log inserts an activity with a plain subject.
func (s *Store) Log(userID, actorID, app, typ, subject, objectType, objectID, objectName string) error {
	_, err := s.db.Exec(`INSERT INTO activities
		(user_id, actor_id, app, type, subject, object_type, object_id, object_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, actorID, app, typ, subject, objectType, objectID, objectName, s.clock().Unix())
	return err
}

// LogRich inserts an activity with a rich subject template and params.
func (s *Store) LogRich(userID, actorID, app, typ, subject, template string, params map[string]any, objectType, objectID, objectName string) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal activity params: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO activities
		(user_id, actor_id, app, type, subject, subject_template, subject_params, object_type, object_id, object_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, actorID, app, typ, subject, template, string(paramsJSON), objectType, objectID, objectName, s.clock().Unix())
	return err
}

// Notification records activity addressed to the user, such as a share they
// received.
func (s *Store) Notification(userID, actorID, app, typ, subject, objectType, objectID, objectName string) error {
	_, err := s.db.Exec(`INSERT INTO activities
		(user_id, actor_id, app, type, subject, object_type, object_id, object_name, notify, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, ?)`,
		userID, actorID, app, typ, subject, objectType, objectID, objectName, s.clock().Unix())
	if err != nil {
		return fmt.Errorf("log notification: %w", err)
	}
	return nil
}

// Filter narrows what Recent returns. The zero Filter is everything.
type Filter struct {
	// Notifications keeps only activity addressed to the user.
	Notifications bool
	// App keeps only one app's activity.
	App string
}

// Recent returns a user's latest activities matching f, newest first.
func (s *Store) Recent(userID string, f Filter, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + columns + ` FROM activities WHERE user_id = ?`
	args := []any{userID}
	if f.Notifications {
		q += ` AND notify = 1`
	}
	if f.App != "" {
		q += ` AND app = ?`
		args = append(args, f.App)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("recent activity: %w", err)
	}
	defer rows.Close()
	return scanActivities(rows)
}

// Apps returns the apps a user has activity from, in name order.
func (s *Store) Apps(userID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT app FROM activities WHERE user_id = ? ORDER BY app`, userID)
	if err != nil {
		return nil, fmt.Errorf("activity apps: %w", err)
	}
	defer rows.Close()
	var apps []string
	for rows.Next() {
		var app string
		if err := rows.Scan(&app); err != nil {
			return nil, fmt.Errorf("activity apps: %w", err)
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

const columns = `id, user_id, actor_id, app, type, subject, subject_template, subject_params,
		object_type, object_id, object_name, notify, created_at`

// List returns activities for a user, newest first.
func (s *Store) List(userID string, limit, offset int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+columns+`
		FROM activities WHERE user_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActivities(rows)
}

// ListByObject returns activities for a specific resource.
func (s *Store) ListByObject(objectType, objectID string, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+columns+`
		FROM activities WHERE object_type = ? AND object_id = ? ORDER BY id DESC LIMIT ?`,
		objectType, objectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActivities(rows)
}

// ListSince returns activities for a user since a given activity ID.
func (s *Store) ListSince(userID string, sinceID int64, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT `+columns+`
		FROM activities WHERE user_id = ? AND id > ? ORDER BY id DESC LIMIT ?`,
		userID, sinceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanActivities(rows)
}

func scanActivities(rows *sql.Rows) ([]Activity, error) {
	var activities []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.UserID, &a.ActorID, &a.App, &a.Type, &a.Subject,
			&a.SubjectTemplate, &a.SubjectParams, &a.ObjectType, &a.ObjectID, &a.ObjectName, &a.Notify, &a.CreatedAt); err != nil {
			return nil, err
		}
		activities = append(activities, a)
	}
	return activities, rows.Err()
}
