package files

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/vfs"
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
	Mount        string // the mount it was trashed within
}

// TrashItem.Mount is the mount an item was trashed within. Trashing is a
// rename into vfs.TrashDir at that mount's root, inside the same backend, so
// it costs nothing however large the item, and works on any mount that can
// rename. The mount FS hides the directory; it is reached here through
// MountRoot.

// trashBin is one mount's trash.
type trashBin struct {
	fs        vfs.FS
	dir       string // the trash directory within fs
	mountPath string
}

func (b trashBin) path(trashName string) string { return path.Join(b.dir, trashName) }

// binFor returns the trash of the mount holding filePath, and filePath's
// path within its backend.
func (p *Plugin) binFor(username, filePath string) (trashBin, string, error) {
	fsys, err := p.userFS(username)
	if err != nil {
		return trashBin{}, "", err
	}
	mr, ok := fsys.(vfs.MountRooter)
	if !ok {
		return trashBin{}, "", fmt.Errorf("trash: %s has no mounts", username)
	}
	backend, root, mountPath, readOnly, err := mr.MountRoot(filePath)
	if err != nil {
		return trashBin{}, "", fmt.Errorf("trash: %w", err)
	}
	if readOnly {
		return trashBin{}, "", vfs.ErrReadOnly
	}
	rel := strings.TrimPrefix(path.Clean("/"+filePath), mountPath)
	return trashBin{fs: backend, dir: path.Join(root, vfs.TrashDir), mountPath: mountPath}, path.Join(root, rel), nil
}

// bin returns the trash of the mount at mountPath.
func (p *Plugin) bin(username, mountPath string) (trashBin, error) {
	b, _, err := p.binFor(username, mountPath)
	if err != nil {
		return trashBin{}, err
	}
	if b.mountPath != mountPath {
		return trashBin{}, fmt.Errorf("trash: nothing is mounted at %s any more", mountPath)
	}
	return b, nil
}

