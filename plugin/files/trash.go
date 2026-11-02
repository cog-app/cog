package files

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
		p.undoMove(dstPath, srcPath)
		return fmt.Errorf("begin trash tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			p.undoMove(dstPath, srcPath)
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

// GetTrashItem returns a single trash item by trash name, or ErrNotFound.
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
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("trash item %q: %w", trashName, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get trash item: %w", err)
	}
	item.IsDir = isDir != 0
	return &item, nil
}

// RestoreFromTrash restores a trashed item to its original location. If
// something has since taken that path, the item comes back beside it as
// "name (restored).ext" (then "name (restored 2).ext", ...) rather than
// replacing it, as Nextcloud does.
func (p *Plugin) RestoreFromTrash(username, trashName string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	item, err := p.GetTrashItem(username, trashName)
	if err != nil {
		return err
	}

	filesDir := p.ctx.UserFilesDir(username)
	restorePath, err := freeRestorePath(db, filesDir, normalizePath(item.OriginalPath), item.IsDir)
	if err != nil {
		return err
	}
	srcPath := filepath.Join(p.trashDir(username), trashName)
	dstPath := filepath.Join(filesDir, restorePath)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("restore from trash: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		p.undoMove(dstPath, srcPath)
		return fmt.Errorf("begin restore tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			p.undoMove(dstPath, srcPath)
		}
	}()

	if _, err := tx.Exec(`
		INSERT INTO files (path, parent_path, name, size, mtime, mime_type, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		restorePath, filepath.Dir(restorePath), filepath.Base(restorePath),
		item.Size, item.MTime, item.MimeType, generateETag(), boolToInt(item.IsDir)); err != nil {
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

// freeRestorePath returns origPath if nothing occupies it, on disk or in the
// metadata, or else the first free "name (restored).ext", "name (restored
// 2).ext", ... beside it. A folder's whole name is the stem.
func freeRestorePath(db *sql.DB, filesDir, origPath string, isDir bool) (string, error) {
	dir, name := filepath.Dir(origPath), filepath.Base(origPath)
	stem, ext := name, ""
	if !isDir {
		if e := filepath.Ext(name); e != name {
			stem, ext = strings.TrimSuffix(name, e), e
		}
	}
	for n := 0; ; n++ {
		candidate := origPath
		switch n {
		case 0:
		case 1:
			candidate = filepath.Join(dir, stem+" (restored)"+ext)
		default:
			candidate = filepath.Join(dir, stem+" (restored "+strconv.Itoa(n)+")"+ext)
		}
		taken, err := pathTaken(db, filesDir, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
}

// pathTaken reports whether filePath exists on disk or in the metadata.
func pathTaken(db *sql.DB, filesDir, filePath string) (bool, error) {
	if _, err := os.Lstat(filepath.Join(filesDir, filePath)); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", filePath, err)
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM files WHERE path = ?)`, filePath).Scan(&exists); err != nil {
		return false, fmt.Errorf("check %s: %w", filePath, err)
	}
	return exists, nil
}

// undoMove puts back a rename whose metadata change failed. The failure is
// already being returned; this one can only be logged.
func (p *Plugin) undoMove(movedTo, from string) {
	if err := os.Rename(movedTo, from); err != nil {
		p.log.Error("undo move", "from", movedTo, "to", from, "err", err)
	}
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
