// Package mount provides per-user mount management for mapping virtual paths to volumes.
package mount

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var ErrNotFound = errors.New("mount not found")

type Mount struct {
	ID             string
	UserID         string
	UserPath       string
	VolumeID       string
	VolumeSubpath  string
	ReadOnly       bool
	CreatedAt      time.Time
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(userID, userPath, volumeID, volumeSubpath string, readOnly bool) (*Mount, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	ro := 0
	if readOnly {
		ro = 1
	}

	_, err = s.db.Exec(
		`INSERT INTO mounts (id, user_id, user_path, volume_id, volume_subpath, read_only, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, userID, userPath, volumeID, volumeSubpath, ro, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert mount: %w", err)
	}

	return &Mount{
		ID:            id,
		UserID:        userID,
		UserPath:      userPath,
		VolumeID:      volumeID,
		VolumeSubpath: volumeSubpath,
		ReadOnly:      readOnly,
		CreatedAt:     time.Unix(now, 0),
	}, nil
}

func (s *Store) GetByID(id string) (*Mount, error) {
	var m Mount
	var createdAt int64
	var ro int
	err := s.db.QueryRow(
		`SELECT id, user_id, user_path, volume_id, volume_subpath, read_only, created_at FROM mounts WHERE id = ?`, id,
	).Scan(&m.ID, &m.UserID, &m.UserPath, &m.VolumeID, &m.VolumeSubpath, &ro, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mount: %w", err)
	}
	m.ReadOnly = ro != 0
	m.CreatedAt = time.Unix(createdAt, 0)
	return &m, nil
}

func (s *Store) ListByUser(userID string) ([]Mount, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, user_path, volume_id, volume_subpath, read_only, created_at FROM mounts WHERE user_id = ? ORDER BY user_path`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mounts: %w", err)
	}
	defer rows.Close()

	var mounts []Mount
	for rows.Next() {
		var m Mount
		var createdAt int64
		var ro int
		if err := rows.Scan(&m.ID, &m.UserID, &m.UserPath, &m.VolumeID, &m.VolumeSubpath, &ro, &createdAt); err != nil {
			return nil, fmt.Errorf("scan mount: %w", err)
		}
		m.ReadOnly = ro != 0
		m.CreatedAt = time.Unix(createdAt, 0)
		mounts = append(mounts, m)
	}
	return mounts, rows.Err()
}

// GetUserRoot returns the root mount (user_path="/") for a user.
func (s *Store) GetUserRoot(userID string) (*Mount, error) {
	var m Mount
	var createdAt int64
	var ro int
	err := s.db.QueryRow(
		`SELECT id, user_id, user_path, volume_id, volume_subpath, read_only, created_at FROM mounts WHERE user_id = ? AND user_path = '/'`,
		userID,
	).Scan(&m.ID, &m.UserID, &m.UserPath, &m.VolumeID, &m.VolumeSubpath, &ro, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user root mount: %w", err)
	}
	m.ReadOnly = ro != 0
	m.CreatedAt = time.Unix(createdAt, 0)
	return &m, nil
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM mounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete mount: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteByUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM mounts WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete user mounts: %w", err)
	}
	return nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
