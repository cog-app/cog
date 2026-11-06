// Package files implements the core privileged files plugin.
// It manages file/directory metadata and provides the authoritative
// metadata layer for user files.
package files

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/bodylimit"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

//go:embed sql
var sqlFS embed.FS

const PluginID = "files"

// ErrNotFound is a path or ID the metadata has no file for.
var ErrNotFound = errors.New("file not found")

// FileMeta represents a file or directory's metadata.
type FileMeta struct {
	ID         int64
	Path       string
	ParentPath string
	Name       string
	Size       int64
	MTime      int64
	MimeType   string
	ETag       string
	IsDir      bool
	Favorite   bool
}

// Plugin is the files plugin. It is a privileged internal plugin
// and cannot be loaded via gRPC.
type Plugin struct {
	plugin.Protected
	ctx           plugin.PluginContext
	log           *slog.Logger
	priv          plugin.PrivilegedContext
	iconProviders []cogplugin.FolderIconProvider

	// uploads serializes writes of the same file (see lockPath).
	uploadsMu sync.Mutex
	uploads   map[string]*pathLock
}

// pathLock is the lock of one file being uploaded, and how many uploads
// hold or wait for it.
type pathLock struct {
	sync.Mutex
	refs int
}

// lockPath locks username's filePath for an upload, from writing the bytes
// to recording them, and returns the unlock. Two uploads of one file, each
// writing and then upserting its row, could otherwise finish in opposite
// orders and leave one upload's size and ETag on the other's bytes. Only
// uploads of the same file wait for each other, and a lock is dropped once
// nobody holds it, so the map holds only files being uploaded.
func (p *Plugin) lockPath(username, filePath string) (unlock func()) {
	key := username + "\x00" + filePath
	p.uploadsMu.Lock()
	if p.uploads == nil {
		p.uploads = make(map[string]*pathLock)
	}
	l := p.uploads[key]
	if l == nil {
		l = &pathLock{}
		p.uploads[key] = l
	}
	l.refs++
	p.uploadsMu.Unlock()

	l.Lock()
	return func() {
		l.Unlock()
		p.uploadsMu.Lock()
		if l.refs--; l.refs == 0 {
			delete(p.uploads, key)
		}
		p.uploadsMu.Unlock()
	}
}

// New creates a new files plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

// DB returns the user's database for this plugin.
//
// The host owns the database: where it lives, how it is opened, and applying
// the schema registered in Register. This is a name for it, not a place that
// does any of that work.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	return p.ctx.PluginDB(username)
}

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	ctx.RegisterSchema(sqlFS)
	p.log = ctx.Logger()
	ctx.RegisterNavItem(plugin.NavItem{Label: "Files", URL: "/apps/files", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Files",
		Priority: 30,
		URL:      "/apps/files/",
		Render:   p.renderWidget,
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:         "file",
		Table:        "files",
		IDColumn:     "id",
		ParentColumn: "parent_path",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:         "directory",
		Table:        "files",
		IDColumn:     "id",
		ParentColumn: "parent_path",
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "files",
		EnrichSQL:    "SELECT rowid, name, path, mime_type FROM files WHERE rowid IN (?)",
		ContentFunc: func(files vfs.FS, cols map[string]string) string {
			if !isTextMIME(cols["mime_type"]) {
				return ""
			}
			fpath := cols["path"]
			if fpath == "" {
				return ""
			}
			info, err := files.Stat(fpath)
			if err != nil {
				// Gone since the change was queued: nothing to index.
				if !errors.Is(err, iofs.ErrNotExist) && !errors.Is(err, vfs.ErrNotFound) {
					p.log.Error("index file content", "path", fpath, "err", err)
				}
				return ""
			}
			if info.Size > 1<<20 { // skip >1MB
				return ""
			}
			data, err := files.ReadFile(fpath)
			if err != nil {
				p.log.Error("index file content", "path", fpath, "err", err)
				return ""
			}
			return string(data)
		},
	})

	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		p.priv = priv
		p.registerRoutes(priv)
	}

	return nil
}