// Trash moves a file or directory to the trash.
func (p *Plugin) Trash(username, filePath string) error {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return err
	}
	// What is deleted from a folder shared with username goes to its
	// owner's trash: it is theirs to restore.
	t, ok, err := p.writable(username, filePath)
	if err != nil {
		return err
	}
	if ok {
		if t.mountRoot {
			return errMountPoint
		}
		if err := p.Trash(t.owner, t.path); err != nil {
			return err
		}
		return forgetRows(db, filePath)
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return err
	}

	// A directory's trash entry carries its contents' size: they still
	// count toward quota.
	size := meta.Size
	if meta.IsDir {
		n, prefix := subtree(filePath)
		if err := db.QueryRow(`SELECT coalesce(sum(size), 0) FROM files WHERE is_dir = 0 AND substr(path, 1, ?) = ?`,
			n, prefix).Scan(&size); err != nil {
			return fmt.Errorf("size of %s: %w", filePath, err)
		}
	}

	now := p.ctx.Now().Unix()
	trashName := meta.Name + ".d" + strconv.FormatInt(now, 10) + "." + strconv.FormatInt(meta.ID, 10)

	bin, src, err := p.binFor(username, filePath)
	if err != nil {
		return err
	}
	if err := bin.fs.Mkdir(bin.dir); err != nil {
		return fmt.Errorf("create trash dir: %w", err)
	}
	dst := bin.path(trashName)
	if err := bin.fs.Rename(src, dst); err != nil {
		return fmt.Errorf("move to trash: %w", err)
	}

	// Atomically: insert trash record + delete files metadata. On any failure,
	// roll back the disk rename so we don't leave orphan files in trash with
	// no metadata or vice versa.
	tx, err := db.Begin()
	if err != nil {
		p.undoMove(bin.fs, dst, src)
		return fmt.Errorf("begin trash tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			p.undoMove(bin.fs, dst, src)
		}
	}()

	if _, err := tx.Exec(`
		INSERT INTO trash (original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name, mount)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		meta.Path, meta.Name, size, meta.MTime, meta.MimeType,
		boolToInt(meta.IsDir), now, trashName, bin.mountPath); err != nil {
		return fmt.Errorf("insert trash record: %w", err)
	}

	if err := forgetRows(tx, filePath); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trash: %w", err)
	}
	commit = true
	if meta.IsDir {
		p.treeChanged()
	}
	return nil
}

// ListTrash returns all trashed items for a user.
func (p *Plugin) ListTrash(username string) ([]TrashItem, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name, mount
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
			&item.DeletedAt, &item.TrashName, &item.Mount); err != nil {
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
		SELECT id, original_path, original_name, size, mtime, mime_type, is_dir, deleted_at, trash_name, mount
		FROM trash WHERE trash_name = ?`, trashName).Scan(
		&item.ID, &item.OriginalPath, &item.OriginalName,
		&item.Size, &item.MTime, &item.MimeType, &isDir,
		&item.DeletedAt, &item.TrashName, &item.Mount)
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

	fsys, err := p.userFS(username)
	if err != nil {
		return err
	}
	bin, err := p.bin(username, item.Mount)
	if err != nil {
		return err
	}
	restorePath, err := freeRestorePath(db, fsys, normalizePath(item.OriginalPath), item.IsDir)
	if err != nil {
		return err
	}
	dstBin, dstPath, err := p.binFor(username, restorePath)
	if err != nil {
		return err
	}
	if dstBin.mountPath != bin.mountPath {
		return fmt.Errorf("restore %s: it was trashed from another mount than now holds %s", trashName, restorePath)
	}
	srcPath := bin.path(trashName)

	// Ensure parent directory exists
	if err := fsys.Mkdir(path.Dir(restorePath)); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	if err := bin.fs.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("restore from trash: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		p.undoMove(bin.fs, dstPath, srcPath)
		return fmt.Errorf("begin restore tx: %w", err)
	}
	commit := false
	defer func() {
		if !commit {
			_ = tx.Rollback()
			p.undoMove(bin.fs, dstPath, srcPath)
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
func freeRestorePath(db *sql.DB, fsys vfs.FS, origPath string, isDir bool) (string, error) {
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
		taken, err := pathTaken(db, fsys, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
}

// pathTaken reports whether filePath exists on disk or in the metadata.
func pathTaken(db *sql.DB, fsys vfs.FS, filePath string) (bool, error) {
	if _, err := fsys.Stat(filePath); err == nil {
		return true, nil
	} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, vfs.ErrNotFound) {
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
func (p *Plugin) undoMove(fsys vfs.FS, movedTo, from string) {
	if err := fsys.Rename(movedTo, from); err != nil {
		p.log.Error("undo move", "from", movedTo, "to", from, "err", err)
	}
}

// PermanentDelete permanently removes a trashed item.
func (p *Plugin) PermanentDelete(username, trashName string) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}

	item, err := p.GetTrashItem(username, trashName)
	if err != nil {
		return err
	}
	// A mount that has gone since takes its trash with it: the record goes,
	// so the item stops counting toward quota, and the files are left to
	// whoever has the volume.
	if bin, err := p.bin(username, item.Mount); err != nil {
		p.log.Warn("trash: mount gone; forgetting the item", "user", username, "item", trashName, "err", err)
	} else if err := bin.fs.Delete(bin.path(trashName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove trashed item: %w", err)
	}

	if _, err := db.Exec(`DELETE FROM trash WHERE trash_name = ?`, trashName); err != nil {
		return fmt.Errorf("delete trash record: %w", err)
	}

	return nil
}

// EmptyTrash permanently deletes all trashed items.
func (p *Plugin) EmptyTrash(username string) error {
	items, err := p.ListTrash(username)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := p.PermanentDelete(username, item.TrashName); err != nil {
			return fmt.Errorf("empty trash: %w", err)
		}
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
