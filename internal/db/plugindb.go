package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/migrate"
)

// PluginDBPath returns where a plugin's per-user database lives.
//
// This is the only place that knows the layout. It is not a detail plugins
// should reproduce: an out-of-process plugin that builds the path itself will
// keep working right up until the database stops being a local file.
func PluginDBPath(dataDir, username, pluginID string) string {
	return filepath.Join(dataDir, username, "app_data", pluginID, "plugin.db")
}

// OpenPluginDB opens a per-user plugin database, creating its directory,
// applying the schema, and returning a handle ready for use.
//
// schemaFS may be nil for callers with no schema of their own, such as a
// read-only reader of another plugin's database.
func OpenPluginDB(path string, schemaFS fs.FS, readOnly bool, hooks ...ConnectHook) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create plugin data dir: %w", err)
	}

	var (
		sdb *sql.DB
		err error
	)
	if readOnly {
		sdb, err = OpenWithHooksMode(path, "ro", hooks...)
	} else {
		sdb, err = OpenWithHooks(path, hooks...)
	}
	if err != nil {
		return nil, fmt.Errorf("open plugin db %s: %w", path, err)
	}

	// Migrate before the handle escapes, so no caller can observe an
	// unmigrated database. A read-only handle cannot migrate and does not
	// need to: whoever owns the database has already done it.
	if schemaFS != nil && !readOnly {
		if err := migrate.Migrate(sdb, schemaFS); err != nil {
			return nil, errors.Join(fmt.Errorf("migrate plugin db %s: %w", path, err), sdb.Close())
		}
	}

	return sdb, nil
}