func (p *Plugin) registerRoutes(priv plugin.PrivilegedContext) {
	pages := NewPages(p)
	csrf := auth.CSRF{Secret: p.ctx.CSRFSecret(), Cookies: p.ctx.Cookies(), Log: p.ctx.Logger()}
	csrfWrap := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(csrf.Require(h))
	}

	// Files browser UI
	priv.RegisterRoute("GET /apps/files/favorites", auth.RequireAuthWeb(http.HandlerFunc(pages.ServeFavorites)))
	priv.RegisterRoute("GET /apps/files/volumes", auth.RequireAuthWeb(http.HandlerFunc(pages.ServeVolumes)))
	priv.RegisterRoute("GET /apps/files/{path...}", auth.RequireAuthWeb(http.HandlerFunc(pages.ServeGet)))
	// Uploads post here; quota bounds them.
	priv.RegisterRoute("POST /apps/files/{path...}", bodylimit.Unlimited(csrfWrap(http.HandlerFunc(pages.ServePost))))
	priv.RegisterRoute("DELETE /apps/files/{path...}", csrfWrap(http.HandlerFunc(pages.ServeDelete)))
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return fmt.Errorf("recent files widget: %w", err)
	}
	files, err := RecentFiles(db, 5)
	if err != nil {
		return fmt.Errorf("recent files widget: %w", err)
	}
	return recentFilesWidget(files, p.ctx.Now()).Render(ctx, w)
}

func (p *Plugin) Start(ctx context.Context) error {
	// Collect folder icon providers from all registered plugins.
	// Done in Start() because all plugins have registered by this point.
	if p.priv != nil {
		for pl := range p.priv.Plugins() {
			if fp, ok := pl.(cogplugin.FolderIconProvider); ok {
				p.iconProviders = append(p.iconProviders, fp)
			}
		}
	}
	return nil
}

// folderIcon returns a custom SVG icon for a folder, or empty string for default.
func (p *Plugin) folderIcon(username, folderPath string) string {
	for _, provider := range p.iconProviders {
		if icon, ok := provider.FolderIcon(username, folderPath); ok {
			return icon
		}
	}
	return ""
}
func (p *Plugin) Stop() error { return nil }

// userFS returns the mount-aware VFS for the given user.
// ReadOnlyChecker returns a function that reports whether a path of the
// user's is on a read-only mount. It is built once per listing: the VFS
// is resolved once and the check itself touches no disk.
func (p *Plugin) ReadOnlyChecker(username string) (func(path string) bool, error) {
	fs, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	ro, ok := fs.(interface{ ReadOnly(string) bool })
	if !ok {
		return func(string) bool { return false }, nil
	}
	return func(path string) bool { return ro.ReadOnly(normalizePath(path)) }, nil
}

func (p *Plugin) userFS(username string) (vfs.FS, error) {
	return p.ctx.UserVFS(username)
}

// EnsureRootDir creates the root directory metadata entry if it doesn't exist.
func (p *Plugin) EnsureRootDir(username string) error {
	if err := p.ctx.EnsureUserDirs(username); err != nil {
		return err
	}
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT OR IGNORE INTO files (path, parent_path, name, mtime, etag, is_dir)
		VALUES ('/', '', '/', ?, ?, 1)`,
		p.ctx.Now().Unix(), generateETag())
	return err
}

// CreateDir creates a directory.
func (p *Plugin) CreateDir(username, dirPath string) (*FileMeta, error) {
	dirPath = normalizePath(dirPath)
	parentPath := filepath.Dir(dirPath)
	name := filepath.Base(dirPath)

	// Create on disk via VFS.
	fs, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	if err := fs.Mkdir(dirPath); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	now := p.ctx.Now().Unix()
	etag := generateETag()

	res, err := db.Exec(`
		INSERT INTO files (path, parent_path, name, mtime, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, 1)`,
		dirPath, parentPath, name, now, etag)
	if err != nil {
		return nil, fmt.Errorf("insert dir: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("insert dir id: %w", err)
	}
	p.logActivity(username, "file_created", username+" created folder "+name, strconv.FormatInt(id, 10), dirPath)
	return &FileMeta{
		ID:         id,
		Path:       dirPath,
		ParentPath: parentPath,
		Name:       name,
		MTime:      now,
		ETag:       etag,
		IsDir:      true,
	}, nil
}

// ensureDir creates dirPath unless the metadata already has it, as when an
// archive names a folder that an earlier entry, or an earlier extraction,
// already made.
func (p *Plugin) ensureDir(username, dirPath string) error {
	dirPath = normalizePath(dirPath)
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM files WHERE path = ?)`, dirPath).Scan(&exists); err != nil {
		return fmt.Errorf("check dir %s: %w", dirPath, err)
	}
	if exists {
		return nil
	}
	_, err = p.CreateDir(username, dirPath)
	return err
}

