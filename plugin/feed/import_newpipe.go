package feed

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ImportNewPipe reads a NewPipe ZIP export (as raw bytes) and creates YouTube
// subscriptions. Returns the number of subscriptions created.
// Duplicates (same URL) are silently skipped.
func ImportNewPipe(s *Store, data []byte) (int, error) {
	dbPath, cleanup, err := extractNewPipeDBFromBytes(data)
	if err != nil {
		return 0, fmt.Errorf("import newpipe: %w", err)
	}
	defer cleanup()

	npDB, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return 0, fmt.Errorf("import newpipe: open db: %w", err)
	}
	defer npDB.Close()

	// service_id=0 means YouTube in NewPipe.
	rows, err := npDB.Query(`SELECT url, name FROM subscriptions WHERE url != ''`)
	if err != nil {
		return 0, fmt.Errorf("import newpipe: query subscriptions: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var channelURL, name string
		if err := rows.Scan(&channelURL, &name); err != nil {
			continue
		}
		if _, err := s.CreateSubscription(Subscription{
			Kind:            "youtube",
			URL:             channelURL,
			Title:           name,
			PollIntervalSec: 1800,
			NextCheckAt:     1,
		}); err == nil {
			count++
		}
		// Duplicate URLs are rejected by the UNIQUE constraint; skip silently.
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("import newpipe: rows: %w", err)
	}

	return count, nil
}

// extractNewPipeDBFromBytes extracts newpipe.db from a ZIP archive in memory,
// writes it to a temp file, and returns the path plus a cleanup function.
func extractNewPipeDBFromBytes(data []byte) (string, func(), error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", func() {}, fmt.Errorf("open zip: %w", err)
	}

	for _, f := range r.File {
		if filepath.Base(f.Name) != "newpipe.db" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", func() {}, fmt.Errorf("open newpipe.db in zip: %w", err)
		}
		tmp, err := os.CreateTemp("", "newpipe-*.db")
		if err != nil {
			rc.Close()
			return "", func() {}, fmt.Errorf("create temp: %w", err)
		}
		if _, err := io.Copy(tmp, rc); err != nil {
			rc.Close()
			tmp.Close()
			os.Remove(tmp.Name())
			return "", func() {}, fmt.Errorf("write temp: %w", err)
		}
		rc.Close()
		tmpPath := tmp.Name()
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			return "", func() {}, fmt.Errorf("close temp: %w", err)
		}
		return tmpPath, func() { os.Remove(tmpPath) }, nil
	}

	return "", func() {}, fmt.Errorf("newpipe.db not found in ZIP")
}
