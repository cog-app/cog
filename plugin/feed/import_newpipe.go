package feed

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
			return count, fmt.Errorf("import newpipe: scan: %w", err)
		}
		created, err := s.importSubscription(Subscription{
			Kind:            "youtube",
			URL:             channelURL,
			Title:           name,
			PollIntervalSec: 1800,
			NextCheckAt:     1,
		})
		if err != nil {
			return count, fmt.Errorf("import newpipe: %w", err)
		}
		if created {
			count++
		}
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
			return "", func() {}, fmt.Errorf("create temp: %w", errors.Join(err, rc.Close()))
		}
		tmpPath := tmp.Name()
		if _, err := io.Copy(tmp, rc); err != nil {
			return "", func() {}, fmt.Errorf("write temp: %w", errors.Join(err, rc.Close(), tmp.Close(), os.Remove(tmpPath)))
		}
		if err := rc.Close(); err != nil {
			return "", func() {}, fmt.Errorf("read newpipe.db in zip: %w", errors.Join(err, tmp.Close(), os.Remove(tmpPath)))
		}
		if err := tmp.Close(); err != nil {
			return "", func() {}, fmt.Errorf("close temp: %w", errors.Join(err, os.Remove(tmpPath)))
		}
		return tmpPath, func() {
			if err := os.Remove(tmpPath); err != nil {
				slog.Warn("feed import newpipe: remove temp", "path", tmpPath, "err", err)
			}
		}, nil
	}

	return "", func() {}, fmt.Errorf("newpipe.db not found in ZIP")
}
