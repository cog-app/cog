package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"time"
)

// touchEvery is how stale a cache entry's modification time may get before
// a hit refreshes it: often enough for the sweep, rarely enough that reading
// a thumbnail is not also a write each time.
const touchEvery = 24 * time.Hour

// SetClock sets what the store takes the time from (the sweep, and a cache
// hit's refresh). Without it, the system clock.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = now
}

func (s *Store) now() time.Time {
	s.mu.Lock()
	now := s.clock
	s.mu.Unlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

// touch marks the cache entry n used at now, unless it was within touchEvery.
func touch(r *os.Root, n string, now time.Time) error {
	info, err := r.Stat(n)
	if err != nil {
		return err
	}
	if now.Sub(info.ModTime()) < touchEvery {
		return nil
	}
	if err := r.Chtimes(n, now, now); err != nil {
		return fmt.Errorf("storage: touch %s: %w", n, err)
	}
	return nil
}

// SweepCaches removes what every user's caches hold that has not been used
// for maxAge (made, or read, as touch marks it), and the directories that
// leaves empty. Cached files are derived, so whatever is still wanted is made
// again. It returns how many files it removed.
func (s *Store) SweepCaches(maxAge time.Duration) (int, error) {
	cutoff := s.now().Add(-maxAge)
	data, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return 0, fmt.Errorf("storage: open data dir: %w", err)
	}
	defer data.Close()

	caches, err := cacheDirs(data)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, dir := range caches {
		n, err := sweep(data, dir, cutoff)
		removed += n
		if err != nil {
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

// cacheDirs finds every user's caches, <user>/app_data/<plugin>/cache.
//
// It opens directories and nothing else. fs.Glob would try every entry of
// the data directory as one, and the system database may live there: on
// Linux, opening and closing a database file outside SQLite drops every
// lock this process holds on it. Another process's SQLite then believes it
// is the last connection, checkpoints and deletes the -wal and -shm files
// this one still has open, and every query after fails with "disk I/O
// error: no such file or directory".
func cacheDirs(data *os.Root) ([]string, error) {
	var dirs []string
	users, err := fs.ReadDir(data.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("storage: find caches: %w", err)
	}
	for _, u := range users {
		if !u.IsDir() {
			continue
		}
		appData := path.Join(u.Name(), "app_data")
		plugins, err := fs.ReadDir(data.FS(), appData)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("storage: find caches: %w", err)
		}
		for _, pl := range plugins {
			if !pl.IsDir() {
				continue
			}
			dir := path.Join(appData, pl.Name(), Cache.dir())
			info, err := data.Lstat(dir)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("storage: find caches: %w", err)
			}
			if info.IsDir() {
				dirs = append(dirs, dir)
			}
		}
	}
	return dirs, nil
}

// sweep removes the files under dir last modified before cutoff, then the
// directories under it left empty.
func sweep(data *os.Root, dir string, cutoff time.Time) (int, error) {
	r, err := data.OpenRoot(dir)
	if err != nil {
		return 0, fmt.Errorf("storage: open %s: %w", dir, err)
	}
	defer r.Close()

	removed := 0
	var dirs []string
	err = fs.WalkDir(r.FS(), ".", func(p string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // removed since it was listed
		}
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p != "." {
				dirs = append(dirs, p)
			}
			return nil
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := r.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, fmt.Errorf("storage: sweep %s: %w", dir, err)
	}
	// Deepest first, so a parent is empty by the time it is tried. One still
	// holding something stays.
	for _, d := range slices.Backward(dirs) {
		if err := r.Remove(d); err != nil && !errors.Is(err, fs.ErrNotExist) && !isNotEmpty(err) {
			return removed, fmt.Errorf("storage: sweep %s: %w", dir, err)
		}
	}
	return removed, nil
}