// Upload writes a file to disk and records its metadata.
func (p *Plugin) Upload(username, filePath string, content io.Reader) (*FileMeta, error) {
	filePath = normalizePath(filePath)
	defer p.lockPath(username, filePath)()
	parentPath := filepath.Dir(filePath)
	name := filepath.Base(filePath)

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	// The quota is checked as the bytes arrive, against the room left plus
	// what the file replaces, so an edit near the quota is not refused for
	// counting both, and a write that would go over fails before it
	// replaces anything: Put writes beside the file and renames.
	var replaced int64
	if old, err := getByPath(db, filePath); err == nil && !old.IsDir {
		replaced = old.Size
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	// The bytes are drawn from the budget writers in progress share as they
	// arrive, so uploads at once cannot each take the whole of what is left.
	w, err := p.beginWrite(username)
	if err != nil {
		return nil, err
	}
	body := w.Reader(content, replaced)
	recorded := false
	defer func() {
		if !recorded {
			w.Abandon()
		}
	}()
	// A known length is refused before anything is read.
	if room := w.Room(); room >= 0 {
		if lr, ok := content.(*io.LimitedReader); ok && lr.N-replaced > room {
			return nil, fmt.Errorf("%w: the upload is larger than the space left", cogplugin.ErrQuotaExceeded)
		}
	}

	fs, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	if err := fs.Mkdir(parentPath); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}
	// Folders the upload made are folders a client can address.
	if err := p.recordDirs(db, fs, parentPath); err != nil {
		return nil, err
	}
	if err := fs.Put(filePath, body); err != nil {
		if errors.Is(err, cogplugin.ErrQuotaExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("write file: %w", err)
	}
	size := body.N()
	// What reconcile compares to notice a later change made elsewhere.
	var diskMTime int64
	if fi, err := fs.Stat(filePath); err != nil {
		p.log.Warn("stat uploaded file; reconcile will record it", "path", filePath, "err", err)
	} else {
		diskMTime = fi.ModTime.Unix()
	}

	now := p.ctx.Now().Unix()
	etag := generateETag()
	mimeType := detectMimeType(name)

	// Upsert: if the path already exists, update it. RETURNING, not
	// LastInsertId: when the upsert updates, SQLite leaves last_insert_rowid
	// at whatever this connection inserted before.
	var id int64
	if err := db.QueryRow(`
		INSERT INTO files (path, parent_path, name, size, mtime, disk_mtime, mime_type, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(path) DO UPDATE SET
			size = excluded.size,
			mtime = excluded.mtime,
			disk_mtime = excluded.disk_mtime,
			mime_type = excluded.mime_type,
			etag = excluded.etag
		RETURNING id`,
		filePath, parentPath, name, size, now, diskMTime, mimeType, etag).Scan(&id); err != nil {
		return nil, fmt.Errorf("upsert file: %w", err)
	}

	// The row counts the bytes now; they leave the budget, and a file
	// smaller than what it replaced gives back the difference.
	body.Settle(size)
	w.Done()
	recorded = true

	p.logActivity(username, "file_created", username+" uploaded "+name, strconv.FormatInt(id, 10), filePath)
	return &FileMeta{
		ID:         id,
		Path:       filePath,
		ParentPath: parentPath,
		Name:       name,
		Size:       size,
		MTime:      now,
		MimeType:   mimeType,
		ETag:       etag,
	}, nil
}

func (p *Plugin) logActivity(username, typ, subject, objectID, objectName string) {
	if al := p.ctx.ActivityLog(); al != nil {
		al.Log(username, username, "files", typ, subject, "files", objectID, objectName)
	}
}

// Download opens a file for reading. The returned io.ReadCloser must be closed
// by the caller. For local backends the underlying value is an *os.File.
func (p *Plugin) Download(username, filePath string) (io.ReadCloser, *FileMeta, error) {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return nil, nil, err
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return nil, nil, err
	}
	rc, err := p.open(username, filePath)
	if err != nil {
		return nil, nil, err
	}
	// The size is the bytes being served, not the row's: they part when the
	// file changed outside the plugin (SFTP, another server on the same
	// data) and the row has not caught up, and a response promising the
	// row's size would end early.
	if st, ok := rc.(interface{ Stat() (iofs.FileInfo, error) }); ok {
		info, err := st.Stat()
		if err != nil {
			return nil, nil, errors.Join(fmt.Errorf("stat %s: %w", filePath, err), rc.Close())
		}
		meta.Size = info.Size()
	}
	return rc, meta, nil
}

