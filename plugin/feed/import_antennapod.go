package feed

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

// ImportAntennaPod reads an AntennaPod SQLite database (as raw bytes) and
// creates RSS subscriptions. Returns the number of subscriptions created.
// Duplicates (same URL) are silently skipped.
func ImportAntennaPod(s *Store, data []byte) (int, error) {
	// Write to a temp file so database/sql can open it.
	tmp, err := os.CreateTemp("", "antennapod-*.db")
	if err != nil {
		return 0, fmt.Errorf("import antennapod: create temp: %w", err)
	}
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil {
			slog.Warn("feed import antennapod: remove temp", "path", tmp.Name(), "err", err)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return 0, fmt.Errorf("import antennapod: write temp: %w", errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("import antennapod: close temp: %w", err)
	}

	apDB, err := sql.Open("sqlite3", tmp.Name()+"?mode=ro")
	if err != nil {
		return 0, fmt.Errorf("import antennapod: open db: %w", err)
	}
	defer apDB.Close()

	rows, err := apDB.Query(`SELECT download_url, title, image_url FROM Feeds WHERE download_url != ''`)
	if err != nil {
		return 0, fmt.Errorf("import antennapod: query feeds: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var feedURL, title, imageURL string
		if err := rows.Scan(&feedURL, &title, &imageURL); err != nil {
			return count, fmt.Errorf("import antennapod: scan: %w", err)
		}
		created, err := s.importSubscription(Subscription{
			Kind:            "rss",
			URL:             feedURL,
			Title:           title,
			ImageURL:        imageURL,
			PollIntervalSec: 1800,
			NextCheckAt:     1,
		})
		if err != nil {
			return count, fmt.Errorf("import antennapod: %w", err)
		}
		if created {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("import antennapod: rows: %w", err)
	}

	return count, nil
}
