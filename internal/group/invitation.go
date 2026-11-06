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
	// InviteeID names the existing user the invitation is for, who alone
	// may accept it; "" for an invitation by email, which anyone holding
	// the link may accept.
	InviteeID string
}

const invitationColumns = `id, group_id, email, token, invited_by, member_type, created_at, expires_at, invitee_id`

func scanInvitation(row interface{ Scan(...any) error }) (Invitation, error) {
	var inv Invitation
	err := row.Scan(&inv.ID, &inv.GroupID, &inv.Email, &inv.Token, &inv.InvitedBy, &inv.MemberType, &inv.CreatedAt, &inv.ExpiresAt, &inv.InviteeID)
	return inv, err
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
	return s.create(groupID, email, "", invitedBy, expiryHours, memberType)
}

// InviteUser invites an existing user, who joins only if they accept: being
// put in a group shares with you whatever is shared with it, so nobody is
// added without saying yes. An earlier invitation of theirs to the group is
// replaced.
func (s *InvitationStore) InviteUser(groupID, inviteeID, invitedBy string, memberType MemberType) (*Invitation, error) {
	if _, err := s.db.Exec(`DELETE FROM group_invitations WHERE group_id = ? AND invitee_id = ?`, groupID, inviteeID); err != nil {
		return nil, fmt.Errorf("replace invitation: %w", err)
	}
	return s.create(groupID, "", inviteeID, invitedBy, 7*24, memberType)
}

func (s *InvitationStore) create(groupID, email, inviteeID, invitedBy string, expiryHours int, memberType MemberType) (*Invitation, error) {
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
		`INSERT INTO group_invitations (id, group_id, email, token, invited_by, member_type, created_at, expires_at, invitee_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, groupID, email, token, invitedBy, memberType, now, expiresAt, inviteeID,
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
		InviteeID:  inviteeID,
	}, nil
}

// GetByToken retrieves a non-expired invitation by its token.
func (s *InvitationStore) GetByToken(token string) (*Invitation, error) {
	inv, err := scanInvitation(s.db.QueryRow(
		`SELECT `+invitationColumns+` FROM group_invitations WHERE token = ? AND expires_at > ?`,
		token, s.clock().Unix(),
	))
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
	return s.list(`group_id = ?`, groupID)
}

// ListForInvitee returns the pending invitations of an existing user.
func (s *InvitationStore) ListForInvitee(userID string) iter.Seq2[Invitation, error] {
	return s.list(`invitee_id = ?`, userID)
}

// Get returns a pending invitation by ID.
func (s *InvitationStore) Get(id string) (*Invitation, error) {
	inv, err := scanInvitation(s.db.QueryRow(
		`SELECT `+invitationColumns+` FROM group_invitations WHERE id = ? AND expires_at > ?`, id, s.clock().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invitation: %w", err)
	}
	return &inv, nil
}

func (s *InvitationStore) list(where string, arg string) iter.Seq2[Invitation, error] {
	return func(yield func(Invitation, error) bool) {
		rows, err := s.db.Query(
			`SELECT `+invitationColumns+` FROM group_invitations WHERE `+where+` AND expires_at > ?
			ORDER BY created_at DESC`,
			arg, s.clock().Unix(),
		)
		if err != nil {
			yield(Invitation{}, fmt.Errorf("list invitations: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			inv, err := scanInvitation(rows)
			if err != nil {
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
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFromGroup removes the group's invitation by ID, and reports
// ErrNotFound for an invitation to any other group: whoever may manage one
// group may not revoke another's invitations.
func (s *InvitationStore) DeleteFromGroup(groupID, id string) error {
	res, err := s.db.Exec(`DELETE FROM group_invitations WHERE id = ? AND group_id = ?`, id, groupID)
	if err != nil {
		return fmt.Errorf("delete invitation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
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
