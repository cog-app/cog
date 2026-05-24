package plugcmd

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	_ "github.com/mattn/go-sqlite3"
)

// trustedRegistrar wraps capturingRegistrar with real DB/filesystem access.
// It is used when the plugin process is launched with COG_TRUSTED=1.
type trustedRegistrar struct {
	*capturingRegistrar
	dataDir    string
	csrfSecret []byte

	mu  sync.Mutex
	dbs map[string]*sql.DB // cached DB connections keyed by path
}

// newTrustedRegistrar creates a trusted registrar if the trust env vars are set.
// Returns nil if not in trusted mode.
func newTrustedRegistrar(base *capturingRegistrar) *trustedRegistrar {
	if os.Getenv("COG_TRUSTED") != "1" {
		return nil
	}
	dataDir := os.Getenv("COG_DATA_DIR")
	if dataDir == "" {
		return nil
	}
	var csrfSecret []byte
	if encoded := os.Getenv("COG_CSRF_SECRET"); encoded != "" {
		csrfSecret, _ = base64.StdEncoding.DecodeString(encoded)
	}
	return &trustedRegistrar{
		capturingRegistrar: base,
		dataDir:            dataDir,
		csrfSecret:         csrfSecret,
		dbs:                make(map[string]*sql.DB),
	}
}

func (t *trustedRegistrar) openDB(path string) (*sql.DB, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if db, ok := t.dbs[path]; ok {
		return db, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db %s: %w", path, err)
	}

	t.dbs[path] = db
	return db, nil
}

func (t *trustedRegistrar) PluginDB(username string) (*sql.DB, error) {
	path := filepath.Join(t.dataDir, username, "app_data", t.pluginID, "plugin.db")
	db, err := t.openDB(path)
	if err != nil {
		return nil, err
	}
	return db, nil
}

func (t *trustedRegistrar) RootDB() (*sql.DB, error) {
	path := filepath.Join(t.dataDir, "app_data", t.pluginID, "root.db")
	return t.openDB(path)
}

func (t *trustedRegistrar) UserFilesDir(username string) string {
	return filepath.Join(t.dataDir, username, "files")
}

func (t *trustedRegistrar) DataDir() string {
	return t.dataDir
}

func (t *trustedRegistrar) CSRFSecret() []byte {
	return t.csrfSecret
}

func (t *trustedRegistrar) Logger() *slog.Logger {
	return slog.Default().With("plugin", t.pluginID)
}

func (t *trustedRegistrar) EnsureUserDirs(username string) error {
	dirs := []string{
		filepath.Join(t.dataDir, username, "files"),
		filepath.Join(t.dataDir, username, "app_data", t.pluginID),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all cached DB connections.
func (t *trustedRegistrar) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, db := range t.dbs {
		db.Close()
	}
	t.dbs = make(map[string]*sql.DB)
}

// QuotaCheck is a no-op for trusted plugins (quota enforcement is host-side).
func (t *trustedRegistrar) QuotaCheck(username string, additionalBytes int64) error {
	return nil
}

// Service stubs — trusted plugins that need these can access the system DB directly.
func (t *trustedRegistrar) ShareStore() cogplugin.ShareStore     { return nil }
func (t *trustedRegistrar) EmailSender() cogplugin.EmailSender   { return nil }
func (t *trustedRegistrar) UserLookup() cogplugin.UserLookup     { return nil }
func (t *trustedRegistrar) GroupStore() cogplugin.GroupStore     { return nil }
func (t *trustedRegistrar) FilesAccess() cogplugin.FilesAccess   { return nil }
func (t *trustedRegistrar) VolumeAccess() cogplugin.VolumeAccess { return nil }
func (t *trustedRegistrar) Cookies() cogplugin.CookieConfig      { return cogplugin.CookieConfig{} }
