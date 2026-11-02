package gallery

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"text/template"
	"time"
)

// RenameData is the template context for file rename patterns.
type RenameData struct {
	Year     string
	Month    string
	Day      string
	Hour     string
	Minute   string
	Second   string
	Camera   string
	Lens     string
	Original string // filename without extension
	Ext      string // e.g. ".jpg"
	Seq      int    // conflict sequence number
	Place    string
	Country  string
	Width    int
	Height   int
}

// RenamePreview represents a planned rename operation.
type RenamePreview struct {
	MediaID int64
	FileID  int64
	OldPath string
	NewPath string
}

// PreviewRename computes the new paths for all media matching the rename pattern.
// Does not modify any files.
func PreviewRename(db *sql.DB, pattern string) ([]RenamePreview, error) {
	tmpl, err := template.New("rename").Parse(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid rename pattern: %w", err)
	}

	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media m ORDER BY COALESCE(m.date_taken, m.date_indexed)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items, err := scanMediaRows(rows)
	if err != nil {
		return nil, err
	}

	// Track target paths to detect conflicts.
	usedPaths := make(map[string]int)
	var previews []RenamePreview

	for i := range items {
		m := &items[i]
		data := buildRenameData(m, 0)
		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			slog.Warn("rename template exec", "path", m.FilePath, "err", err)
			continue
		}

		newPath := normalizePath(buf.String())
		if newPath == "" || newPath == m.FilePath || newPath == normalizePath(m.FilePath) {
			continue
		}

		// Validate: path must not escape root.
		if strings.Contains(newPath, "..") {
			slog.Warn("rename path traversal rejected", "path", newPath)
			continue
		}

		// Handle conflicts by appending _N.
		for usedPaths[newPath] > 0 {
			usedPaths[newPath]++
			ext := filepath.Ext(newPath)
			base := strings.TrimSuffix(newPath, ext)
			newPath = fmt.Sprintf("%s_%d%s", base, usedPaths[newPath]-1, ext)
		}
		usedPaths[newPath] = 1

		previews = append(previews, RenamePreview{
			MediaID: m.ID,
			FileID:  m.FileID,
			OldPath: m.FilePath,
			NewPath: newPath,
		})
	}

	return previews, nil
}

// ApplyRename executes the rename operations using the files plugin.
func (p *Plugin) ApplyRename(username string, previews []RenamePreview) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return err
	}
	if len(previews) == 0 {
		return nil
	}

	db, err := p.DB(username)
	if err != nil {
		return err
	}

	batchID, err := NextRenameBatchID(db)
	if err != nil {
		return fmt.Errorf("next batch id: %w", err)
	}

	// One file that cannot be renamed does not stop the rest; the failures
	// are reported together at the end.
	var errs []error
	for _, pr := range previews {
		if _, err := fp.Rename(username, pr.OldPath, pr.NewPath); err != nil {
			p.log.Error("rename", "from", pr.OldPath, "to", pr.NewPath, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", pr.OldPath, err))
			continue
		}

		// Log for undo first: a renamed file the batch does not know of
		// could not be put back.
		if err := LogRename(db, batchID, pr.FileID, pr.OldPath, pr.NewPath); err != nil {
			p.log.Error("log rename", "from", pr.OldPath, "err", err)
			errs = append(errs, fmt.Errorf("%s: record for undo: %w", pr.OldPath, err))
		}

		// Update gallery media path.
		if _, err := db.Exec(`UPDATE media SET file_path = ? WHERE id = ?`, pr.NewPath, pr.MediaID); err != nil {
			p.log.Error("update media path", "media_id", pr.MediaID, "err", err)
			errs = append(errs, fmt.Errorf("%s: update gallery: %w", pr.OldPath, err))
		}
	}

	return renameErrors(errs, len(previews))
}

// renameErrors summarises the failures of a batch of n renames, or is nil.
func renameErrors(errs []error, n int) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%d of %d failed: %w", len(errs), n, errors.Join(errs...))
}

// UndoLastRename reverses the most recent rename batch.
func (p *Plugin) UndoLastRename(username string) error {
	fp, err := p.filesPlugin()
	if err != nil {
		return err
	}
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	batchID, entries, err := LastRenameBatch(db)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no rename batch to undo")
	}

	// Reverse in reverse order.
	var errs []error
	for _, e := range slices.Backward(entries) {
		if _, err := fp.Rename(username, e.NewPath, e.OldPath); err != nil {
			p.log.Error("undo rename", "from", e.NewPath, "to", e.OldPath, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", e.NewPath, err))
			continue
		}
		// Update gallery media path.
		if _, err := db.Exec(`UPDATE media SET file_path = ? WHERE file_id = ?`, e.OldPath, e.FileID); err != nil {
			p.log.Error("undo update media path", "file_id", e.FileID, "err", err)
			errs = append(errs, fmt.Errorf("%s: update gallery: %w", e.OldPath, err))
		}
	}

	if err := DeleteRenameBatch(db, batchID); err != nil {
		errs = append(errs, fmt.Errorf("forget batch: %w", err))
	}
	return renameErrors(errs, len(entries))
}

func buildRenameData(m *Media, seq int) RenameData {
	var t time.Time
	if m.DateTaken != nil {
		t = time.Unix(*m.DateTaken, 0).UTC()
	} else {
		t = time.Unix(m.DateIndexed, 0).UTC()
	}

	ext := filepath.Ext(m.FilePath)
	base := filepath.Base(m.FilePath)
	original := strings.TrimSuffix(base, ext)

	camera := sanitizeFilename(strings.TrimSpace(m.CameraMake + " " + m.CameraModel))
	lens := sanitizeFilename(m.LensModel)

	return RenameData{
		Year:     t.Format("2006"),
		Month:    t.Format("01"),
		Day:      t.Format("02"),
		Hour:     t.Format("15"),
		Minute:   t.Format("04"),
		Second:   t.Format("05"),
		Camera:   camera,
		Lens:     lens,
		Original: original,
		Ext:      ext,
		Seq:      seq,
		Place:    m.City,
		Country:  m.Country,
		Width:    m.Width,
		Height:   m.Height,
	}
}

// sanitizeFilename removes characters unsafe for filenames.
func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\x00':
			return '_'
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// normalizePath cleans up a file path, removing leading/trailing slashes and double slashes.
func normalizePath(p string) string {
	p = filepath.Clean(p)
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "./")
	return p
}
