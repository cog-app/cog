package dashboard

import (
	"database/sql"
	"encoding/json"
)

// Layout stores per-user dashboard customization preferences.
type Layout struct {
	Columns     int      `json:"columns"`
	WidgetOrder []string `json:"widget_order"`
}

func migrateLayout(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS layout (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			columns INTEGER NOT NULL DEFAULT 2,
			widget_order TEXT NOT NULL DEFAULT '[]'
		)
	`)
	return err
}

// GetLayout returns the user's saved layout, or a default if none exists.
func GetLayout(db *sql.DB) (*Layout, error) {
	var l Layout
	var orderJSON string
	err := db.QueryRow(`SELECT columns, widget_order FROM layout WHERE id = 1`).Scan(&l.Columns, &orderJSON)
	if err == sql.ErrNoRows {
		return &Layout{Columns: 2, WidgetOrder: nil}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(orderJSON), &l.WidgetOrder); err != nil {
		l.WidgetOrder = nil
	}
	return &l, nil
}

// SaveLayout persists the user's layout preferences.
func SaveLayout(db *sql.DB, l *Layout) error {
	orderJSON, err := json.Marshal(l.WidgetOrder)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO layout (id, columns, widget_order) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET columns = excluded.columns, widget_order = excluded.widget_order`,
		l.Columns, string(orderJSON))
	return err
}
