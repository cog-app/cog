package group

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// Invitation represents a pending group invite.
type Invitation struct {
	ID         string
	GroupID    string
	Email      string
	Token      string
	InvitedBy  string
	MemberType MemberType
	CreatedAt  int64
	ExpiresAt  int64
}

// InvitationStore provides CRUD for group invitations.
type InvitationStore struct {
	db    *sql.DB
	clock clock.Clock
}

// NewInvitationStore creates a new invitation store.
func NewInvitationStore(db *sql.DB, c clock.Clock) *InvitationStore {
	return &InvitationStore{db: db, clock: c}
}

// Create generates a new invitation with the given expiry (in hours) and a 24-byte random token.
// If expiryHours is <= 0, defaults to 72 hours. memberType should be "USER" or "OWNER".
func (s *InvitationStore) Create(groupID, email, invitedBy string, expiryHours int, memberType MemberType) (*Invitation, error) {
	if expiryHours <= 0 {
		expiryHours = 72
	}
	if memberType == "" {
		memberType = User
	}
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
	}

	now := s.clock().Unix()
	expiresAt := now + int64(expiryHours)*60*60

	_, err = s.db.Exec(
		`INSERT INTO group_invitations (id, group_id, email, token, invited_by, member_type, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, groupID, email, token, invitedBy, memberType, now, expiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("insert invitation: %w", err)
	}

	return &Invitation{
		ID:         id,
		GroupID:    groupID,
		Email:      email,
		Token:      token,
		InvitedBy:  invitedBy,
		MemberType: memberType,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
	}, nil
}

// GetByToken retrieves a non-expired invitation by its token.
func (s *InvitationStore) GetByToken(token string) (*Invitation, error) {
	var inv Invitation
	err := s.db.QueryRow(
		`SELECT id, group_id, email, token, invited_by, member_type, created_at, expires_at
		FROM group_invitations WHERE token = ? AND expires_at > ?`,
		token, s.clock().Unix(),
	).Scan(&inv.ID, &inv.GroupID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.MemberType, &inv.CreatedAt, &inv.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invitation: %w", err)
	}
	return &inv, nil
}

// ListByGroup returns all pending (non-expired) invitations for a group.
func (s *InvitationStore) ListByGroup(groupID string) iter.Seq2[Invitation, error] {
	return func(yield func(Invitation, error) bool) {
		rows, err := s.db.Query(
			`SELECT id, group_id, email, token, invited_by, member_type, created_at, expires_at
			FROM group_invitations WHERE group_id = ? AND expires_at > ?
			ORDER BY created_at DESC`,
			groupID, s.clock().Unix(),
		)
		if err != nil {
			yield(Invitation{}, fmt.Errorf("list invitations: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var inv Invitation
			if err := rows.Scan(&inv.ID, &inv.GroupID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.MemberType, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
				yield(Invitation{}, fmt.Errorf("scan invitation: %w", err))
				return
			}
			if !yield(inv, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Invitation{}, err)
		}
	}
}

// Delete removes an invitation by ID.
func (s *InvitationStore) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM group_invitations WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete invitation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpired removes all expired invitations.
func (s *InvitationStore) DeleteExpired() error {
	_, err := s.db.Exec(`DELETE FROM group_invitations WHERE expires_at <= ?`, s.clock().Unix())
	return err
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
