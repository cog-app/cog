package files

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DAVLock is a WebDAV write lock (RFC 4918 §6). While it lasts, only a request
// that submits its token may change what it covers: its root, and with
// Infinite everything beneath. Locks are kept per user, beside the files.
type DAVLock struct {
	Token    string
	Path     string
	Infinite bool
	Shared   bool
	// Owner is the client's DAV:owner element, kept as sent to give back.
	Owner   string
	Timeout time.Duration
	Expires time.Time
}

// Covers reports whether the lock applies to p.
func (l *DAVLock) Covers(p string) bool {
	return l.Path == p || (l.Infinite && isBeneath(l.Path, p))
}

// isBeneath reports whether p is strictly inside dir.
func isBeneath(dir, p string) bool {
	_, prefix := subtree(dir)
	return strings.HasPrefix(p, prefix) && p != dir
}

var (
	// ErrLocked is a lock that conflicts with one already held.
	ErrLocked = errors.New("files: locked")
	// ErrNoLock is a token that names no current lock on the path.
	ErrNoLock = errors.New("files: no such lock")
)

// MaxLockTimeout bounds how long a lock lasts between refreshes, so one a
// client forgot does not hold a file for good. Clients that edit for longer
// refresh it.
const MaxLockTimeout = time.Hour

// DefaultLockTimeout is a lock's length when the client does not ask.
const DefaultLockTimeout = 30 * time.Minute