// List returns files/directories within a given directory, performing
// lazy reconciliation between disk and metadata.
func (p *Plugin) List(username, dirPath string) ([]FileMeta, error) {
	return p.list(username, dirPath, false)
}

// list is List, or with dirsOnly just the folders.
func (p *Plugin) list(username, dirPath string, dirsOnly bool) ([]FileMeta, error) {
	dirPath = normalizePath(dirPath)

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	// Reconcile: scan disk and update metadata.
	if err := p.reconcile(db, username, dirPath); err != nil {
		p.log.Error("reconcile", "user", username, "dir", dirPath, "err", err)
	}

	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE parent_path = ? AND (is_dir = 1 OR NOT ?)
		ORDER BY is_dir DESC, name COLLATE NOCASE ASC, name ASC`, dirPath, dirsOnly)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()

	var files []FileMeta
	for rows.Next() {
		var f FileMeta
		if err := rows.Scan(&f.ID, &f.Path, &f.ParentPath, &f.Name,
			&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetByID returns file metadata by its stable ID.
func (p *Plugin) GetByID(username string, id int64) (*FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return getByID(db, id)
}

// GetByIDs returns file metadata for several ids in one query, keyed by id.
// Ids with no file are absent from the map.
func (p *Plugin) GetByIDs(username string, ids []int64) (map[int64]*FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return getByIDs(db, ids)
}

// GetByPath returns file metadata by path.
func (p *Plugin) GetByPath(username, path string) (*FileMeta, error) {
	path = normalizePath(path)
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return getByPath(db, path)
}

// Delete moves a file or directory to the trash.
// Use PermanentDelete to bypass the trash.
func (p *Plugin) Delete(username, filePath string) error {
	return p.Trash(username, filePath)
}

// HardDelete permanently removes a file or directory, bypassing the trash.
// Metadata deletion runs in a single transaction so a crash mid-delete leaves
// either the row intact or fully removed, not a stranded child record.
func (p *Plugin) HardDelete(username, filePath string) error {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return err
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return err
	}

	// Remove from disk via VFS first. A subsequent DB failure leaves an
	// orphan metadata row referencing a missing file — recoverable by lazy
	// reconcile — which is preferable to deleting metadata while the file
	// still exists on disk.
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	if err := fs.Delete(filePath); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin hard-delete tx: %w", err)
	}
	defer tx.Rollback()

	if err := forgetRows(tx, filePath); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hard-delete: %w", err)
	}
	p.logActivity(username, "file_deleted", username+" deleted "+filepath.Base(filePath), strconv.FormatInt(meta.ID, 10), filePath)
	return nil
}

// Rename moves/renames a file or directory, preserving its stable ID.
func (p *Plugin) Rename(username, oldPath, newPath string) (*FileMeta, error) {
	oldPath = normalizePath(oldPath)
	newPath = normalizePath(newPath)

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	// Rename on disk via VFS.
	fs, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	newParentDir := filepath.Dir(newPath)
	if err := fs.Mkdir(newParentDir); err != nil {
		return nil, fmt.Errorf("mkdir new parent: %w", err)
	}
	if err := fs.Rename(oldPath, newPath); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}

	newParent := filepath.Dir(newPath)
	newName := filepath.Base(newPath)
	now := p.ctx.Now().Unix()
	etag := generateETag()

	// Atomically rename the row and reparent any children. A crash between
	// the two statements would otherwise strand children under the old path.
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin rename tx: %w", err)
	}
	defer tx.Rollback()

	// What the rename replaced on disk is gone; its rows would collide.
	if newPath != oldPath {
		if err := forgetRows(tx, newPath); err != nil {
			return nil, err
		}
	}

	res, err := tx.Exec(`
		UPDATE files SET path = ?, parent_path = ?, name = ?, mtime = ?, etag = ?
		WHERE path = ?`,
		newPath, newParent, newName, now, etag, oldPath)
	if err != nil {
		return nil, fmt.Errorf("update file metadata: %w", err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("update file metadata: %w", err)
	}

	n, prefix := subtree(oldPath)
	if _, err := tx.Exec(`
		UPDATE files SET
			path = ? || substr(path, ?),
			parent_path = ? || substr(parent_path, ?)
		WHERE substr(path, 1, ?) = ?`,
		newPath, n,
		newPath, n,
		n, prefix); err != nil {
		return nil, fmt.Errorf("update children metadata: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit rename: %w", err)
	}

	p.logActivity(username, "file_changed", username+" renamed "+filepath.Base(oldPath)+" to "+newName, "", newPath)
	if moved == 0 {
		// Written outside the plugin and not yet reconciled: it has no row
		// to carry over, so it gets one now.
		return p.record(db, fs, newPath)
	}
	return p.GetByPath(username, newPath)
}

// ParentID returns the parent file ID for a given file ID.
// Used by the sharing system for hierarchical permission checks.
func (p *Plugin) ParentID(username string, fileID int64) (string, error) {
	db, err := p.DB(username)
	if err != nil {
		return "", err
	}

	meta, err := getByID(db, fileID)
	if err != nil {
		return "", err
	}

	if meta.ParentPath == "" || meta.Path == "/" {
		return "", nil
	}

	parent, err := getByPath(db, meta.ParentPath)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(parent.ID, 10), nil
}

// reconcile brings the rows of dirPath's children in step with what is on
// disk, for changes made outside the plugin: it adds rows for new entries,
// updates those whose file changed, and forgets those that are gone.
func (p *Plugin) reconcile(db *sql.DB, username, dirPath string) error {
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	vfsEntries, err := fs.List(dirPath)
	if err != nil {
		if errors.Is(err, vfs.ErrNotFound) || errors.Is(err, iofs.ErrNotExist) {
			return nil // directory may not exist yet
		}
		return fmt.Errorf("list %s: %w", dirPath, err)
	}

	// A file still being uploaded is not one yet.
	vfsEntries = slices.DeleteFunc(vfsEntries, func(e vfs.FileInfo) bool { return vfs.IsPartial(e.Name) })

	type row struct {
		size      int64
		isDir     bool
		diskMTime int64
	}
	rows, err := db.Query(`SELECT path, size, is_dir, disk_mtime FROM files WHERE parent_path = ?`, dirPath)
	if err != nil {
		return err
	}
	defer rows.Close()
	known := make(map[string]row)
	for rows.Next() {
		var path string
		var r row
		if err := rows.Scan(&path, &r.size, &r.isDir, &r.diskMTime); err != nil {
			return err
		}
		known[path] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, fi := range vfsEntries {
		childPath := filepath.Join(dirPath, fi.Name)
		r, ok := known[childPath]
		delete(known, childPath)
		diskMTime := fi.ModTime.Unix()
		switch {
		case ok && r.isDir != fi.IsDir:
			// Replaced by the other kind: nothing of the old one carries over.
			if err := forgetRows(db, childPath); err != nil {
				return err
			}
		case ok && !fi.IsDir && (r.size != fi.Size || r.diskMTime != 0 && r.diskMTime != diskMTime):
			if _, err := db.Exec(`UPDATE files SET size = ?, mtime = ?, disk_mtime = ?, etag = ? WHERE path = ?`,
				fi.Size, p.ctx.Now().Unix(), diskMTime, generateETag(), childPath); err != nil {
				return fmt.Errorf("reconcile %s: %w", childPath, err)
			}
			continue
		case ok && r.diskMTime != diskMTime:
			// Not known before, or a directory's: nothing a client sees.
			if _, err := db.Exec(`UPDATE files SET disk_mtime = ? WHERE path = ?`, diskMTime, childPath); err != nil {
				return fmt.Errorf("reconcile %s: %w", childPath, err)
			}
			continue
		case ok:
			continue
		}

		var size int64
		mimeType := ""
		if !fi.IsDir {
			size = fi.Size
			mimeType = detectMimeType(fi.Name)
		}
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO files (path, parent_path, name, size, mtime, disk_mtime, mime_type, etag, is_dir, favorite)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			childPath, dirPath, fi.Name, size, diskMTime, diskMTime, mimeType, generateETag(), boolToInt(fi.IsDir)); err != nil {
			return fmt.Errorf("reconcile %s: %w", childPath, err)
		}
	}

	// What has a row but is no longer on disk.
	for gone := range known {
		if err := forgetRows(db, gone); err != nil {
			return err
		}
	}
	return nil
}

func getByID(db *sql.DB, id int64) (*FileMeta, error) {
	var f FileMeta
	err := db.QueryRow(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE id = ?`, id).Scan(
		&f.ID, &f.Path, &f.ParentPath, &f.Name,
		&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: id %d", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("get file by id: %w", err)
	}
	return &f, nil
}

