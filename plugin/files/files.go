// Package files implements the core privileged files plugin.
// It manages file/directory metadata and provides the authoritative
// metadata layer for user files.
package files

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:embed sql
var sqlFS embed.FS

const PluginID = "files"

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
	ctx plugin.PluginContext
	log *slog.Logger
}

// New creates a new files plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()
	ctx.RegisterNavItem(plugin.NavItem{Label: "Files", URL: "/apps/files", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M22 19a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h5l2 3h9a2 2 0 0 1 2 2z"></path></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Files",
		Priority: 30,
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
		ContentFunc: func(filesDir string, cols map[string]string) string {
			if !isTextMIME(cols["mime_type"]) {
				return ""
			}
			path := cols["path"]
			if path == "" {
				return ""
			}
			fsPath := filepath.Join(filesDir, path)
			info, err := os.Stat(fsPath)
			if err != nil || info.Size() > 1<<20 { // skip >1MB
				return ""
			}
			data, err := os.ReadFile(fsPath)
			if err != nil {
				return ""
			}
			return string(data)
		},
	})

	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		p.registerRoutes(priv)
	}

	return nil
}

func (p *Plugin) registerRoutes(priv plugin.PrivilegedContext) {
	pages := NewPages(p)
	csrfSecret := p.ctx.CSRFSecret()
	csrfWrap := func(h http.Handler) http.Handler {
		return auth.RequireAuthWeb(auth.RequireCSRF(csrfSecret, h))
	}

	// Files browser UI
	priv.RegisterRoute("GET /apps/files/favorites", auth.RequireAuthWeb(http.HandlerFunc(pages.ServeFavorites)))
	priv.RegisterRoute("GET /apps/files/{path...}", auth.RequireAuthWeb(http.HandlerFunc(pages.ServeGet)))
	priv.RegisterRoute("POST /apps/files/{path...}", csrfWrap(http.HandlerFunc(pages.ServePost)))
	priv.RegisterRoute("DELETE /apps/files/{path...}", csrfWrap(http.HandlerFunc(pages.ServeDelete)))
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	files, _ := RecentFiles(db, 5)
	return recentFilesWidget(files).Render(ctx, w)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the files plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate files db for %s: %w", username, err)
	}
	return db, nil
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
		time.Now().Unix(), generateETag())
	return err
}

// CreateDir creates a directory.
func (p *Plugin) CreateDir(username, dirPath string) (*FileMeta, error) {
	dirPath = normalizePath(dirPath)
	parentPath := filepath.Dir(dirPath)
	name := filepath.Base(dirPath)

	// Create on disk
	fsPath := filepath.Join(p.ctx.UserFilesDir(username), dirPath)
	if err := os.MkdirAll(fsPath, 0755); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	etag := generateETag()

	res, err := db.Exec(`
		INSERT INTO files (path, parent_path, name, mtime, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, 1)`,
		dirPath, parentPath, name, now, etag)
	if err != nil {
		return nil, fmt.Errorf("insert dir: %w", err)
	}

	id, _ := res.LastInsertId()
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

// Upload writes a file to disk and records its metadata.
func (p *Plugin) Upload(username, filePath string, content io.Reader) (*FileMeta, error) {
	filePath = normalizePath(filePath)
	parentPath := filepath.Dir(filePath)
	name := filepath.Base(filePath)

	// Ensure parent dir exists on disk
	fsDir := filepath.Join(p.ctx.UserFilesDir(username), parentPath)
	if err := os.MkdirAll(fsDir, 0755); err != nil {
		return nil, fmt.Errorf("mkdir parent: %w", err)
	}

	// Write to disk
	fsPath := filepath.Join(p.ctx.UserFilesDir(username), filePath)
	f, err := os.Create(fsPath)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}

	size, err := io.Copy(f, content)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	etag := generateETag()
	mimeType := detectMimeType(name)

	// Upsert: if the path already exists, update it
	res, err := db.Exec(`
		INSERT INTO files (path, parent_path, name, size, mtime, mime_type, etag, is_dir)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0)
		ON CONFLICT(path) DO UPDATE SET
			size = excluded.size,
			mtime = excluded.mtime,
			mime_type = excluded.mime_type,
			etag = excluded.etag`,
		filePath, parentPath, name, size, now, mimeType, etag)
	if err != nil {
		return nil, fmt.Errorf("upsert file: %w", err)
	}

	id, _ := res.LastInsertId()
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

// Download opens a file for reading.
func (p *Plugin) Download(username, filePath string) (*os.File, *FileMeta, error) {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return nil, nil, err
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return nil, nil, err
	}

	fsPath := filepath.Join(p.ctx.UserFilesDir(username), filePath)
	f, err := os.Open(fsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open file: %w", err)
	}

	return f, meta, nil
}

