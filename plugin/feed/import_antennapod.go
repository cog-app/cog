package feed

import (
	"database/sql"
	"fmt"
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
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return 0, fmt.Errorf("import antennapod: write temp: %w", err)
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
			continue
		}
		if _, err := s.CreateSubscription(Subscription{
			Kind:            "rss",
			URL:             feedURL,
			Title:           title,
			ImageURL:        imageURL,
			PollIntervalSec: 1800,
			NextCheckAt:     1,
		}); err == nil {
			count++
		}
		// Duplicate URLs are rejected by the UNIQUE constraint; skip silently.
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("import antennapod: rows: %w", err)
	}

	return count, nil
}
