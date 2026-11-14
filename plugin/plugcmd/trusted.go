package plugcmd

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/storage"
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

	// storage follows the host's layout and confinement. A trusted plugin
	// has no system database, so its writes are not counted toward quota.
	storage *storage.Store
}

// newTrustedRegistrar creates a trusted registrar if the trust env vars are set.
// Returns nil if not in trusted mode.
func newTrustedRegistrar(base *capturingRegistrar) (*trustedRegistrar, error) {
	if os.Getenv("COG_TRUSTED") != "1" {
		return nil, nil
	}
	dataDir := os.Getenv("COG_DATA_DIR")
	if dataDir == "" {
		return nil, nil
	}
	var csrfSecret []byte
	if encoded := os.Getenv("COG_CSRF_SECRET"); encoded != "" {
		var err error
		if csrfSecret, err = base64.StdEncoding.DecodeString(encoded); err != nil {
			return nil, fmt.Errorf("decode COG_CSRF_SECRET: %w", err)
		}
	}
	return &trustedRegistrar{
		capturingRegistrar: base,
		dataDir:            dataDir,
		csrfSecret:         csrfSecret,
		dbs:                make(map[string]*sql.DB),
		storage:            storage.New(dataDir, nil),
	}, nil
}

// openDB opens a database using the same layout, connection settings, and
// migrations the in-process host uses. A trusted plugin runs in its own
// process, so it cannot be handed the host's handle — sharing the
// implementation is the most that can be shared, and it is what keeps an
// out-of-process plugin from drifting into a different set of rules.
func (t *trustedRegistrar) openDB(path string, schema fs.FS, opts cogplugin.DBOptions) (*sql.DB, error) {
	if opts.ReadOnly || opts.NoHooks {
		return db.OpenPluginDB(path, schema, opts.ReadOnly)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if cached, ok := t.dbs[path]; ok {
		return cached, nil
	}

	sdb, err := db.OpenPluginDB(path, schema, false)
	if err != nil {
		return nil, err
	}

	t.dbs[path] = sdb
	return sdb, nil
}

func (t *trustedRegistrar) PluginDB(username string, opts ...cogplugin.DBOption) (*sql.DB, error) {
	o := cogplugin.ApplyDBOptions(opts)
	pluginID := t.pluginID
	if o.PluginID != "" {
		pluginID = o.PluginID
	}
	return t.openDB(db.PluginDBPath(t.dataDir, username, pluginID), t.schema, o)
}

func (t *trustedRegistrar) RootDB() (*sql.DB, error) {
	path := filepath.Join(t.dataDir, "app_data", t.pluginID, "root.db")
	return t.openDB(path, nil, cogplugin.DBOptions{})
}

func (t *trustedRegistrar) UserStorage(username string) (cogplugin.Storage, error) {
	return t.storage.Dir(storage.Counted, t.pluginID, username)
}

func (t *trustedRegistrar) UserCache(username string) (cogplugin.Storage, error) {
	return t.storage.Dir(storage.Cache, t.pluginID, username)
}

func (t *trustedRegistrar) GlobalStorage() (cogplugin.Storage, error) {
	return t.storage.Dir(storage.Global, t.pluginID, "")
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
	for key, db := range t.dbs {
		if err := db.Close(); err != nil {
			slog.Error("plugcmd: close plugin database", "db", key, "err", err)
		}
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
func (t *trustedRegistrar) BaseURL() string                      { return "" }

// QuotaRemaining reports no limit: quota is enforced host-side.
func (t *trustedRegistrar) QuotaRemaining(username string) (int64, error) { return -1, nil }
