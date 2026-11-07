package migrate_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// Every migration runs on every open, so one that adds a column must say
// when not to: without "-- IF COLUMN" (or "-- ONCE") the first open adds the
// column and every later one fails adding it again. A test opens a database
// once, so nothing else catches it before a restart does.
func TestAddColumnMigrationsAreGuarded(t *testing.T) {
	root := os.DirFS("../..")
	n := 0
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != "." && (strings.HasPrefix(name, ".") || name == "tmp" || name == "testdata") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(d.Name(), "migration") || !strings.HasSuffix(d.Name(), ".sql") {
			return nil
		}
		data, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		body := strings.ToUpper(string(data))
		if !strings.Contains(body, "ADD COLUMN") {
			return nil
		}
		n++
		if !strings.Contains(body, "-- IF COLUMN ") && !strings.Contains(body, "-- ONCE") {
			t.Errorf("%s adds a column with no -- IF COLUMN or -- ONCE guard", path)
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, n > 0, "no migrations found")
}
