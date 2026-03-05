// Package group provides CRUD operations for groups and group membership.
package group

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
)

var ErrNotFound = errors.New("group not found")

type Group struct {
	ID            string
	Name          string
	ParentGroupID *string
	OwnerID       *string
	MemberCount   int
}

type Member struct {
	GroupID    string
	MemberID   string
	MemberType string // "USER" or "OWNER"
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(name string, parentGroupID, ownerID *string) (*Group, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO groups (id, name, parent_group_id, owner_id) VALUES (?, ?, ?, ?)`,
		id, name, parentGroupID, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert group: %w", err)
	}
	return &Group{ID: id, Name: name, ParentGroupID: parentGroupID, OwnerID: ownerID}, nil
}

func (s *Store) Get(id string) (*Group, error) {
	var g Group
	err := s.db.QueryRow(
		`SELECT g.id, g.name, g.parent_group_id, g.owner_id, (SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id) FROM groups g WHERE g.id = ?`, id,
	).Scan(&g.ID, &g.Name, &g.ParentGroupID, &g.OwnerID, &g.MemberCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get group: %w", err)
	}
	return &g, nil
}

func (s *Store) List() iter.Seq2[Group, error] {
	return func(yield func(Group, error) bool) {
		rows, err := s.db.Query(`SELECT g.id, g.name, g.parent_group_id, g.owner_id, (SELECT COUNT(*) FROM group_members gm WHERE gm.group_id = g.id) FROM groups g ORDER BY g.name`)
		if err != nil {
			yield(Group{}, fmt.Errorf("list groups: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.Name, &g.ParentGroupID, &g.OwnerID, &g.MemberCount); err != nil {
				yield(Group{}, fmt.Errorf("scan group: %w", err))
				return
			}
			if !yield(g, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Group{}, err)
		}
	}
}

func (s *Store) Update(id, name string) error {
	res, err := s.db.Exec(`UPDATE groups SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("update group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(id string) error {
	// Remove members first.
	if _, err := s.db.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("delete group members: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return s.RebuildClosure()
}

func (s *Store) AddMember(groupID, memberID, memberType string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO group_members (group_id, member_id, member_type) VALUES (?, ?, ?)`,
		groupID, memberID, memberType,
	)
	if err != nil {
		return err
	}
	return s.RebuildClosure()
}

// UpdateMemberType changes the member_type for an existing group member.
func (s *Store) UpdateMemberType(groupID, memberID, memberType string) error {
	res, err := s.db.Exec(
		`UPDATE group_members SET member_type = ? WHERE group_id = ? AND member_id = ?`,
		memberType, groupID, memberID,
	)
	if err != nil {
		return fmt.Errorf("update member type: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return s.RebuildClosure()
}

func (s *Store) RemoveMember(groupID, memberID string) error {
	_, err := s.db.Exec(
		`DELETE FROM group_members WHERE group_id = ? AND member_id = ?`,
		groupID, memberID,
	)
	if err != nil {
		return err
	}
	return s.RebuildClosure()
}

func (s *Store) ListMembers(groupID string) iter.Seq2[Member, error] {
	return func(yield func(Member, error) bool) {
		rows, err := s.db.Query(
			`SELECT group_id, member_id, member_type FROM group_members WHERE group_id = ? ORDER BY member_type, member_id`,
			groupID,
		)
		if err != nil {
			yield(Member{}, fmt.Errorf("list members: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.GroupID, &m.MemberID, &m.MemberType); err != nil {
				yield(Member{}, fmt.Errorf("scan member: %w", err))
				return
			}
			if !yield(m, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Member{}, err)
		}
	}
}

func (s *Store) ListGroupsForUser(userID string) iter.Seq2[Group, error] {
	return func(yield func(Group, error) bool) {
		rows, err := s.db.Query(`
			SELECT g.id, g.name, g.parent_group_id, g.owner_id,
				(SELECT COUNT(*) FROM group_members gm2 WHERE gm2.group_id = g.id)
			FROM groups g
			JOIN group_members gm ON g.id = gm.group_id
			WHERE gm.member_id = ? AND gm.member_type IN ('USER', 'OWNER')
			ORDER BY g.name`, userID)
		if err != nil {
			yield(Group{}, fmt.Errorf("list groups for user: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.Name, &g.ParentGroupID, &g.OwnerID, &g.MemberCount); err != nil {
				yield(Group{}, fmt.Errorf("scan group: %w", err))
				return
			}
			if !yield(g, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Group{}, err)
		}
	}
}

// ListOwnedByUser returns all groups where the user is an OWNER member.
func (s *Store) ListOwnedByUser(userID string) iter.Seq2[Group, error] {
	return func(yield func(Group, error) bool) {
		rows, err := s.db.Query(`
			SELECT g.id, g.name, g.parent_group_id, g.owner_id,
				(SELECT COUNT(*) FROM group_members gm2 WHERE gm2.group_id = g.id)
			FROM groups g
			JOIN group_members gm ON g.id = gm.group_id
			WHERE gm.member_id = ? AND gm.member_type = 'OWNER'
			ORDER BY g.name`, userID)
		if err != nil {
			yield(Group{}, fmt.Errorf("list owned groups: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var g Group
			if err := rows.Scan(&g.ID, &g.Name, &g.ParentGroupID, &g.OwnerID, &g.MemberCount); err != nil {
				yield(Group{}, fmt.Errorf("scan group: %w", err))
				return
			}
			if !yield(g, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Group{}, err)
		}
	}
}

// IsOwnerMember returns true if the user is an OWNER member of the group.
func (s *Store) IsOwnerMember(groupID, userID string) (bool, error) {
	var exists int
	err := s.db.QueryRow(
		`SELECT 1 FROM group_members WHERE group_id = ? AND member_id = ? AND member_type = 'OWNER'`,
		groupID, userID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check owner member: %w", err)
	}
	return true, nil
}

// RebuildClosure recomputes the group_membership_closure table.
// All members are user IDs (OWNER or USER type), so no recursive walk needed.
func (s *Store) RebuildClosure() error {
	_, err := s.db.Exec(`
		DELETE FROM group_membership_closure;

		INSERT INTO group_membership_closure (ancestor_group_id, descendant_user_id, depth)
		SELECT group_id, member_id, 0 FROM group_members
	`)
	return err
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
