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

// parseNewPipe lists the YouTube channels in a NewPipe ZIP export (as raw
// bytes).
func parseNewPipe(data []byte) ([]importEntry, error) {
	dbPath, cleanup, err := extractNewPipeDBFromBytes(data)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	npDB, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer npDB.Close()

	rows, err := npDB.Query(`SELECT url, name FROM subscriptions WHERE url != ''`)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	var entries []importEntry
	for rows.Next() {
		sub := Subscription{Kind: "youtube"}
		if err := rows.Scan(&sub.URL, &sub.Title); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		entries = append(entries, importEntry{Subscription: sub})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return entries, nil
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
