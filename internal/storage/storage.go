// Package storage is where plugins keep files outside their databases. It
// is the only package that knows where in the data directory those files
// live: a plugin asks for a [plugin.Storage] and names files within it.
//
// Every operation goes through an os.Root on the storage directory, so no
// name, and no symlink inside, reaches a file outside it. A user's storage
// is counted toward their quota: storage_usage holds the bytes each plugin
// keeps for each user, updated by every Put and Remove, and a Put that would
// take the user over their quota is refused before it replaces anything.
package storage

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"

	"git.duckfam.us/jj-software/cog/internal/quota/budget"
	"git.duckfam.us/jj-software/cog/plugin"
)

// Kind is what a directory holds, which decides whether it is counted.
type Kind int

const (
	// Counted is a user's storage: it counts toward their quota.
	Counted Kind = iota
	// Cache is a user's cache: derived files the plugin can make again,
	// which are not counted.
	Cache
	// Global is a plugin's own storage, belonging to no user.
	Global
	// Host is a directory the server itself keeps at the top of the data
	// directory, such as avatars; its "plugin ID" is the directory's name.
	Host
)

func (k Kind) dir() string {
	switch k {
	case Cache:
		return "cache"
	default:
		return "storage"
	}
}

// BeginFunc starts a write for username, drawing on the budget their writes
// in progress share (see budget).
type BeginFunc func(username string) (*budget.Write, error)

// Store hands out plugin storage under a data directory.
type Store struct {
	dataDir string
	db      *sql.DB
	quota   BeginFunc
	gone    func(username string) (bool, error)

	mu    sync.Mutex
	ready map[readyKey]bool        // directories migrated and counted
	locks map[readyKey]*sync.Mutex // serialises a user's counted writes per plugin
}

type readyKey struct {
	kind     Kind
	pluginID string
	username string
}

// New returns a Store for dataDir, counting usage in the system database.
// Without one (a trusted plugin process, which has none) nothing is counted.
func New(dataDir string, db *sql.DB) *Store {
	return &Store{
		dataDir: dataDir,
		db:      db,
		ready:   map[readyKey]bool{},
		locks:   map[readyKey]*sync.Mutex{},
	}
}

// SetQuota sets where a counted write draws its growth from. Without it,
// writes are counted but not limited.
func (s *Store) SetQuota(q BeginFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quota = q
}

// rel is where a directory lives, relative to the data directory.
func rel(kind Kind, pluginID, username string) string {
	if kind == Host {
		return pluginID
	}
	if kind == Global {
		return filepath.Join("app_data", pluginID, kind.dir())
	}
	return filepath.Join(username, "app_data", pluginID, kind.dir())
}

// Dir returns a plugin's directory of the given kind for username (ignored
// for Global). The first use in a process moves files the plugin kept at its
// old location, and counts a user's storage if it has not been yet.
func (s *Store) Dir(kind Kind, pluginID, username string) (*Dir, error) {
	if !validID(pluginID) || (kind != Global && kind != Host && !validID(username)) {
		return nil, fmt.Errorf("storage: invalid plugin %q or user %q", pluginID, username)
	}
	d := &Dir{s: s, kind: kind, pluginID: pluginID, username: username, rel: rel(kind, pluginID, username)}
	if err := d.refuseGone(); err != nil {
		return nil, err
	}
	if err := s.prepare(d); err != nil {
		return nil, err
	}
	return d, nil
}

// validID refuses what would make an ID more than one path element.
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && path.Base(id) == id
}

func (s *Store) prepare(d *Dir) error {
	key := readyKey{d.kind, d.pluginID, d.username}
	s.mu.Lock()
	ready := s.ready[key]
	s.mu.Unlock()
	if ready {
		return nil
	}
	if err := s.migrate(d); err != nil {
		return err
	}
	if d.kind == Counted {
		if err := s.count(d); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.ready[key] = true
	s.mu.Unlock()
	return nil
}

// count records what a user's directory holds, if nothing has been recorded:
// files from before usage was counted, or moved from an old location.
func (s *Store) count(d *Dir) error {
	if s.db == nil {
		return nil
	}
	var known bool
	err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM storage_usage u JOIN users ON users.id = u.user_id
		WHERE users.username = ? AND u.plugin_id = ?)`, d.username, d.pluginID).Scan(&known)
	if err != nil {
		return fmt.Errorf("storage: usage of %s/%s: %w", d.username, d.pluginID, err)
	}
	if known {
		return nil
	}
	var n int64
	err = d.with(func(r *os.Root) error {
		return fs.WalkDir(r.FS(), ".", func(_ string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			n += info.Size()
			return nil
		})
	})
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("storage: count %s: %w", d.rel, err)
	}
	// A user with no account (a test's, or one being deleted) has nothing
	// to count against; the insert then matches no row.
	_, err = s.db.Exec(`INSERT INTO storage_usage (user_id, plugin_id, bytes)
		SELECT id, ?, ? FROM users WHERE username = ? ON CONFLICT DO NOTHING`, d.pluginID, n, d.username)
	if err != nil {
		return fmt.Errorf("storage: record usage of %s/%s: %w", d.username, d.pluginID, err)
	}
	return nil
}

// add changes a user's recorded usage by delta.
func (s *Store) add(d *Dir, delta int64) error {
	if delta == 0 || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO storage_usage (user_id, plugin_id, bytes)
		SELECT id, ?, max(?, 0) FROM users WHERE username = ?
		ON CONFLICT (user_id, plugin_id) DO UPDATE SET bytes = max(bytes + ?, 0)`,
		d.pluginID, delta, d.username, delta)
	if err != nil {
		return fmt.Errorf("storage: record usage of %s/%s: %w", d.username, d.pluginID, err)
	}
	return nil
}

