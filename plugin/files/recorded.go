package files

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// subtree returns what selects the paths beneath dir in SQL, as
// `substr(path, 1, n) = prefix`. Not LIKE: that ignores ASCII case and
// treats _ and % as wildcards. substr counts characters, not bytes, so n
// does too.
func subtree(dir string) (n int, prefix string) {
	prefix = dir + "/"
	if dir == "/" {
		prefix = "/"
	}
	return utf8.RuneCountInString(prefix), prefix
}

// execer is a database or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// forgetRows removes the rows of path and everything beneath it.
func forgetRows(db execer, path string) error {
	n, prefix := subtree(path)
	if _, err := db.Exec(`DELETE FROM files WHERE path = ? OR substr(path, 1, ?) = ?`,
		path, n, prefix); err != nil {
		return fmt.Errorf("forget %s: %w", path, err)
	}
	return nil
}

// record brings path's row in step with what fsys holds there, for a change
// made without the plugin: it is added or updated, or forgotten with
// everything beneath it when path is gone.
func (p *Plugin) record(db *sql.DB, fsys vfs.FS, path string) (*FileMeta, error) {
	path = normalizePath(path)
	fi, err := fsys.Stat(path)
	if errors.Is(err, vfs.ErrNotFound) || errors.Is(err, iofs.ErrNotExist) {
		if err := forgetRows(db, path); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	var size int64
	mimeType := ""
	if !fi.IsDir {
		size = fi.Size
		mimeType = detectMimeType(filepath.Base(path))
	}
	if _, err := db.Exec(`
		INSERT INTO files (path, parent_path, name, size, mtime, disk_mtime, mime_type, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			size = excluded.size,
			mtime = excluded.mtime,
			disk_mtime = excluded.disk_mtime,
			mime_type = excluded.mime_type,
			etag = excluded.etag,
			is_dir = excluded.is_dir`,
		path, filepath.Dir(path), filepath.Base(path), size, fi.ModTime.Unix(), fi.ModTime.Unix(),
		mimeType, generateETag(), boolToInt(fi.IsDir)); err != nil {
		return nil, fmt.Errorf("record %s: %w", path, err)
	}
	return getByPath(db, path)
}

// recordDirs records dir and those of its parents that have no row, after
// they were made on disk.
func (p *Plugin) recordDirs(db *sql.DB, fsys vfs.FS, dir string) error {
	for dir = normalizePath(dir); dir != "/"; dir = filepath.Dir(dir) {
		var known bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM files WHERE path = ?)`, dir).Scan(&known); err != nil {
			return fmt.Errorf("check %s: %w", dir, err)
		}
		if known {
			return nil
		}
		if _, err := p.record(db, fsys, dir); err != nil {
			return err
		}
	}
	return nil
}

// budget is what a user's writers in progress (uploads, and handles open
// through FS) may still add to their files. The usage the quota is checked
// against comes from the rows, which a writer updates only when it is done,
// so writers at once share one budget instead of each being allowed the
// whole of what is left.
type budget struct {
	handles int   // open write handles
	left    int64 // bytes they may still add; negative for no limit
	pending int64 // bytes they have added that no row records yet
}

// openBudget joins username's budget, starting it from the quota when no
// other handle has.
func (p *Plugin) openBudget(username string) error {
	p.budgetsMu.Lock()
	defer p.budgetsMu.Unlock()
	if b := p.budgets[username]; b != nil {
		b.handles++
		return nil
	}
	remaining, err := p.ctx.QuotaRemaining(username)
	if err != nil {
		return err
	}
	if p.budgets == nil {
		p.budgets = make(map[string]*budget)
	}
	p.budgets[username] = &budget{handles: 1, left: remaining}
	return nil
}

// grow takes delta bytes, which may be negative, from username's budget,
// or fails without taking them when there is not room.
func (p *Plugin) grow(username string, delta int64) error {
	p.budgetsMu.Lock()
	defer p.budgetsMu.Unlock()
	b := p.budgets[username]
	if b.left >= 0 {
		if delta > b.left {
			return fmt.Errorf("%w: the file would be larger than the space left", cogplugin.ErrQuotaExceeded)
		}
		b.left -= delta
	}
	b.pending += delta
	return nil
}

// closeBudget leaves username's budget once a handle's grown bytes are in
// its row.
func (p *Plugin) closeBudget(username string, grown int64) {
	p.budgetsMu.Lock()
	defer p.budgetsMu.Unlock()
	b := p.budgets[username]
	b.pending -= grown
	if b.handles--; b.handles == 0 {
		delete(p.budgets, username)
	}
}

// room returns what username's open writers may still add, or a negative
// number for no limit. They must have joined the budget.
func (p *Plugin) room(username string) int64 {
	p.budgetsMu.Lock()
	defer p.budgetsMu.Unlock()
	return p.budgets[username].left
}

// give returns n bytes taken from username's budget.
func (p *Plugin) give(username string, n int64) {
	p.budgetsMu.Lock()
	defer p.budgetsMu.Unlock()
	b := p.budgets[username]
	if b.left >= 0 {
		b.left += n
	}
	b.pending -= n
}

// chargedReader takes what it reads beyond end from its writer's budget,
// failing the read when there is not room.
type chargedReader struct {
	r        io.Reader
	p        *Plugin
	username string
	n        int64 // bytes read
	end      int64 // bytes charged for, counting the file replaced
}

func (c *chargedReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	if c.n > c.end {
		if gerr := c.p.grow(c.username, c.n-c.end); gerr != nil {
			return n, gerr
		}
		c.end = c.n
	}
	return n, err
}

// FS returns username's files as a VFS that keeps the plugin's records:
// writes are held to the quota and each change updates the rows, as it
// would through Upload, CreateDir, Rename and HardDelete. It is for clients
// that need a filesystem rather than the plugin's API, such as SFTP, SCP and
// the shell. Deleting through it does not use the trash: editors and
// transfer clients save by writing a new file and removing the old, and the
// trash would keep every version.
func (p *Plugin) FS(username string) (vfs.FS, error) {
	fsys, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return &recordedFS{p: p, db: db, username: username, fs: fsys}, nil
}

type recordedFS struct {
	p        *Plugin
	db       *sql.DB
	username string
	fs       vfs.FS
}

var (
	_ vfs.FileOpener = (*recordedFS)(nil)
	_ vfs.DirReader  = (*recordedFS)(nil)
	_ vfs.AttrSetter = (*recordedFS)(nil)
)

func (f *recordedFS) List(path string) ([]vfs.FileInfo, error) { return f.fs.List(path) }
func (f *recordedFS) Stat(path string) (*vfs.FileInfo, error)  { return f.fs.Stat(path) }
func (f *recordedFS) Get(path string) (io.ReadCloser, error)   { return f.fs.Get(path) }
func (f *recordedFS) ReadFile(path string) ([]byte, error)     { return f.fs.ReadFile(path) }

func (f *recordedFS) Put(path string, content io.Reader) error {
	_, err := f.p.Upload(f.username, path, content)
	return err
}

// Mkdir makes path and any missing parents, and records each.
func (f *recordedFS) Mkdir(path string) error {
	path = normalizePath(path)
	if err := f.fs.Mkdir(path); err != nil {
		return err
	}
	return f.p.recordDirs(f.db, f.fs, path)
}

func (f *recordedFS) Delete(path string) error {
	path = normalizePath(path)
	if path == "/" {
		return fmt.Errorf("delete /: %w", iofs.ErrPermission)
	}
	if err := f.fs.Delete(path); err != nil {
		return err
	}
	if err := forgetRows(f.db, path); err != nil {
		return err
	}
	f.p.logActivity(f.username, "file_deleted", f.username+" deleted "+filepath.Base(path), "", path)
	return nil
}

func (f *recordedFS) Rename(oldPath, newPath string) error {
	_, err := f.p.Rename(f.username, oldPath, newPath)
	return err
}

func (f *recordedFS) StatInfo(path string) (iofs.FileInfo, error) {
	dr, err := f.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.StatInfo(path)
}

func (f *recordedFS) LstatInfo(path string) (iofs.FileInfo, error) {
	dr, err := f.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.LstatInfo(path)
}

func (f *recordedFS) ReadDirEntries(path string) ([]iofs.DirEntry, error) {
	dr, err := f.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.ReadDirEntries(path)
}

func (f *recordedFS) dirReader() (vfs.DirReader, error) {
	dr, ok := f.fs.(vfs.DirReader)
	if !ok {
		return nil, fmt.Errorf("read directory: %w", errors.ErrUnsupported)
	}
	return dr, nil
}

func (f *recordedFS) attrSetter() (vfs.AttrSetter, error) {
	as, ok := f.fs.(vfs.AttrSetter)
	if !ok {
		return nil, fmt.Errorf("set attributes: %w", errors.ErrUnsupported)
	}
	return as, nil
}

func (f *recordedFS) Chmod(path string, mode os.FileMode) error {
	as, err := f.attrSetter()
	if err != nil {
		return err
	}
	return as.Chmod(path, mode)
}

// Chtimes sets path's times, and its row's mtime with them: a client that
// preserves times (scp -p, sftp put -p) sets them after writing.
func (f *recordedFS) Chtimes(path string, atime, mtime time.Time) error {
	as, err := f.attrSetter()
	if err != nil {
		return err
	}
	if err := as.Chtimes(path, atime, mtime); err != nil {
		return err
	}
	_, err = f.p.record(f.db, f.fs, path)
	return err
}

// Truncate resizes path, held to the quota when it grows.
func (f *recordedFS) Truncate(path string, size int64) error {
	as, err := f.attrSetter()
	if err != nil {
		return err
	}
	fi, err := f.fs.Stat(path)
	if err != nil {
		return err
	}
	if err := f.p.openBudget(f.username); err != nil {
		return err
	}
	delta := size - fi.Size
	if err := f.p.grow(f.username, delta); err != nil {
		f.p.closeBudget(f.username, 0)
		return err
	}
	if err := as.Truncate(path, size); err != nil {
		err = errors.Join(err, f.p.grow(f.username, -delta))
		f.p.closeBudget(f.username, 0)
		return err
	}
	_, err = f.p.record(f.db, f.fs, path)
	f.p.closeBudget(f.username, delta)
	return err
}

// Link is refused: two rows for one file's bytes would count them twice.
func (f *recordedFS) Link(oldname, newname string) error {
	return fmt.Errorf("link %s: %w", newname, errors.ErrUnsupported)
}

// OpenFile opens path. A handle that can write is held to the quota as it
// writes, and records the file when it is closed.
func (f *recordedFS) OpenFile(path string, flag int, perm os.FileMode) (vfs.File, error) {
	opener, ok := f.fs.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("open %s: %w", path, errors.ErrUnsupported)
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) == 0 {
		return opener.OpenFile(path, flag, perm)
	}

	var before int64
	if fi, err := f.fs.Stat(path); err == nil {
		before = fi.Size
	} else if !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, iofs.ErrNotExist) {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if err := f.p.openBudget(f.username); err != nil {
		return nil, err
	}
	file, err := opener.OpenFile(path, flag, perm)
	if err != nil {
		f.p.closeBudget(f.username, 0)
		return nil, err
	}
	end := before
	if flag&os.O_TRUNC != 0 {
		end = 0
	}
	h := &recordedFile{File: file, fs: f, path: path, appending: flag&os.O_APPEND != 0, before: before, end: before}
	// Truncating on open freed what the file held.
	if err := h.resize(end); err != nil {
		f.p.closeBudget(f.username, 0)
		return nil, errors.Join(err, file.Close())
	}
	return h, nil
}

// recordedFile is a file open for writing through recordedFS.
type recordedFile struct {
	vfs.File
	fs        *recordedFS
	path      string
	appending bool
	before    int64 // the size when opened
	end       int64 // the size as far as its writes and truncations have made it
}

// resize moves the file's end to n, taking any growth from the budget first.
func (h *recordedFile) resize(n int64) error {
	if err := h.fs.p.grow(h.fs.username, n-h.end); err != nil {
		return err
	}
	h.end = n
	return nil
}

// reach makes room for a write ending at n.
func (h *recordedFile) reach(n int64) error {
	if n <= h.end {
		return nil
	}
	return h.resize(n)
}

func (h *recordedFile) Write(b []byte) (int, error) {
	pos := h.end
	if !h.appending {
		var err error
		if pos, err = h.File.Seek(0, io.SeekCurrent); err != nil {
			return 0, err
		}
	}
	if err := h.reach(pos + int64(len(b))); err != nil {
		return 0, err
	}
	return h.File.Write(b)
}

func (h *recordedFile) WriteAt(b []byte, off int64) (int, error) {
	if err := h.reach(off + int64(len(b))); err != nil {
		return 0, err
	}
	return h.File.WriteAt(b, off)
}

func (h *recordedFile) Truncate(size int64) error {
	if size > h.end {
		if err := h.resize(size); err != nil {
			return err
		}
		return h.File.Truncate(size)
	}
	if err := h.File.Truncate(size); err != nil {
		return err
	}
	return h.resize(size)
}

// Close closes the file and records it. Its growth leaves the budget only
// once the row counts it.
func (h *recordedFile) Close() error {
	err := h.File.Close()
	if _, rerr := h.fs.p.record(h.fs.db, h.fs.fs, h.path); rerr != nil {
		err = errors.Join(err, rerr)
	}
	h.fs.p.closeBudget(h.fs.username, h.end-h.before)
	return err
}
