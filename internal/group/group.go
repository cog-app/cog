// Package group provides CRUD operations for groups and group membership.
package group

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("group not found")

type Group struct {
	ID            string
	Name          string
	ParentGroupID *string
}

type Member struct {
	GroupID    string
	MemberID   string
	MemberType string // "USER" or "GROUP"
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(name string, parentGroupID *string) (*Group, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(
		`INSERT INTO groups (id, name, parent_group_id) VALUES (?, ?, ?)`,
		id, name, parentGroupID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert group: %w", err)
	}
	return &Group{ID: id, Name: name, ParentGroupID: parentGroupID}, nil
}

func (s *Store) Get(id string) (*Group, error) {
	var g Group
	err := s.db.QueryRow(
		`SELECT id, name, parent_group_id FROM groups WHERE id = ?`, id,
	).Scan(&g.ID, &g.Name, &g.ParentGroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get group: %w", err)
	}
	return &g, nil
}

func (s *Store) List() ([]Group, error) {
	rows, err := s.db.Query(`SELECT id, name, parent_group_id FROM groups ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentGroupID); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
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
	return nil
}

func (s *Store) AddMember(groupID, memberID, memberType string) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO group_members (group_id, member_id, member_type) VALUES (?, ?, ?)`,
		groupID, memberID, memberType,
	)
	return err
}

func (s *Store) RemoveMember(groupID, memberID string) error {
	_, err := s.db.Exec(
		`DELETE FROM group_members WHERE group_id = ? AND member_id = ?`,
		groupID, memberID,
	)
	return err
}

func (s *Store) ListMembers(groupID string) ([]Member, error) {
	rows, err := s.db.Query(
		`SELECT group_id, member_id, member_type FROM group_members WHERE group_id = ? ORDER BY member_type, member_id`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.GroupID, &m.MemberID, &m.MemberType); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *Store) ListGroupsForUser(userID string) ([]Group, error) {
	rows, err := s.db.Query(`
		SELECT g.id, g.name, g.parent_group_id
		FROM groups g
		JOIN group_members gm ON g.id = gm.group_id
		WHERE gm.member_type = 'USER' AND gm.member_id = ?
		ORDER BY g.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list groups for user: %w", err)
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentGroupID); err != nil {
			return nil, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
