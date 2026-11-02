package files

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// TrashItem represents a trashed file or directory.
type TrashItem struct {
	ID           int64
	OriginalPath string
	OriginalName string
	Size         int64
	MTime        int64
	MimeType     string
	IsDir        bool
	DeletedAt    int64
	TrashName    string // unique name in the trash directory
}

// trashDir returns the path to the user's trash directory on disk.
func (p *Plugin) trashDir(username string) string {
	return filepath.Join(p.ctx.UserFilesDir(username), "..", "trash")
}

// Trash moves a file or directory to the trash.
func (p *Plugin) Trash(username, filePath string) error {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return err
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return err
	}

	now := p.ctx.Now().Unix()
	trashName := meta.Name + ".d" + strconv.FormatInt(now, 10) + "." + strconv.FormatInt(meta.ID, 10)

	// Move file on disk to trash directory.
	// Trash is always a local directory; VFS rename would fail across mounts.
	trashDir := p.trashDir(username)
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return fmt.Errorf("create trash dir: %w", err)
	}

	srcPath := filepath.Join(p.ctx.UserFilesDir(username), filePath)
	dstPath := filepath.Join(trashDir, trashName)

	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	// Atomically: insert trash record + delete files metadata. On any failure,
	// roll back the disk rename so we don't leave orphan files in trash with
	// no metadata or vice versa.
	tx, err := db.Begin()
	if err != nil {
		_ = os.Rename(dstPath, srcPath)
		return fmt.Errorf("begin trash tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			_ = os.Rename(dstPath, srcPath)
		}
	}()

	if _, err := tx.Exec(`
		INSERT INTO trash (original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.Path, meta.Name, meta.Size, meta.MTime, meta.MimeType,
		boolToInt(meta.IsDir), now, trashName); err != nil {
		return fmt.Errorf("insert trash record: %w", err)
	}

	if meta.IsDir {
		if _, err := tx.Exec(`DELETE FROM files WHERE path LIKE ? OR path = ?`,
			filePath+"/%", filePath); err != nil {
			return fmt.Errorf("delete dir metadata: %w", err)
		}
	} else {
		if _, err := tx.Exec(`DELETE FROM files WHERE path = ?`, filePath); err != nil {
			return fmt.Errorf("delete file metadata: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trash: %w", err)
	}
	commit = true
	return nil
}

// ListTrash returns all trashed items for a user.
func (p *Plugin) ListTrash(username string) ([]TrashItem, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name
		FROM trash ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list trash: %w", err)
	}
	defer rows.Close()

	var items []TrashItem
	for rows.Next() {
		var item TrashItem
		var isDir int
		if err := rows.Scan(&item.ID, &item.OriginalPath, &item.OriginalName,
			&item.Size, &item.MTime, &item.MimeType, &isDir,
			&item.DeletedAt, &item.TrashName); err != nil {
			return nil, fmt.Errorf("scan trash row: %w", err)
		}
		item.IsDir = isDir != 0
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetTrashItem returns a single trash item by trash name.
func (p *Plugin) GetTrashItem(username, trashName string) (*TrashItem, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	var item TrashItem
	var isDir int
	err = db.QueryRow(`
		SELECT id, original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name
		FROM trash WHERE trash_name = ?`, trashName).Scan(
		&item.ID, &item.OriginalPath, &item.OriginalName,
		&item.Size, &item.MTime, &item.MimeType, &isDir,
		&item.DeletedAt, &item.TrashName)
	if err != nil {
		return nil, fmt.Errorf("get trash item: %w", err)
	}
	item.IsDir = isDir != 0
	return &item, nil
}

// RestoreFromTrash restores a trashed item to its original location.
func (p *Plugin) RestoreFromTrash(username, trashName string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	item, err := p.GetTrashItem(username, trashName)
	if err != nil {
		return err
	}

	trashDir := p.trashDir(username)
	srcPath := filepath.Join(trashDir, trashName)
	dstPath := filepath.Join(p.ctx.UserFilesDir(username), item.OriginalPath)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("restore from trash: %w", err)
	}

	parentPath := filepath.Dir(item.OriginalPath)
	if parentPath == "." {
		parentPath = "/"
	}

	tx, err := db.Begin()
	if err != nil {
		_ = os.Rename(dstPath, srcPath)
		return fmt.Errorf("begin restore tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			_ = os.Rename(dstPath, srcPath)
		}
	}()

	isDirInt := 0
	if item.IsDir {
		isDirInt = 1
	}
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO files (path, parent_path, name, size, mtime, mime_type, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.OriginalPath, parentPath, item.OriginalName, item.Size, item.MTime, item.MimeType, generateETag(), isDirInt); err != nil {
		return fmt.Errorf("restore metadata: %w", err)
	}

	if _, err := tx.Exec(`DELETE FROM trash WHERE trash_name = ?`, trashName); err != nil {
		return fmt.Errorf("delete trash record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit restore: %w", err)
	}
	commit = true
	return nil
}

// PermanentDelete permanently removes a trashed item.
func (p *Plugin) PermanentDelete(username, trashName string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	if _, err := p.GetTrashItem(username, trashName); err != nil {
		return err
	}

	trashPath := filepath.Join(p.trashDir(username), trashName)
	if err := os.RemoveAll(trashPath); err != nil {
		return fmt.Errorf("remove trashed item: %w", err)
	}

	if _, err := db.Exec(`DELETE FROM trash WHERE trash_name = ?`, trashName); err != nil {
		return fmt.Errorf("delete trash record: %w", err)
	}

	return nil
}

// EmptyTrash permanently deletes all trashed items.
func (p *Plugin) EmptyTrash(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	trashDir := p.trashDir(username)
	if err := os.RemoveAll(trashDir); err != nil {
		return fmt.Errorf("remove trash dir: %w", err)
	}

	if _, err := db.Exec(`DELETE FROM trash`); err != nil {
		return fmt.Errorf("empty trash: %w", err)
	}

	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
