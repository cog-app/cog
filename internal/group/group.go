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
	MemberType MemberType
	Username   string // populated via LEFT JOIN on users
}

type MemberType string

const (
	Owner       MemberType = "OWNER"
	User        MemberType = "USER"
	GroupMember MemberType = "GROUP"
)

func (t MemberType) IsValid() bool {
	switch t {
	case Owner, User, GroupMember:
		return true
	default:
		return false
	}
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
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("delete group members: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := rebuildClosureTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrCyclicMembership = errors.New("adding this group would create a cycle")

func (s *Store) AddMember(groupID, memberID string, memberType MemberType) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Cycle detection for GROUP members: check if groupID is already a descendant
	// of memberID (the candidate child group), which would create a cycle.
	if memberType == GroupMember {
		if isCyclic, err := isCyclicMembershipTx(tx, groupID, memberID); err != nil {
			return fmt.Errorf("cycle check: %w", err)
		} else if isCyclic {
			return ErrCyclicMembership
		}
	}

	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO group_members (group_id, member_id, member_type) VALUES (?, ?, ?)`,
		groupID, memberID, memberType,
	); err != nil {
		return fmt.Errorf("add member: %w", err)
	}
	if err := rebuildClosureTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// isCyclicMembershipTx checks if parentGroupID already appears as a descendant
// of candidateChildGroupID via the closure table, or if they are the same group.
func isCyclicMembershipTx(tx *sql.Tx, parentGroupID, candidateChildGroupID string) (bool, error) {
	if parentGroupID == candidateChildGroupID {
		return true, nil
	}
	// Check if parentGroupID is already reachable from candidateChildGroupID.
	var exists int
	err := tx.QueryRow(
		`SELECT 1 FROM group_membership_closure WHERE ancestor_group_id = ? AND descendant_user_id IN (
			SELECT member_id FROM group_members WHERE group_id = ? AND member_type = 'GROUP'
		)`, candidateChildGroupID, parentGroupID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		// Also do a recursive walk through GROUP members since the closure table
		// only tracks user descendants. Walk the group_members graph directly.
		return isGroupDescendant(tx, candidateChildGroupID, parentGroupID, make(map[string]bool)), nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// isGroupDescendant returns true if target appears as a GROUP-type descendant of start.
func isGroupDescendant(tx *sql.Tx, start, target string, visited map[string]bool) bool {
	if start == target {
		return true
	}
	if visited[start] {
		return false
	}
	visited[start] = true

	rows, err := tx.Query(
		`SELECT member_id FROM group_members WHERE group_id = ? AND member_type = 'GROUP'`, start,
	)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var childID string
		if err := rows.Scan(&childID); err != nil {
			continue
		}
		if isGroupDescendant(tx, childID, target, visited) {
			return true
		}
	}
	return false
}

// UpdateMemberType changes the member_type for an existing group member.
func (s *Store) UpdateMemberType(groupID, memberID string, memberType MemberType) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
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
	if err := rebuildClosureTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveMember(groupID, memberID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM group_members WHERE group_id = ? AND member_id = ?`,
		groupID, memberID,
	); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if err := rebuildClosureTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListMembers(groupID string) iter.Seq2[Member, error] {
	return func(yield func(Member, error) bool) {
		rows, err := s.db.Query(
			`SELECT gm.group_id, gm.member_id, gm.member_type,
				CASE WHEN gm.member_type = 'GROUP' THEN COALESCE(g.name, gm.member_id)
				     ELSE COALESCE(u.username, '') END
			FROM group_members gm
			LEFT JOIN users u ON u.id = gm.member_id AND gm.member_type != 'GROUP'
			LEFT JOIN groups g ON g.id = gm.member_id AND gm.member_type = 'GROUP'
			WHERE gm.group_id = ?
			ORDER BY gm.member_type, gm.member_id`,
			groupID,
		)
		if err != nil {
			yield(Member{}, fmt.Errorf("list members: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.GroupID, &m.MemberID, &m.MemberType, &m.Username); err != nil {
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
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := rebuildClosureTx(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// rebuildClosureTx recomputes the closure table within an existing transaction.
// Handles recursive group nesting via fixed-point expansion.
func rebuildClosureTx(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM group_membership_closure`); err != nil {
		return fmt.Errorf("clear closure: %w", err)
	}

	// Step 1: Insert direct user/owner members at depth 0.
	if _, err := tx.Exec(`
		INSERT INTO group_membership_closure (ancestor_group_id, descendant_user_id, depth)
		SELECT group_id, member_id, 0 FROM group_members
		WHERE member_type IN ('USER', 'OWNER')
	`); err != nil {
		return fmt.Errorf("rebuild closure direct: %w", err)
	}

	// Step 2: Fixed-point loop — for each GROUP-type member, copy child group's
	// closure rows into parent group at depth+1. Repeat until no new rows.
	for depth := 1; depth < 100; depth++ {
		res, err := tx.Exec(`
			INSERT OR IGNORE INTO group_membership_closure (ancestor_group_id, descendant_user_id, depth)
			SELECT gm.group_id, gmc.descendant_user_id, ?
			FROM group_members gm
			JOIN group_membership_closure gmc ON gmc.ancestor_group_id = gm.member_id
			WHERE gm.member_type = 'GROUP'
			  AND NOT EXISTS (
				SELECT 1 FROM group_membership_closure c
				WHERE c.ancestor_group_id = gm.group_id AND c.descendant_user_id = gmc.descendant_user_id
			  )
		`, depth)
		if err != nil {
			return fmt.Errorf("rebuild closure depth %d: %w", depth, err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			break
		}
	}
	return nil
}

// ListClosureUserIDs returns all descendant user IDs for a group from the closure table.
func (s *Store) ListClosureUserIDs(groupID string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT descendant_user_id FROM group_membership_closure WHERE ancestor_group_id = ?`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("list closure user IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan closure user ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