// List returns files/directories within a given directory, performing
// lazy reconciliation between disk and metadata.
func (p *Plugin) List(username, dirPath string) ([]FileMeta, error) {
	dirPath = normalizePath(dirPath)

	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	// Reconcile: scan disk and update metadata
	fsPath := filepath.Join(p.ctx.UserFilesDir(username), dirPath)
	if err := p.reconcile(db, username, dirPath, fsPath); err != nil {
		p.log.Error("reconcile", "user", username, "dir", dirPath, "err", err)
	}

	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE parent_path = ?
		ORDER BY is_dir DESC, name ASC`, dirPath)
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

// GetByPath returns file metadata by path.
func (p *Plugin) GetByPath(username, path string) (*FileMeta, error) {
	path = normalizePath(path)
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return getByPath(db, path)
}

// Delete removes a file or directory.
func (p *Plugin) Delete(username, filePath string) error {
	filePath = normalizePath(filePath)

	db, err := p.DB(username)
	if err != nil {
		return err
	}

	meta, err := getByPath(db, filePath)
	if err != nil {
		return err
	}

	// Remove from disk
	fsPath := filepath.Join(p.ctx.UserFilesDir(username), filePath)
	if meta.IsDir {
		if err := os.RemoveAll(fsPath); err != nil {
			return fmt.Errorf("remove dir: %w", err)
		}
		// Remove all children from metadata
		if _, err := db.Exec(`DELETE FROM files WHERE path LIKE ? OR path = ?`,
			filePath+"/%", filePath); err != nil {
			return fmt.Errorf("delete dir metadata: %w", err)
		}
	} else {
		if err := os.Remove(fsPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove file: %w", err)
		}
		if _, err := db.Exec(`DELETE FROM files WHERE path = ?`, filePath); err != nil {
			return fmt.Errorf("delete file metadata: %w", err)
		}
	}
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

	oldFsPath := filepath.Join(p.ctx.UserFilesDir(username), oldPath)
	newFsPath := filepath.Join(p.ctx.UserFilesDir(username), newPath)

	// Ensure parent of new path exists
	if err := os.MkdirAll(filepath.Dir(newFsPath), 0755); err != nil {
		return nil, fmt.Errorf("mkdir new parent: %w", err)
	}

	if err := os.Rename(oldFsPath, newFsPath); err != nil {
		return nil, fmt.Errorf("rename: %w", err)
	}

	newParent := filepath.Dir(newPath)
	newName := filepath.Base(newPath)
	now := time.Now().Unix()
	etag := generateETag()

	_, err = db.Exec(`
		UPDATE files SET path = ?, parent_path = ?, name = ?, mtime = ?, etag = ?
		WHERE path = ?`,
		newPath, newParent, newName, now, etag, oldPath)
	if err != nil {
		return nil, fmt.Errorf("update file metadata: %w", err)
	}

	// Update children if directory
	_, err = db.Exec(`
		UPDATE files SET
			path = ? || substr(path, ?),
			parent_path = ? || substr(parent_path, ?)
		WHERE path LIKE ?`,
		newPath, len(oldPath)+1,
		newPath, len(oldPath)+1,
		oldPath+"/%")
	if err != nil {
		return nil, fmt.Errorf("update children metadata: %w", err)
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

// reconcile performs lazy synchronization between disk and metadata.
func (p *Plugin) reconcile(db *sql.DB, username, dirPath, fsPath string) error {
	entries, err := os.ReadDir(fsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	// Build set of known paths from metadata
	rows, err := db.Query(`SELECT path FROM files WHERE parent_path = ?`, dirPath)
	if err != nil {
		return err
	}
	defer rows.Close()

	known := make(map[string]bool)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		known[path] = true
	}

	// Add new entries found on disk
	for _, entry := range entries {
		childPath := filepath.Join(dirPath, entry.Name())
		if known[childPath] {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		etag := generateETag()
		isDir := 0
		if entry.IsDir() {
			isDir = 1
		}
		mimeType := ""
		if !entry.IsDir() {
			mimeType = detectMimeType(entry.Name())
		}

		_, err = db.Exec(`
			INSERT OR IGNORE INTO files (path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			childPath, dirPath, entry.Name(), info.Size(),
			info.ModTime().Unix(), mimeType, etag, isDir)
		if err != nil {
			p.log.Error("reconcile insert", "path", childPath, "err", err)
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
		return nil, fmt.Errorf("file not found: id %d", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get file by id: %w", err)
	}
	return &f, nil
}

func getByPath(db *sql.DB, path string) (*FileMeta, error) {
	var f FileMeta
	err := db.QueryRow(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE path = ?`, path).Scan(
		&f.ID, &f.Path, &f.ParentPath, &f.Name,
		&f.Size, &f.MTime, &f.MimeType, &f.ETag, &f.IsDir, &f.Favorite)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("file not found: %s", path)
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
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("file not found: %s", filePath)
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
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("file not found: id %d", id)
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
		ORDER BY is_dir DESC, name ASC`)
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

// Search returns files matching a simple name pattern.
func (p *Plugin) Search(username, query string) ([]FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}

	pattern := "%" + strings.ReplaceAll(query, "%", "\\%") + "%"
	rows, err := db.Query(`
		SELECT id, path, parent_path, name, size, mtime, mime_type, etag, is_dir, favorite
		FROM files WHERE name LIKE ? ESCAPE '\'
		ORDER BY is_dir DESC, name ASC
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
