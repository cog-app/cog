package mount

import (
	"fmt"
	"time"
)

// GroupMount represents a mount shared with all members of a group.
type GroupMount struct {
	ID            string
	GroupID       string
	UserPath      string
	VolumeID      string
	VolumeSubpath string
	ReadOnly      bool
	CreatedAt     time.Time
}

// CreateGroupMount creates a new group mount.
func (s *Store) CreateGroupMount(groupID, userPath, volumeID, volumeSubpath string, readOnly bool) (*GroupMount, error) {
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
		`INSERT INTO group_mounts (id, group_id, user_path, volume_id, volume_subpath, read_only, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, groupID, userPath, volumeID, volumeSubpath, ro, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert group mount: %w", err)
	}

	return &GroupMount{
		ID:            id,
		GroupID:       groupID,
		UserPath:      userPath,
		VolumeID:      volumeID,
		VolumeSubpath: volumeSubpath,
		ReadOnly:      readOnly,
		CreatedAt:     time.Unix(now, 0),
	}, nil
}

// ListGroupMounts returns all mounts for a group.
func (s *Store) ListGroupMounts(groupID string) ([]GroupMount, error) {
	rows, err := s.db.Query(
		`SELECT id, group_id, user_path, volume_id, volume_subpath, read_only, created_at
		FROM group_mounts WHERE group_id = ? ORDER BY user_path`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("list group mounts: %w", err)
	}
	defer rows.Close()

	var mounts []GroupMount
	for rows.Next() {
		var m GroupMount
		var createdAt int64
		var ro int
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserPath, &m.VolumeID, &m.VolumeSubpath, &ro, &createdAt); err != nil {
			return nil, fmt.Errorf("scan group mount: %w", err)
		}
		m.ReadOnly = ro != 0
		m.CreatedAt = time.Unix(createdAt, 0)
		mounts = append(mounts, m)
	}
	return mounts, rows.Err()
}

// DeleteGroupMount removes a group mount by ID.
func (s *Store) DeleteGroupMount(id string) error {
	res, err := s.db.Exec(`DELETE FROM group_mounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group mount: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListGroupMountsForUser returns all group mounts accessible to a user via their group memberships.
func (s *Store) ListGroupMountsForUser(userID string) ([]GroupMount, error) {
	rows, err := s.db.Query(`
		SELECT gm.id, gm.group_id, gm.user_path, gm.volume_id, gm.volume_subpath, gm.read_only, gm.created_at
		FROM group_mounts gm
		JOIN group_membership_closure gmc ON gm.group_id = gmc.ancestor_group_id
		WHERE gmc.descendant_user_id = ?
		ORDER BY gm.user_path`, userID)
	if err != nil {
		return nil, fmt.Errorf("list group mounts for user: %w", err)
	}
	defer rows.Close()

	var mounts []GroupMount
	for rows.Next() {
		var m GroupMount
		var createdAt int64
		var ro int
		if err := rows.Scan(&m.ID, &m.GroupID, &m.UserPath, &m.VolumeID, &m.VolumeSubpath, &ro, &createdAt); err != nil {
			return nil, fmt.Errorf("scan group mount: %w", err)
		}
		m.ReadOnly = ro != 0
		m.CreatedAt = time.Unix(createdAt, 0)
		mounts = append(mounts, m)
	}
	return mounts, rows.Err()
}
