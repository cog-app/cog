// Package quota manages volume storage quotas.
package quota

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strings"

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
// Resolution order: USER > GROUP (the largest of the user's groups) >
// DEFAULT > -1 (unlimited). One query covers every level.
func (s *Store) EffectiveQuota(userID, volumeID string, groupIDs []string) (int64, error) {
	args := make([]any, 0, len(groupIDs)+2)
	args = append(args, volumeID, userID)
	groups := "0" // no groups: the GROUP arm matches nothing
	if len(groupIDs) > 0 {
		groups = "subject_id IN (?" + strings.Repeat(", ?", len(groupIDs)-1) + ")"
		for _, g := range groupIDs {
			args = append(args, g)
		}
	}
	var bytes int64
	err := s.db.QueryRow(`
		SELECT quota_bytes FROM volume_quotas
		WHERE volume_id = ? AND (
			(subject_type = 'USER' AND subject_id = ?)
			OR (subject_type = 'GROUP' AND `+groups+`)
			OR (subject_type = 'DEFAULT' AND subject_id = '')
		)
		ORDER BY CASE subject_type WHEN 'USER' THEN 0 WHEN 'GROUP' THEN 1 ELSE 2 END, quota_bytes DESC
		LIMIT 1`, args...).Scan(&bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return -1, nil // unlimited
	}
	if err != nil {
		return 0, fmt.Errorf("effective quota for %s on %s: %w", userID, volumeID, err)
	}
	return bytes, nil
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