// Lock takes a new lock on l.Path, whether or not anything is there yet: a
// client locks a name before it first writes to it. An exclusive lock
// conflicts with any other covering the same files, a shared one only with
// exclusive ones: ErrLocked. The returned lock has its token and expiry
// filled in.
//
// It does not wait for writes in progress: one that began before the lock
// was checked against the locks there were then.
func (p *Plugin) Lock(username string, l DAVLock) (*DAVLock, error) {
	l.Path = normalizePath(l.Path)
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin lock: %w", err)
	}
	defer tx.Rollback()

	now := p.ctx.Now()
	if _, err := tx.Exec(`DELETE FROM dav_locks WHERE expires_at <= ?`, now.Unix()); err != nil {
		return nil, fmt.Errorf("expire locks: %w", err)
	}
	held, err := locksFor(tx, l.Path, l.Infinite, now)
	if err != nil {
		return nil, err
	}
	for _, h := range held {
		if !l.Shared || !h.Shared {
			return nil, fmt.Errorf("%w: %s by %s", ErrLocked, l.Path, h.Path)
		}
	}

	l.Token = "opaquelocktoken:" + uuid.NewString()
	l.Timeout = clampLockTimeout(l.Timeout)
	l.Expires = now.Add(l.Timeout)
	if _, err := tx.Exec(`INSERT INTO dav_locks (token, path, infinite, shared, owner, timeout, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		l.Token, l.Path, l.Infinite, l.Shared, l.Owner, int64(l.Timeout/time.Second), l.Expires.Unix()); err != nil {
		return nil, fmt.Errorf("insert lock: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit lock: %w", err)
	}
	return &l, nil
}

// RefreshLock restarts the timeout of the lock token on a path it covers.
func (p *Plugin) RefreshLock(username, filePath, token string, timeout time.Duration) (*DAVLock, error) {
	filePath = normalizePath(filePath)
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	now := p.ctx.Now()
	l, err := lockByToken(db, token, now)
	if err != nil {
		return nil, err
	}
	if !l.Covers(filePath) {
		return nil, ErrNoLock
	}
	l.Timeout = clampLockTimeout(timeout)
	l.Expires = now.Add(l.Timeout)
	if _, err := db.Exec(`UPDATE dav_locks SET timeout = ?, expires_at = ? WHERE token = ?`,
		int64(l.Timeout/time.Second), l.Expires.Unix(), token); err != nil {
		return nil, fmt.Errorf("refresh lock: %w", err)
	}
	return l, nil
}

// Unlock removes the lock token, which must cover filePath.
func (p *Plugin) Unlock(username, filePath, token string) error {
	filePath = normalizePath(filePath)
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	l, err := lockByToken(db, token, p.ctx.Now())
	if err != nil {
		return err
	}
	if !l.Covers(filePath) {
		return ErrNoLock
	}
	if _, err := db.Exec(`DELETE FROM dav_locks WHERE token = ?`, token); err != nil {
		return fmt.Errorf("unlock: %w", err)
	}
	return nil
}

// LocksFor returns the current locks that cover filePath: on it, and on the
// folders above it with infinite depth. With beneath, it adds the locks on
// anything inside it, which a change to the whole folder (delete, move,
// listing their lock state) must reckon with.
func (p *Plugin) LocksFor(username, filePath string, beneath bool) ([]DAVLock, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	return locksFor(db, normalizePath(filePath), beneath, p.ctx.Now())
}

func locksFor(q querier, filePath string, beneath bool, now time.Time) ([]DAVLock, error) {
	var above []string
	for dir := filePath; dir != "/"; {
		dir = path.Dir(dir)
		above = append(above, dir)
	}
	aboveJSON, err := json.Marshal(above)
	if err != nil {
		return nil, fmt.Errorf("encode paths: %w", err)
	}
	n, prefix := subtree(filePath)
	rows, err := q.Query(`SELECT token, path, infinite, shared, owner, timeout, expires_at FROM dav_locks
		WHERE expires_at > ? AND (path = ?
			OR (infinite AND path IN (SELECT value FROM json_each(?)))
			OR (? AND substr(path, 1, ?) = ?))
		ORDER BY path, token`,
		now.Unix(), filePath, string(aboveJSON), beneath, n, prefix)
	if err != nil {
		return nil, fmt.Errorf("list locks: %w", err)
	}
	defer rows.Close()
	var out []DAVLock
	for rows.Next() {
		l, err := scanLock(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func lockByToken(q querier, token string, now time.Time) (*DAVLock, error) {
	l, err := scanLock(q.QueryRow(`SELECT token, path, infinite, shared, owner, timeout, expires_at FROM dav_locks
		WHERE token = ? AND expires_at > ?`, token, now.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoLock
	}
	return l, err
}

func scanLock(row interface{ Scan(...any) error }) (*DAVLock, error) {
	var l DAVLock
	var timeout, expires int64
	if err := row.Scan(&l.Token, &l.Path, &l.Infinite, &l.Shared, &l.Owner, &timeout, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("scan lock: %w", err)
	}
	l.Timeout = time.Duration(timeout) * time.Second
	l.Expires = time.Unix(expires, 0)
	return &l, nil
}

func clampLockTimeout(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return DefaultLockTimeout
	case d > MaxLockTimeout:
		return MaxLockTimeout
	}
	return d
}

// forgetLocks drops the locks on path and everything beneath it: a lock
// covers what is at a path, and once that is gone, or moved, a new file
// there is not locked.
func forgetLocks(db execer, filePath string) error {
	n, prefix := subtree(filePath)
	if _, err := db.Exec(`DELETE FROM dav_locks WHERE path = ? OR substr(path, 1, ?) = ?`, filePath, n, prefix); err != nil {
		return fmt.Errorf("forget locks on %s: %w", filePath, err)
	}
	return nil
}

// querier is a database or a transaction.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// A Precondition decides whether a write may go ahead, given what is at the
// path now (nil when nothing is). It runs while the path is held against
// other writes through this plugin, so what it saw is what the write
// replaces; its error is returned as the write's.
type Precondition func(current *FileMeta) error

// UploadIf is Upload when pre allows it.
func (p *Plugin) UploadIf(username, filePath string, content io.Reader, pre Precondition) (*FileMeta, error) {
	return p.upload(username, filePath, content, pre)
}

// DeleteIf is Delete when pre allows it.
func (p *Plugin) DeleteIf(username, filePath string, pre Precondition) error {
	filePath = normalizePath(filePath)
	defer p.lockPath(username, filePath)()
	if err := p.checkPrecondition(username, filePath, pre); err != nil {
		return err
	}
	return p.Trash(username, filePath)
}

// RenameIf is Rename when pre allows it, given what is at the old path and
// what is at the new one.
func (p *Plugin) RenameIf(username, oldPath, newPath string, pre func(src, dst *FileMeta) error) (*FileMeta, error) {
	oldPath, newPath = normalizePath(oldPath), normalizePath(newPath)
	// Always in the same order, so two renames between the same paths
	// cannot each hold one and wait for the other.
	first, second := oldPath, newPath
	if second < first {
		first, second = second, first
	}
	defer p.lockPath(username, first)()
	if second != first {
		defer p.lockPath(username, second)()
	}
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	src, err := currentMeta(db, oldPath)
	if err != nil {
		return nil, err
	}
	dst, err := currentMeta(db, newPath)
	if err != nil {
		return nil, err
	}
	if err := pre(src, dst); err != nil {
		return nil, err
	}
	return p.Rename(username, oldPath, newPath)
}

func (p *Plugin) checkPrecondition(username, filePath string, pre Precondition) error {
	if pre == nil {
		return nil
	}
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	cur, err := currentMeta(db, filePath)
	if err != nil {
		return err
	}
	return pre(cur)
}

// currentMeta is the row at path, or nil when there is none.
func currentMeta(db *sql.DB, filePath string) (*FileMeta, error) {
	m, err := getByPath(db, filePath)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return m, err
}