// getByIDs returns the files among ids that exist, keyed by ID.
func getByIDs(db *sql.DB, ids []int64) (map[int64]*FileMeta, error) {
	out := make(map[int64]*FileMeta, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get files by id: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f FileMeta
		if err := rows.Scan(&f.ID, &f.Path, &f.ParentPath, &f.Name,
			&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		out[f.ID] = &f
	}
	return out, rows.Err()
}

func getByPath(db *sql.DB, path string) (*FileMeta, error) {
	var f FileMeta
	err := db.QueryRow(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE path = ?`, path).Scan(
		&f.ID, &f.Path, &f.ParentPath, &f.Name,
		&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, fmt.Errorf("get file by path: %w", err)
	}
	return &f, nil
}

func normalizePath(p string) string {
	p = filepath.Clean("/" + p)
	if p == "." {
		return "/"
	}
	return p
}

func detectMimeType(name string) string {
	ext := filepath.Ext(name)
	if ext == "" {
		return "application/octet-stream"
	}
	t := mime.TypeByExtension(ext)
	if t == "" {
		return "application/octet-stream"
	}
	return t
}

func generateETag() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fall back to timestamp-based etag on entropy exhaustion.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RecentFiles returns the most recently modified non-directory files.
func RecentFiles(db *sql.DB, limit int) ([]FileMeta, error) {
	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE is_dir = 0
		ORDER BY mtime DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent files: %w", err)
	}
	defer rows.Close()

	var files []FileMeta
	for rows.Next() {
		var f FileMeta
		if err := rows.Scan(&f.ID, &f.Path, &f.ParentPath, &f.Name,
			&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// likeEscape quotes what LIKE … ESCAPE '\' would otherwise read as a
// wildcard or an escape, so a query matches the text typed.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func formatSize(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// countingReader wraps an io.Reader and counts bytes read.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// isTextMIME returns true for MIME types whose content should be indexed.
func isTextMIME(m string) bool {
	if strings.HasPrefix(m, "text/") {
		return true
	}
	switch m {
	case "application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/toml":
		return true
	}
	return false
}

// ResourceType returns the full resource type string for sharing.
func ResourceType(meta *FileMeta) string {
	if meta.IsDir {
		return PluginID + ":directory"
	}
	return PluginID + ":file"
}

// IDString returns the file ID as a string (for use in share records).
func IDString(meta *FileMeta) string {
	return strconv.FormatInt(meta.ID, 10)
}

// SetFavorite sets or clears the favorite flag for a file.
func (p *Plugin) SetFavorite(username, filePath string, fav bool) error {
	filePath = normalizePath(filePath)
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	val := 0
	if fav {
		val = 1
	}
	res, err := db.Exec(`UPDATE files SET favorite = ? WHERE path = ?`, val, filePath)
	if err != nil {
		return fmt.Errorf("set favorite: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set favorite rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, filePath)
	}
	return nil
}

// SetFavoriteByID sets or clears the favorite flag for a file by ID.
func (p *Plugin) SetFavoriteByID(username string, id int64, fav bool) error {
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	val := 0
	if fav {
		val = 1
	}
	res, err := db.Exec(`UPDATE files SET favorite = ? WHERE id = ?`, val, id)
	if err != nil {
		return fmt.Errorf("set favorite: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set favorite rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: id %d", ErrNotFound, id)
	}
	return nil
}

// Favorites returns all favorited files for a user.
func (p *Plugin) Favorites(username string) ([]FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE favorite = 1
		ORDER BY is_dir DESC, name COLLATE NOCASE ASC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("favorites: %w", err)
	}
	defer rows.Close()

	var files []FileMeta
	for rows.Next() {
		var f FileMeta
		if err := rows.Scan(&f.ID, &f.Path, &f.ParentPath, &f.Name,
			&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ExtractArchive extracts all entries from an archive into destDir.
// Returns the count of extracted entries.
func (p *Plugin) ExtractArchive(username, archivePath, destDir string) (int, error) {
	archivePath = normalizePath(archivePath)
	destDir = normalizePath(destDir)

	meta, err := p.GetByPath(username, archivePath)
	if err != nil {
		return 0, err
	}
	if !isArchiveFile(meta) {
		return 0, fmt.Errorf("not an archive: %s", archivePath)
	}

	format := ArchiveFormat(meta.Name)

	rc, _, err := p.Download(username, archivePath)
	if err != nil {
		return 0, fmt.Errorf("open archive: %w", err)
	}
	defer rc.Close()

	var ar archiveReader
	if f, ok := rc.(*os.File); ok {
		ar = f
	} else {
		// Buffer for non-seekable readers.
		data, err := io.ReadAll(rc)
		if err != nil {
			return 0, fmt.Errorf("read archive: %w", err)
		}
		ar = bytes.NewReader(data)
	}

	size := meta.Size

	// List all entries at all levels by iterating the archive directly.
	switch format {
	case "zip":
		return p.extractZip(username, ar, size, destDir)
	case "tar", "tar.gz", "tar.bz2":
		if _, err := ar.Seek(0, io.SeekStart); err != nil {
			return 0, fmt.Errorf("seek: %w", err)
		}
		return p.extractTar(username, ar, format, destDir)
	default:
		return 0, fmt.Errorf("unsupported format: %s", format)
	}
}

func (p *Plugin) extractZip(username string, ar archiveReader, size int64, destDir string) (int, error) {
	zr, err := zip.NewReader(ar, size)
	if err != nil {
		return 0, fmt.Errorf("open zip: %w", err)
	}

	count := 0
	for _, zf := range zr.File {
		entryPath := cleanInternalPath(zf.Name)
		if entryPath == "" || !isSafePath(entryPath) {
			continue
		}
		if zf.FileInfo().Mode()&os.ModeSymlink != 0 {
			continue
		}

		dest := filepath.Join(destDir, entryPath)
		if strings.HasSuffix(zf.Name, "/") || zf.FileInfo().IsDir() {
			if err := p.ensureDir(username, dest); err != nil {
				p.log.Error("extract mkdir", "path", dest, "err", err)
			}
			count++
			continue
		}

		// Ensure parent directory exists.
		parentDir := filepath.Dir(dest)
		if parentDir != destDir {
			if err := p.ensureDir(username, parentDir); err != nil {
				p.log.Error("extract mkdir", "path", parentDir, "err", err)
			}
		}

		rc, err := zf.Open()
		if err != nil {
			p.log.Error("extract open", "entry", entryPath, "err", err)
			continue
		}
		_, err = p.Upload(username, dest, rc)
		rc.Close()
		if err != nil {
			p.log.Error("extract upload", "path", dest, "err", err)
			continue
		}
		count++
	}
	return count, nil
}

func (p *Plugin) extractTar(username string, r io.ReadSeeker, format, destDir string) (int, error) {
	tr, err := wrapTarReader(r, format)
	if err != nil {
		return 0, err
	}

	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return count, fmt.Errorf("read tar: %w", err)
		}

		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeDir {
			continue
		}

		entryPath := cleanInternalPath(hdr.Name)
		if entryPath == "" || !isSafePath(entryPath) {
			continue
		}

		dest := filepath.Join(destDir, entryPath)
		if hdr.Typeflag == tar.TypeDir {
			if err := p.ensureDir(username, dest); err != nil {
				p.log.Error("extract mkdir", "path", dest, "err", err)
			}
			count++
			continue
		}

		parentDir := filepath.Dir(dest)
		if parentDir != destDir {
			if err := p.ensureDir(username, parentDir); err != nil {
				p.log.Error("extract mkdir", "path", parentDir, "err", err)
			}
		}

		if _, err := p.Upload(username, dest, tr); err != nil {
			p.log.Error("extract upload", "path", dest, "err", err)
			continue
		}
		count++
	}
	return count, nil
}

// QuotaCheck returns nil if the user has room for additionalBytes more, or
// cogplugin.ErrQuotaExceeded. Writers that stage bytes before they become a file
// (chunked uploads) ask here, so staging cannot hold more than the quota.
func (p *Plugin) QuotaCheck(username string, additionalBytes int64) error {
	return p.ctx.QuotaCheck(username, additionalBytes)
}

// Usage returns the bytes a user's files take, trashed ones included: they
// are still on disk.
func (p *Plugin) Usage(username string) (int64, error) {
	db, err := p.DB(username)
	if err != nil {
		return 0, err
	}
	var used int64
	err = db.QueryRow(`SELECT (SELECT coalesce(sum(size), 0) FROM files WHERE is_dir = 0)
		+ (SELECT coalesce(sum(size), 0) FROM trash)`).Scan(&used)
	if err != nil {
		return 0, fmt.Errorf("usage query: %w", err)
	}
	return used, nil
}

// Search returns files matching a simple name pattern.
func (p *Plugin) Search(username, query string) ([]FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	pattern := "%" + likeEscape(query) + "%"
	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE name LIKE ? ESCAPE '\'
		ORDER BY is_dir DESC, name COLLATE NOCASE ASC, name ASC
		LIMIT 50`, pattern)
	if err != nil {
		return nil, fmt.Errorf("search files: %w", err)
	}
	defer rows.Close()

	var results []FileMeta
	for rows.Next() {
		var f FileMeta
		if err := rows.Scan(&f.ID, &f.Path, &f.ParentPath, &f.Name,
			&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		results = append(results, f)
	}
	return results, rows.Err()
}
