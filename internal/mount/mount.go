// Package mount provides per-user mount management for mapping virtual paths to volumes.
package mount

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

var ErrNotFound = errors.New("mount not found")

// Kind is what a mount puts in a user's tree.
type Kind string

const (
	// Volume mounts VolumeSubpath of a volume, as a share of it allows.
	Volume Kind = "volume"
	// Area mounts the user's own area of a storage volume, at VolumeSubpath.
	Area Kind = "area"
	// Share mounts a folder someone shared with the user: the directory
	// ShareResourceID of ShareOwnerID's files.
	Share Kind = "share"
)

type Mount struct {
	ID            string
	UserID        string
	UserPath      string
	Kind          Kind
	VolumeID      string // empty for a Share
	VolumeSubpath string
	// ShareOwnerID and ShareResourceID name the shared directory of a Share.
	ShareOwnerID    string
	ShareResourceID string
	ReadOnly        bool
	CreatedAt       time.Time
}

// mountColumns are the columns scanMount reads, in order.
const mountColumns = `id, user_id, user_path, kind, coalesce(volume_id, ''), volume_subpath,
	coalesce(share_owner_id, ''), coalesce(share_resource_id, ''), read_only, created_at`

type scanner interface{ Scan(dest ...any) error }

func scanMount(row scanner) (Mount, error) {
	var m Mount
	var createdAt int64
	if err := row.Scan(&m.ID, &m.UserID, &m.UserPath, &m.Kind, &m.VolumeID, &m.VolumeSubpath,
		&m.ShareOwnerID, &m.ShareResourceID, &m.ReadOnly, &createdAt); err != nil {
		return Mount{}, err
	}
	m.CreatedAt = time.Unix(createdAt, 0)
	return m, nil
}

type Store struct {
	db    *sql.DB
	clock clock.Clock
	// Changed runs after a mount is made or removed, for whoever keeps
	// something built from the mounts.
	Changed []func()
}

func (s *Store) changed() {
	for _, f := range s.Changed {
		f()
	}
}

func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

// Create mounts volumeSubpath of a volume at userPath.
func (s *Store) Create(userID, userPath, volumeID, volumeSubpath string, readOnly bool) (*Mount, error) {
	return s.insert(Mount{UserID: userID, UserPath: userPath, Kind: Volume, VolumeID: volumeID,
		VolumeSubpath: volumeSubpath, ReadOnly: readOnly})
}

// CreateShare mounts the directory resourceID of ownerID's files, shared
// with userID, at userPath. What it may do is decided by the shares each
// time it is used.
func (s *Store) CreateShare(userID, userPath, ownerID, resourceID string) (*Mount, error) {
	return s.insert(Mount{UserID: userID, UserPath: userPath, Kind: Share,
		ShareOwnerID: ownerID, ShareResourceID: resourceID})
}

// CreateArea mounts an area of the storage volume volumeID at userPath,
// at dir (<username>/<name>) in the volume. An area dir no mount holds is
// reattached — the files of an area unmounted before are still there. One
// another area holds, or that holds or is held by one, is taken, and the
// name gets a suffix: Photos-2, Photos-3.
func (s *Store) CreateArea(userID, userPath, volumeID, dir string) (*Mount, error) {
	var taken []string
	rows, err := s.db.Query(`SELECT volume_subpath FROM mounts WHERE volume_id = ? AND kind = 'area'`, volumeID)
	if err != nil {
		return nil, fmt.Errorf("list mounts of volume: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sub string
		if err := rows.Scan(&sub); err != nil {
			return nil, fmt.Errorf("scan mount: %w", err)
		}
		taken = append(taken, strings.Trim(sub, "/"))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	overlaps := func(c string) bool {
		for _, t := range taken {
			// An area converted from a mount of the whole volume holds
			// everything; it cannot be stepped around, so it is not counted.
			if t != "" && (t == c || strings.HasPrefix(c, t+"/") || strings.HasPrefix(t, c+"/")) {
				return true
			}
		}
		return false
	}
	dir = strings.Trim(dir, "/")
	cand := dir
	for i := 2; overlaps(cand); i++ {
		cand = dir + "-" + strconv.Itoa(i)
	}
	return s.insert(Mount{UserID: userID, UserPath: userPath, Kind: Area, VolumeID: volumeID, VolumeSubpath: cand})
}

func (s *Store) insert(m Mount) (*Mount, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	now := s.clock().Unix()
	_, err = s.db.Exec(
		`INSERT INTO mounts (id, user_id, user_path, kind, volume_id, volume_subpath, share_owner_id, share_resource_id, read_only, created_at)
		VALUES (?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?)`,
		id, m.UserID, m.UserPath, m.Kind, m.VolumeID, m.VolumeSubpath, m.ShareOwnerID, m.ShareResourceID, m.ReadOnly, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert mount: %w", err)
	}
	s.changed()
	m.ID = id
	m.CreatedAt = time.Unix(now, 0)
	return &m, nil
}

func (s *Store) GetByID(id string) (*Mount, error) {
	m, err := scanMount(s.db.QueryRow(`SELECT `+mountColumns+` FROM mounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mount: %w", err)
	}
	return &m, nil
}

// GetByUserPath returns the user's mount at userPath.
func (s *Store) GetByUserPath(userID, userPath string) (*Mount, error) {
	m, err := scanMount(s.db.QueryRow(`SELECT `+mountColumns+` FROM mounts WHERE user_id = ? AND user_path = ?`, userID, userPath))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mount at %s: %w", userPath, err)
	}
	return &m, nil
}

func (s *Store) ListByUser(userID string) iter.Seq2[Mount, error] {
	return func(yield func(Mount, error) bool) {
		rows, err := s.db.Query(`SELECT `+mountColumns+` FROM mounts WHERE user_id = ? ORDER BY user_path`, userID)
		if err != nil {
			yield(Mount{}, fmt.Errorf("list mounts: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			m, err := scanMount(rows)
			if err != nil {
				yield(Mount{}, fmt.Errorf("scan mount: %w", err))
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Mount{}, err)
		}
	}
}

// GetUserRoot returns the root mount (user_path="/") for a user.
func (s *Store) GetUserRoot(userID string) (*Mount, error) {
	m, err := s.GetByUserPath(userID, "/")
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("get user root mount: %w", err)
	}
	return m, err
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM mounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete mount: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.changed()
	return nil
}

// Move moves the user's mount at oldPath, and every mount beneath it, to
// newPath, reporting how many moved.
func (s *Store) Move(userID, oldPath, newPath string) (int64, error) {
	prefix := oldPath + "/"
	n := utf8.RuneCountInString(prefix)
	res, err := s.db.Exec(`UPDATE mounts SET user_path = ? || substr(user_path, ?)
		WHERE user_id = ? AND (user_path = ? OR substr(user_path, 1, ?) = ?)`,
		newPath, utf8.RuneCountInString(oldPath)+1, userID, oldPath, n, prefix)
	if err != nil {
		return 0, fmt.Errorf("move mounts at %s: %w", oldPath, err)
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	if moved > 0 {
		s.changed()
	}
	return moved, nil
}

func (s *Store) DeleteByUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM mounts WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user mounts: %w", err)
	}
	s.changed()
	return nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