// Usage returns the bytes every plugin keeps for username.
func (s *Store) Usage(username string) (int64, error) {
	if s.db == nil {
		return 0, nil
	}
	var n int64
	err := s.db.QueryRow(`SELECT coalesce(sum(u.bytes), 0) FROM storage_usage u JOIN users ON users.id = u.user_id
		WHERE users.username = ?`, username).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage usage of %s: %w", username, err)
	}
	return n, nil
}

// RemoveUser removes everything kept for a deleted account under the data
// directory: its own directory, which holds its files, databases and plugin
// storage, and what plugins in pluginIDs kept for it under their own
// directories before they had storage. Its usage rows go with the account.
func (s *Store) RemoveUser(username string, pluginIDs ...string) error {
	if !validID(username) {
		return fmt.Errorf("storage: invalid user %q", username)
	}
	s.forget(username)
	data, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return fmt.Errorf("storage: open data dir: %w", err)
	}
	defer data.Close()
	dirs := []string{username}
	for _, id := range pluginIDs {
		if validID(id) {
			dirs = append(dirs, filepath.Join(id, username))
		}
	}
	var errs []error
	for _, dir := range dirs {
		if err := data.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

// forget drops what the store remembers of username, whose directories and
// usage rows are being deleted with their account: a new account given the
// name starts from nothing.
func (s *Store) forget(username string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.ready {
		if k.username == username {
			delete(s.ready, k)
		}
	}
	for k := range s.locks {
		if k.username == username {
			delete(s.locks, k)
		}
	}
}

func (s *Store) lock(d *Dir) func() {
	key := readyKey{d.kind, d.pluginID, d.username}
	s.mu.Lock()
	m := s.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		s.locks[key] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// SetGone sets how the store learns that a user's account was deleted:
// their directories are being removed, so none is made again.
func (s *Store) SetGone(gone func(username string) (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone = gone
}

// ErrUserGone is a user whose account was deleted.
var ErrUserGone = errors.New("the account was deleted")

// refuseGone fails for a user's directory whose account was deleted.
func (d *Dir) refuseGone() error {
	if d.kind != Counted && d.kind != Cache {
		return nil
	}
	d.s.mu.Lock()
	gone := d.s.gone
	d.s.mu.Unlock()
	if gone == nil {
		return nil
	}
	deleted, err := gone(d.username)
	if err != nil {
		return err
	}
	if deleted {
		return fmt.Errorf("storage of %s: %w", d.username, ErrUserGone)
	}
	return nil
}

// begin starts a write to d: a counted one draws on its user's budget, and
// any other is not limited.
func (s *Store) begin(d *Dir) (*budget.Write, error) {
	s.mu.Lock()
	q := s.quota
	s.mu.Unlock()
	if q == nil || d.kind != Counted {
		return new(budget.Write), nil
	}
	return q(d.username)
}

// Dir is one plugin directory. It implements plugin.Storage.
type Dir struct {
	s        *Store
	kind     Kind
	pluginID string
	username string
	rel      string
}

var _ plugin.Storage = (*Dir)(nil)

// with runs fn on the directory's root, creating the directory. The root is
// opened for each operation rather than held: there is one directory per
// plugin per user, and holding each open would hold that many descriptors.
func (d *Dir) with(fn func(r *os.Root) error) error {
	// A handle taken before the account was deleted must not make its
	// directory again.
	if err := d.refuseGone(); err != nil {
		return err
	}
	data, err := os.OpenRoot(d.s.dataDir)
	if err != nil {
		return fmt.Errorf("storage: open data dir: %w", err)
	}
	defer data.Close()
	if err := data.MkdirAll(d.rel, 0o700); err != nil {
		return fmt.Errorf("storage: create %s: %w", d.rel, err)
	}
	r, err := data.OpenRoot(d.rel)
	if err != nil {
		return fmt.Errorf("storage: open %s: %w", d.rel, err)
	}
	return errors.Join(fn(r), r.Close())
}

func name(n string) (string, error) {
	if !fs.ValidPath(n) {
		return "", fmt.Errorf("storage: invalid name %q: %w", n, fs.ErrInvalid)
	}
	return filepath.FromSlash(n), nil
}

// Open implements plugin.Storage.
func (d *Dir) Open(n string) (plugin.StoredFile, error) {
	n, err := name(n)
	if err != nil {
		return nil, err
	}
	var f *os.File
	err = d.with(func(r *os.Root) (err error) {
		f, err = r.Open(n)
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Put implements plugin.Storage. A counted Put writes the new bytes beside
// the old file, held to the quota as they arrive, and only then replaces it,
// so a write over quota leaves the old file as it was.
func (d *Dir) Put(n string, content io.Reader) (int64, error) {
	n, err := name(n)
	if err != nil {
		return 0, err
	}
	if n == "." {
		return 0, fmt.Errorf("storage: put: %w", fs.ErrInvalid)
	}
	if d.kind == Counted {
		defer d.s.lock(d)()
	}
	w, err := d.s.begin(d)
	if err != nil {
		return 0, err
	}
	var written int64
	err = d.with(func(r *os.Root) error {
		if err := r.MkdirAll(filepath.Dir(n), 0o700); err != nil {
			return fmt.Errorf("storage: create parent of %s: %w", n, err)
		}
		// The lock keeps the file as it is until the rename.
		var old int64
		if info, err := r.Stat(n); err == nil {
			old = info.Size()
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storage: stat %s: %w", n, err)
		}
		var suffix [8]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return err
		}
		tmp := filepath.Join(filepath.Dir(n), "."+filepath.Base(n)+"."+hex.EncodeToString(suffix[:])+".tmp")
		f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("storage: create %s: %w", n, err)
		}
		body := w.Reader(content, old)
		written, err = io.Copy(f, body)
		if err = errors.Join(err, f.Close()); err != nil {
			if errors.Is(err, plugin.ErrQuotaExceeded) {
				return errors.Join(err, r.Remove(tmp))
			}
			return errors.Join(fmt.Errorf("storage: write %s: %w", n, err), r.Remove(tmp))
		}
		body.Settle(written)
		if err := r.Rename(tmp, n); err != nil {
			return errors.Join(fmt.Errorf("storage: replace %s: %w", n, err), r.Remove(tmp))
		}
		if d.kind == Counted {
			return d.s.add(d, written-old)
		}
		return nil
	})
	if err != nil {
		w.Abandon()
		return 0, err
	}
	w.Done()
	return written, nil
}

// Take implements plugin.Storage. Between one plugin's directories for one
// user it renames; from anything else it copies, then removes.
func (d *Dir) Take(n string, from plugin.Storage, fromName string) (int64, error) {
	src, ok := from.(*Dir)
	if !ok || src.s.dataDir != d.s.dataDir || src.pluginID != d.pluginID || src.username != d.username {
		return d.copyFrom(n, from, fromName)
	}
	n, err := name(n)
	if err != nil {
		return 0, err
	}
	fn, err := name(fromName)
	if err != nil {
		return 0, err
	}
	if n == "." || fn == "." {
		return 0, fmt.Errorf("storage: take: %w", fs.ErrInvalid)
	}
	if err := d.refuseGone(); err != nil {
		return 0, err
	}
	if d.kind == Counted {
		defer d.s.lock(d)()
	}
	data, err := os.OpenRoot(d.s.dataDir)
	if err != nil {
		return 0, fmt.Errorf("storage: open data dir: %w", err)
	}
	defer data.Close()
	srcRoot, err := data.OpenRoot(src.rel)
	if err != nil {
		return 0, fmt.Errorf("storage: open %s: %w", src.rel, err)
	}
	defer srcRoot.Close()
	info, err := srcRoot.Lstat(fn)
	if err != nil {
		return 0, fmt.Errorf("storage: take %s: %w", fromName, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("storage: take %s: not a regular file", fromName)
	}
	if err := data.MkdirAll(filepath.Join(d.rel, filepath.Dir(n)), 0o700); err != nil {
		return 0, fmt.Errorf("storage: create parent of %s: %w", n, err)
	}
	dstRoot, err := data.OpenRoot(d.rel)
	if err != nil {
		return 0, fmt.Errorf("storage: open %s: %w", d.rel, err)
	}
	defer dstRoot.Close()
	var old int64
	if prev, err := dstRoot.Lstat(n); err == nil {
		if prev.Mode().IsRegular() {
			old = prev.Size()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("storage: stat %s: %w", n, err)
	}
	w, err := d.s.begin(d)
	if err != nil {
		return 0, err
	}
	if err := w.Grow(info.Size() - old); err != nil {
		w.Abandon()
		return 0, err
	}
	// Recorded below, or not taken at all.
	defer w.Done()
	// Both names were checked within their roots above; the rename is
	// made between the two directories on the data dir's root, which no
	// name here can leave.
	if err := data.Rename(filepath.Join(src.rel, fn), filepath.Join(d.rel, n)); err != nil {
		w.Abandon()
		return 0, fmt.Errorf("storage: take %s: %w", fromName, err)
	}
	if src.kind == Counted {
		if err := src.s.add(src, -info.Size()); err != nil {
			return 0, err
		}
	}
	if d.kind == Counted {
		if err := d.s.add(d, info.Size()-old); err != nil {
			return 0, err
		}
	}
	return info.Size(), nil
}

func (d *Dir) copyFrom(n string, from plugin.Storage, fromName string) (int64, error) {
	f, err := from.Open(fromName)
	if err != nil {
		return 0, err
	}
	written, err := d.Put(n, f)
	if err = errors.Join(err, f.Close()); err != nil {
		return 0, err
	}
	return written, from.Remove(fromName)
}

// Remove implements plugin.Storage.
func (d *Dir) Remove(n string) error {
	n, err := name(n)
	if err != nil {
		return err
	}
	if n == "." {
		return fmt.Errorf("storage: remove: %w", fs.ErrInvalid)
	}
	if d.kind == Counted {
		defer d.s.lock(d)()
	}
	return d.with(func(r *os.Root) error {
		info, err := r.Lstat(n)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("storage: remove %s: %w", n, err)
		}
		if err := r.Remove(n); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storage: remove %s: %w", n, err)
		}
		if d.kind == Counted && info.Mode().IsRegular() {
			return d.s.add(d, -info.Size())
		}
		return nil
	})
}

// RemoveAll implements plugin.Storage.
func (d *Dir) RemoveAll(n string) error {
	n, err := name(n)
	if err != nil {
		return err
	}
	if d.kind == Counted {
		defer d.s.lock(d)()
	}
	return d.with(func(r *os.Root) error {
		var size int64
		if d.kind == Counted {
			err := fs.WalkDir(r.FS(), filepath.ToSlash(n), func(_ string, e fs.DirEntry, err error) error {
				if err != nil || !e.Type().IsRegular() {
					return err
				}
				info, err := e.Info()
				if err != nil {
					return err
				}
				size += info.Size()
				return nil
			})
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("storage: remove %s: %w", n, err)
			}
		}
		if n == "." {
			entries, err := fs.ReadDir(r.FS(), ".")
			if err != nil {
				return fmt.Errorf("storage: remove all: %w", err)
			}
			for _, e := range entries {
				if err := r.RemoveAll(e.Name()); err != nil {
					return fmt.Errorf("storage: remove %s: %w", e.Name(), err)
				}
			}
		} else if err := r.RemoveAll(n); err != nil {
			return fmt.Errorf("storage: remove %s: %w", n, err)
		}
		if d.kind == Counted {
			return d.s.add(d, -size)
		}
		return nil
	})
}

// Stat implements plugin.Storage.
func (d *Dir) Stat(n string) (fs.FileInfo, error) {
	n, err := name(n)
	if err != nil {
		return nil, err
	}
	var info fs.FileInfo
	err = d.with(func(r *os.Root) (err error) {
		info, err = r.Stat(n)
		return err
	})
	return info, err
}

// ReadDir implements plugin.Storage.
func (d *Dir) ReadDir(n string) ([]fs.DirEntry, error) {
	if _, err := name(n); err != nil {
		return nil, err
	}
	var entries []fs.DirEntry
	err := d.with(func(r *os.Root) (err error) {
		entries, err = fs.ReadDir(r.FS(), n)
		return err
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// LocalPath implements plugin.Storage. A symlink at name is refused, since
// the program given the path would follow it wherever it leads.
func (d *Dir) LocalPath(n string) (string, error) {
	n, err := name(n)
	if err != nil {
		return "", err
	}
	err = d.with(func(r *os.Root) error {
		if err := r.MkdirAll(filepath.Dir(n), 0o700); err != nil {
			return fmt.Errorf("storage: create parent of %s: %w", n, err)
		}
		info, err := r.Lstat(n)
		if err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("storage: %s is a symlink", n)
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("storage: stat %s: %w", n, err)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(d.s.dataDir, d.rel, n))
	if err != nil {
		return "", fmt.Errorf("storage: path of %s: %w", n, err)
	}
	return abs, nil
}
