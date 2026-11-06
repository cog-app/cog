package storage_test

import (
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/quota/budget"
	"git.duckfam.us/jj-software/cog/internal/storage"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func newStore(t *testing.T, users ...string) (*storage.Store, string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	sys, err := db.OpenSystem(filepath.Join(dir, "system.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sys.Close() })
	for _, u := range users {
		_, err := sys.Exec(`INSERT INTO users (id, username, password_hash, home_mount, created_at) VALUES (?, ?, '', '', 0)`, "id-"+u, u)
		require.NoError(t, err)
	}
	return storage.New(dir, sys), dir, sys
}

func read(t *testing.T, s plugin.Storage, name string) string {
	t.Helper()
	f, err := s.Open(name)
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(b)
}

// TestCounted: a user's storage counts what Put writes and Remove frees,
// and a Put over quota leaves the old file in place.
func TestCounted(t *testing.T) {
	s, _, _ := newStore(t, "alice")
	limit := int64(10)
	s.SetQuota(budget.New(func(username string) (int64, error) {
		used, err := s.Usage(username)
		return max(limit-used, 0), err
	}).Begin)
	d, err := s.Dir(storage.Counted, "notes", "alice")
	require.NoError(t, err)

	n, err := d.Put("a/one.txt", strings.NewReader("123456"))
	require.NoError(t, err)
	require.Equal(t, int64(6), n)
	used, err := s.Usage("alice")
	require.NoError(t, err)
	require.Equal(t, int64(6), used)

	// Replacing counts the difference, not both files.
	_, err = d.Put("a/one.txt", strings.NewReader("12345678"))
	require.NoError(t, err)
	used, _ = s.Usage("alice")
	require.Equal(t, int64(8), used)

	_, err = d.Put("a/one.txt", strings.NewReader("12345678901"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "over-quota put: %v", err)
	require.Equal(t, "12345678", read(t, d, "a/one.txt"))
	entries, err := d.ReadDir("a")
	require.NoError(t, err)
	require.Equal(t, 1, len(entries)) // no temporary file left behind

	require.NoError(t, d.Remove("a/one.txt"))
	used, _ = s.Usage("alice")
	require.Equal(t, int64(0), used)

	_, err = d.Put("b/c/two.txt", strings.NewReader("12"))
	require.NoError(t, err)
	_, err = d.Put("b/three.txt", strings.NewReader("123"))
	require.NoError(t, err)
	require.NoError(t, d.RemoveAll("b"))
	used, _ = s.Usage("alice")
	require.Equal(t, int64(0), used)
	require.NoError(t, d.Remove("missing"))
}

// TestUncounted: caches and global storage take no quota.
func TestUncounted(t *testing.T) {
	s, _, _ := newStore(t, "alice")
	s.SetQuota(budget.New(func(string) (int64, error) { return 0, nil }).Begin)
	c, err := s.Dir(storage.Cache, "gallery", "alice")
	require.NoError(t, err)
	_, err = c.Put("thumb.jpg", strings.NewReader("x"))
	require.NoError(t, err)
	g, err := s.Dir(storage.Global, "gallery", "")
	require.NoError(t, err)
	_, err = g.Put("model.onnx", strings.NewReader("x"))
	require.NoError(t, err)
	used, err := s.Usage("alice")
	require.NoError(t, err)
	require.Equal(t, int64(0), used)
}

// TestConfined: no name, and no symlink in the directory, reaches outside it.
func TestConfined(t *testing.T) {
	s, dataDir, _ := newStore(t, "alice", "bob")
	bob, err := s.Dir(storage.Counted, "notes", "bob")
	require.NoError(t, err)
	_, err = bob.Put("secret.txt", strings.NewReader("bob's"))
	require.NoError(t, err)

	d, err := s.Dir(storage.Counted, "notes", "alice")
	require.NoError(t, err)
	for _, n := range []string{"../../../bob/app_data/notes/storage/secret.txt", "/etc/passwd", "a/../../x", ""} {
		_, err := d.Open(n)
		require.True(t, err != nil, "opened %q", n)
		_, err = d.Put(n, strings.NewReader("x"))
		require.True(t, err != nil, "wrote %q", n)
	}

	aliceDir, err := d.LocalPath(".")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(filepath.Join(dataDir, "bob", "app_data", "notes", "storage"), filepath.Join(aliceDir, "link")))
	_, err = d.Open("link/secret.txt")
	require.True(t, err != nil, "followed a symlink out")
	_, err = d.Put("link/planted.txt", strings.NewReader("x"))
	require.True(t, err != nil, "wrote through a symlink")
	_, err = d.LocalPath("link")
	require.True(t, err != nil, "handed out a symlink")

	for _, bad := range []string{"", "..", "a/b"} {
		_, err := s.Dir(storage.Counted, bad, "alice")
		require.True(t, err != nil, "plugin %q", bad)
		_, err = s.Dir(storage.Counted, "notes", bad)
		require.True(t, err != nil, "user %q", bad)
	}
}

// TestMigrate: files a plugin kept at its old location move into its
// storage on first use and are counted; cached copies are dropped.
func TestMigrate(t *testing.T) {
	s, dataDir, sys := newStore(t, "alice")
	old := filepath.Join(dataDir, "contacts", "alice", "photos")
	require.NoError(t, os.MkdirAll(old, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(old, "abc"), []byte("12345"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(old, "u-def"), []byte("cached"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "alice"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "alice", "profanity.txt"), []byte("darn\n"), 0o600))

	d, err := s.Dir(storage.Counted, "contacts", "alice")
	require.NoError(t, err)
	require.Equal(t, "12345", read(t, d, "photos/abc"))
	_, err = d.Stat("photos/u-def")
	require.True(t, errors.Is(err, fs.ErrNotExist), "cached copy kept: %v", err)
	_, err = os.Stat(filepath.Join(dataDir, "contacts", "alice"))
	require.True(t, errors.Is(err, fs.ErrNotExist), "old directory left: %v", err)

	p, err := s.Dir(storage.Counted, "profanity", "alice")
	require.NoError(t, err)
	require.Equal(t, "darn\n", read(t, p, "words.txt"))

	used, err := s.Usage("alice")
	require.NoError(t, err)
	require.Equal(t, int64(10), used)

	// A second store (a restart) neither moves nor counts again.
	s2 := storage.New(dataDir, sys)
	_, err = s2.Dir(storage.Counted, "contacts", "alice")
	require.NoError(t, err)
	used, err = s2.Usage("alice")
	require.NoError(t, err)
	require.Equal(t, int64(10), used)
}

// TestTake: a file a program wrote into the cache becomes the user's,
// counted, and one that would take them over quota stays where it was.
func TestTake(t *testing.T) {
	s, _, _ := newStore(t, "alice")
	limit := int64(10)
	s.SetQuota(budget.New(func(username string) (int64, error) {
		used, err := s.Usage(username)
		return max(limit-used, 0), err
	}).Begin)
	cache, err := s.Dir(storage.Cache, "feed", "alice")
	require.NoError(t, err)
	own, err := s.Dir(storage.Counted, "feed", "alice")
	require.NoError(t, err)

	out, err := cache.LocalPath("downloads/1.part")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(out, []byte("123456"), 0o600))
	n, err := own.Take("media/1.mp3", cache, "downloads/1.part")
	require.NoError(t, err)
	require.Equal(t, int64(6), n)
	require.Equal(t, "123456", read(t, own, "media/1.mp3"))
	_, err = cache.Stat("downloads/1.part")
	require.True(t, errors.Is(err, fs.ErrNotExist))
	used, _ := s.Usage("alice")
	require.Equal(t, int64(6), used)

	require.NoError(t, os.WriteFile(out, []byte("12345"), 0o600))
	_, err = own.Take("media/2.mp3", cache, "downloads/1.part")
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "over quota: %v", err)
	_, err = cache.Stat("downloads/1.part")
	require.NoError(t, err)

	// Replacing counts the difference.
	_, err = own.Take("media/1.mp3", cache, "downloads/1.part")
	require.NoError(t, err)
	used, _ = s.Usage("alice")
	require.Equal(t, int64(5), used)

	// From another plugin's directory it copies.
	other, err := s.Dir(storage.Cache, "gallery", "alice")
	require.NoError(t, err)
	_, err = other.Put("x", strings.NewReader("12"))
	require.NoError(t, err)
	_, err = own.Take("media/x", other, "x")
	require.NoError(t, err)
	used, _ = s.Usage("alice")
	require.Equal(t, int64(7), used)
	_, err = other.Stat("x")
	require.True(t, errors.Is(err, fs.ErrNotExist))
}
