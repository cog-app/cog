package feed

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// parseAntennaPod lists the RSS feeds in an AntennaPod SQLite database (as
// raw bytes).
func parseAntennaPod(data []byte) ([]importEntry, error) {
	// Write to a temp file so database/sql can open it.
	tmp, err := os.CreateTemp("", "antennapod-*.db")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil {
			slog.Warn("feed import antennapod: remove temp", "path", tmp.Name(), "err", err)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return nil, fmt.Errorf("write temp: %w", errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp: %w", err)
	}

	apDB, err := sql.Open("sqlite3", tmp.Name()+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer apDB.Close()

	rows, err := apDB.Query(`SELECT download_url, title, image_url FROM Feeds WHERE download_url != ''`)
	if err != nil {
		return nil, fmt.Errorf("query feeds: %w", err)
	}
	defer rows.Close()

	var entries []importEntry
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.URL, &sub.Title, &sub.ImageURL); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		sub.Kind = "rss"
		entries = append(entries, importEntry{Subscription: sub})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return entries, nil
}
