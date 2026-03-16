// Package quota manages volume storage quotas.
package quota

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// Quota represents a storage quota for a volume.
type Quota struct {
	ID          string
	VolumeID    string
	SubjectType string // USER, GROUP, or DEFAULT
	SubjectID   string
	QuotaBytes  int64
	CreatedAt   int64
	UpdatedAt   int64
}

// Store provides CRUD operations for volume quotas.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// NewStore creates a new quota store.
func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

// Set upserts a quota for a volume/subject combination.
func (s *Store) Set(volumeID, subjectType, subjectID string, quotaBytes int64) error {
	now := s.clock().Unix()
	id := generateID()
	_, err := s.db.Exec(`
		INSERT INTO volume_quotas (id, volume_id, subject_type, subject_id, quota_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(volume_id, subject_type, subject_id) DO UPDATE SET
			quota_bytes = excluded.quota_bytes,
			updated_at = excluded.updated_at`,
		id, volumeID, subjectType, subjectID, quotaBytes, now, now)
	if err != nil {
		return fmt.Errorf("set quota: %w", err)
	}
	return nil
}

// Get returns a specific quota by volume/subject.
func (s *Store) Get(volumeID, subjectType, subjectID string) (*Quota, error) {
	var q Quota
	err := s.db.QueryRow(`
		SELECT id, volume_id, subject_type, subject_id, quota_bytes, created_at, updated_at
		FROM volume_quotas
		WHERE volume_id = ? AND subject_type = ? AND subject_id = ?`,
		volumeID, subjectType, subjectID).Scan(
		&q.ID, &q.VolumeID, &q.SubjectType, &q.SubjectID, &q.QuotaBytes, &q.CreatedAt, &q.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get quota: %w", err)
	}
	return &q, nil
}

// Delete removes a quota by ID.
func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM volume_quotas WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete quota: %w", err)
	}
	return nil
}

// ListByVolume returns all quotas for a volume.
func (s *Store) ListByVolume(volumeID string) iter.Seq2[Quota, error] {
	return func(yield func(Quota, error) bool) {
		rows, err := s.db.Query(`
			SELECT id, volume_id, subject_type, subject_id, quota_bytes, created_at, updated_at
			FROM volume_quotas
			WHERE volume_id = ?
			ORDER BY subject_type, subject_id`, volumeID)
		if err != nil {
			yield(Quota{}, fmt.Errorf("list quotas: %w", err))
			return
		}
		defer rows.Close()

		for rows.Next() {
			var q Quota
			if err := rows.Scan(&q.ID, &q.VolumeID, &q.SubjectType, &q.SubjectID, &q.QuotaBytes, &q.CreatedAt, &q.UpdatedAt); err != nil {
				yield(Quota{}, fmt.Errorf("scan quota: %w", err))
				return
			}
			if !yield(q, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Quota{}, err)
		}
	}
}

// EffectiveQuota resolves the effective quota for a user on a volume.
// Resolution order: USER > GROUP > DEFAULT > -1 (unlimited).
func (s *Store) EffectiveQuota(userID, volumeID string, groupIDs []string) int64 {
	// Check user-specific quota.
	var bytes int64
	err := s.db.QueryRow(`
		SELECT quota_bytes FROM volume_quotas
		WHERE volume_id = ? AND subject_type = 'USER' AND subject_id = ?`,
		volumeID, userID).Scan(&bytes)
	if err == nil {
		return bytes
	}

	// Check group quotas — pick the largest.
	if len(groupIDs) > 0 {
		var maxGroupQuota int64 = -1
		found := false
		for _, gid := range groupIDs {
			err := s.db.QueryRow(`
				SELECT quota_bytes FROM volume_quotas
				WHERE volume_id = ? AND subject_type = 'GROUP' AND subject_id = ?`,
				volumeID, gid).Scan(&bytes)
			if err == nil {
				found = true
				if bytes > maxGroupQuota {
					maxGroupQuota = bytes
				}
			}
		}
		if found {
			return maxGroupQuota
		}
	}

	// Check default quota.
	err = s.db.QueryRow(`
		SELECT quota_bytes FROM volume_quotas
		WHERE volume_id = ? AND subject_type = 'DEFAULT' AND subject_id = ''`,
		volumeID).Scan(&bytes)
	if err == nil {
		return bytes
	}

	return -1 // unlimited
}

// FormatBytes formats a byte count into a human-readable string.
func FormatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
