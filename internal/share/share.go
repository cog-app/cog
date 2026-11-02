// Package share implements the global sharing and permissions system.
package share

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"golang.org/x/crypto/bcrypt"
)

const SubjectInstance = "INSTANCE"

var ErrNotFound = errors.New("share not found")
var ErrAccessDenied = errors.New("access denied")
var ErrPasswordRequired = errors.New("password required")
var ErrPasswordIncorrect = errors.New("incorrect password")

// Record is a canonical global share record.
type Record struct {
	ID           string
	OwnerUserID  string
	ResourceType string // e.g. "files:file"
	ResourceID   string
	SubjectType  string // "USER" or "GROUP"
	SubjectID    string
	Permissions  uint32
	Token        *string
	PasswordHash *string
	Expiration   *int64
	CreatedAt    int64
	UpdatedAt    int64
}

// AccessDecision is the result of a permission check.
type AccessDecision struct {
	Allowed         bool
	EffectivePerms  uint32
	MatchedShareIDs []string
	OwnerUserID     string
}

// Store provides CRUD operations on the global shares table.
type Store struct {
	db    *sql.DB
	clock clock.Clock
	// OnChange, when set, runs after a share is created, changed or
	// deleted, with the share as it was. What it derives from shares (the
	// birthday calendars of the users a shared address book reaches)
	// follows them; it handles its own errors.
	OnChange func(Record)
}

func (s *Store) changed(rec *Record) {
	if s.OnChange != nil && rec != nil {
		s.OnChange(*rec)
	}
}

// NewStore creates a new share store backed by the system database.
func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

// Create inserts a new share record. Returns the created record.
func (s *Store) Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*Record, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	now := s.clock().Unix()

	_, err = s.db.Exec(`
		INSERT INTO shares (id, owner_user_id, resource_type, resource_id,
			subject_type, subject_id, permissions, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, ownerUserID, resourceType, resourceID,
		subjectType, subjectID, permissions, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert share: %w", err)
	}

	rec := &Record{
		ID:           id,
		OwnerUserID:  ownerUserID,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		SubjectType:  subjectType,
		SubjectID:    subjectID,
		Permissions:  permissions,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	s.changed(rec)
	return rec, nil
}

// CreatePublicLink creates a share with a public token.
func (s *Store) CreatePublicLink(ownerUserID, resourceType, resourceID string, permissions uint32, expiration *int64) (*Record, error) {
	id, err := generateID()
	if err != nil {
		return nil, err
	}
	token, err := generateToken()
	if err != nil {
		return nil, err
	}
	now := s.clock().Unix()

	_, err = s.db.Exec(`
		INSERT INTO shares (id, owner_user_id, resource_type, resource_id,
			subject_type, subject_id, permissions, token, expiration, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'PUBLIC', '', ?, ?, ?, ?, ?)`,
		id, ownerUserID, resourceType, resourceID,
		permissions, token, expiration, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert public share: %w", err)
	}

	return &Record{
		ID:           id,
		OwnerUserID:  ownerUserID,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		SubjectType:  "PUBLIC",
		Permissions:  permissions,
		Token:        &token,
		Expiration:   expiration,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// CreatePublicLinkWithPassword creates a password-protected public link.
func (s *Store) CreatePublicLinkWithPassword(ownerUserID, resourceType, resourceID string, permissions uint32, expiration *int64, password string) (*Record, error) {
	rec, err := s.CreatePublicLink(ownerUserID, resourceType, resourceID, permissions, expiration)
	if err != nil {
		return nil, err
	}
	if password != "" {
		if err := s.SetPassword(rec.ID, password); err != nil {
			return nil, err
		}
	}
	return rec, nil
}

// SetPassword sets a bcrypt-hashed password on a share.
func (s *Store) SetPassword(shareID, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	res, err := s.db.Exec(`UPDATE shares SET password_hash = ?, updated_at = ? WHERE id = ?`,
		string(hash), s.clock().Unix(), shareID)
	if err != nil {
		return fmt.Errorf("set share password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set share password rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// CheckPassword verifies a password against the share's hash.
// Returns nil if the share has no password set.
// Returns ErrPasswordRequired if the share has a password but none was provided.
// Returns ErrPasswordIncorrect if the password doesn't match.
func CheckPassword(rec *Record, password string) error {
	if rec.PasswordHash == nil || *rec.PasswordHash == "" {
		return nil // no password required
	}
	if password == "" {
		return ErrPasswordRequired
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*rec.PasswordHash), []byte(password)); err != nil {
		return ErrPasswordIncorrect
	}
	return nil
}

// Get retrieves a share by ID.
func (s *Store) Get(id string) (*Record, error) {
	return s.scanRecord(s.db.QueryRow(`
		SELECT id, owner_user_id, resource_type, resource_id,
			subject_type, subject_id, permissions, token, password_hash,
			expiration, created_at, updated_at
		FROM shares WHERE id = ?`, id))
}

// GetByToken retrieves a share by public token.
func (s *Store) GetByToken(token string) (*Record, error) {
	rec, err := s.scanRecord(s.db.QueryRow(`
		SELECT id, owner_user_id, resource_type, resource_id,
			subject_type, subject_id, permissions, token, password_hash,
			expiration, created_at, updated_at
		FROM shares WHERE token = ?`, token))
	if err != nil {
		return nil, err
	}
	// Check expiration
	if rec.Expiration != nil && *rec.Expiration < s.clock().Unix() {
		return nil, ErrNotFound
	}
	return rec, nil
}

// Delete removes a share record.
func (s *Store) Delete(id string) error {
	rec := Record{ID: id}
	err := s.db.QueryRow(`DELETE FROM shares WHERE id = ?
		RETURNING owner_user_id, resource_type, resource_id, subject_type, subject_id, permissions`, id).
		Scan(&rec.OwnerUserID, &rec.ResourceType, &rec.ResourceID, &rec.SubjectType, &rec.SubjectID, &rec.Permissions)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete share: %w", err)
	}
	s.changed(&rec)
	return nil
}

// ListByOwner returns all shares owned by a user.
func (s *Store) ListByOwner(ownerUserID string) iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		rows, err := s.db.Query(`
			SELECT id, owner_user_id, resource_type, resource_id,
				subject_type, subject_id, permissions, token, password_hash,
				expiration, created_at, updated_at
			FROM shares WHERE owner_user_id = ?
			ORDER BY created_at DESC`, ownerUserID)
		if err != nil {
			yield(Record{}, fmt.Errorf("list shares by owner: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r Record
			if err := rows.Scan(&r.ID, &r.OwnerUserID, &r.ResourceType, &r.ResourceID,
				&r.SubjectType, &r.SubjectID, &r.Permissions, &r.Token, &r.PasswordHash,
				&r.Expiration, &r.CreatedAt, &r.UpdatedAt); err != nil {
				yield(Record{}, fmt.Errorf("scan share: %w", err))
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Record{}, err)
		}
	}
}

// ListBySubject returns all shares targeting a specific user.
func (s *Store) ListBySubject(subjectID string) iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		rows, err := s.db.Query(`
			SELECT s.id, s.owner_user_id, s.resource_type, s.resource_id,
				s.subject_type, s.subject_id, s.permissions, s.token, s.password_hash,
				s.expiration, s.created_at, s.updated_at
			FROM shares s
			WHERE ((s.subject_type = 'USER' AND s.subject_id = ?)
			   OR (s.subject_type = 'GROUP' AND s.subject_id IN (
					SELECT ancestor_group_id FROM group_membership_closure
					WHERE descendant_user_id = ?
				))
			   OR (s.subject_type = 'INSTANCE'))
			  AND (s.expiration IS NULL OR s.expiration > ?)
			ORDER BY s.created_at DESC`, subjectID, subjectID, s.clock().Unix())
		if err != nil {
			yield(Record{}, fmt.Errorf("list shares by subject: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r Record
			if err := rows.Scan(&r.ID, &r.OwnerUserID, &r.ResourceType, &r.ResourceID,
				&r.SubjectType, &r.SubjectID, &r.Permissions, &r.Token, &r.PasswordHash,
				&r.Expiration, &r.CreatedAt, &r.UpdatedAt); err != nil {
				yield(Record{}, fmt.Errorf("scan share: %w", err))
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Record{}, err)
		}
	}
}

// ListByResource returns all shares for a specific resource.
func (s *Store) ListByResource(resourceType, resourceID string) iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		rows, err := s.db.Query(`
			SELECT id, owner_user_id, resource_type, resource_id,
				subject_type, subject_id, permissions, token, password_hash,
				expiration, created_at, updated_at
			FROM shares WHERE resource_type = ? AND resource_id = ?
			ORDER BY created_at DESC`, resourceType, resourceID)
		if err != nil {
			yield(Record{}, fmt.Errorf("list shares by resource: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var r Record
			if err := rows.Scan(&r.ID, &r.OwnerUserID, &r.ResourceType, &r.ResourceID,
				&r.SubjectType, &r.SubjectID, &r.Permissions, &r.Token, &r.PasswordHash,
				&r.Expiration, &r.CreatedAt, &r.UpdatedAt); err != nil {
				yield(Record{}, fmt.Errorf("scan share: %w", err))
				return
			}
			if !yield(r, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(Record{}, err)
		}
	}
}

// CheckAccess determines whether a user has access to a resource.
// It checks direct user shares, group membership shares, and
// hierarchical parent shares (for files).
func (s *Store) CheckAccess(userID, resourceType, resourceID string) (*AccessDecision, error) {
	decision := &AccessDecision{}

	// Check direct shares (user + groups + instance)
	rows, err := s.db.Query(`
		SELECT s.id, s.permissions, s.owner_user_id
		FROM shares s
		WHERE s.resource_type = ? AND s.resource_id = ?
		  AND (
			(s.subject_type = 'USER' AND s.subject_id = ?)
			OR (s.subject_type = 'GROUP' AND s.subject_id IN (
				SELECT ancestor_group_id FROM group_membership_closure
				WHERE descendant_user_id = ?
			))
			OR (s.subject_type = 'INSTANCE')
		  )
		  AND (s.expiration IS NULL OR s.expiration > ?)`,
		resourceType, resourceID, userID, userID, s.clock().Unix())
	if err != nil {
		return nil, fmt.Errorf("check access: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var shareID, ownerID string
		var perms uint32
		if err := rows.Scan(&shareID, &perms, &ownerID); err != nil {
			return nil, fmt.Errorf("scan access: %w", err)
		}
		decision.Allowed = true
		decision.EffectivePerms |= perms
		decision.MatchedShareIDs = append(decision.MatchedShareIDs, shareID)
		decision.OwnerUserID = ownerID
	}

	return decision, rows.Err()
}

// CheckAccessWithHierarchy checks access for a resource, and if not found,
// walks up the parent hierarchy (for files: check parent directories).
// parentLookup should return the parent resource ID, or "" if at root.
func (s *Store) CheckAccessWithHierarchy(userID, resourceType, resourceID string, parentLookup func(resourceID string) (string, error)) (*AccessDecision, error) {
	currentID := resourceID
	for currentID != "" {
		decision, err := s.CheckAccess(userID, resourceType, currentID)
		if err != nil {
			return nil, err
		}
		if decision.Allowed {
			return decision, nil
		}
		parentID, err := parentLookup(currentID)
		if err != nil {
			return nil, fmt.Errorf("parent lookup: %w", err)
		}
		currentID = parentID
	}
	return &AccessDecision{}, nil
}

// Update modifies a share's permissions and/or expiration.
func (s *Store) Update(id string, permissions *uint32, expiration **int64) error {
	// One statement, so permissions and expiration change together.
	var sets []string
	var args []any
	if permissions != nil {
		sets, args = append(sets, "permissions = ?"), append(args, *permissions)
	}
	if expiration != nil {
		sets, args = append(sets, "expiration = ?"), append(args, *expiration)
	}
	if len(sets) > 0 {
		sets, args = append(sets, "updated_at = ?"), append(args, s.clock().Unix(), id)
		if _, err := s.db.Exec(`UPDATE shares SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return fmt.Errorf("update share: %w", err)
		}
	}
	if s.OnChange != nil {
		rec, err := s.Get(id)
		if err != nil {
			return fmt.Errorf("read updated share: %w", err)
		}
		s.changed(rec)
	}
	return nil
}

func (s *Store) scanRecord(row *sql.Row) (*Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.OwnerUserID, &r.ResourceType, &r.ResourceID,
		&r.SubjectType, &r.SubjectID, &r.Permissions, &r.Token, &r.PasswordHash,
		&r.Expiration, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan share: %w", err)
	}
	return &r, nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func generateToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NCIDFor returns the NC-compatible integer ID for a share, creating one if needed.
func (s *Store) NCIDFor(shareID string) (int64, error) {
	var ncID int64
	err := s.db.QueryRow(`SELECT nc_id FROM share_nc_ids WHERE share_id = ?`, shareID).Scan(&ncID)
	if err == nil {
		return ncID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("lookup nc id: %w", err)
	}
	// A concurrent caller may have inserted between the SELECT and here; the
	// no-op update makes RETURNING yield the existing id instead of failing
	// on the unique constraint.
	err = s.db.QueryRow(`INSERT INTO share_nc_ids (share_id) VALUES (?)
		ON CONFLICT (share_id) DO UPDATE SET share_id = excluded.share_id
		RETURNING nc_id`, shareID).Scan(&ncID)
	if err != nil {
		return 0, fmt.Errorf("create nc id: %w", err)
	}
	return ncID, nil
}

// NCIDsFor is NCIDFor for several shares: two queries however many there
// are, creating the ids that do not exist yet.
func (s *Store) NCIDsFor(shareIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(shareIDs))
	if len(shareIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(shareIDs))
	for i, id := range shareIDs {
		args[i] = id
	}
	values := "(?)" + strings.Repeat(",(?)", len(shareIDs)-1)
	if _, err := s.db.Exec(`INSERT INTO share_nc_ids (share_id) VALUES `+values+`
		ON CONFLICT (share_id) DO NOTHING`, args...); err != nil {
		return nil, fmt.Errorf("create nc ids: %w", err)
	}
	rows, err := s.db.Query(`SELECT share_id, nc_id FROM share_nc_ids
		WHERE share_id IN (?`+strings.Repeat(",?", len(shareIDs)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("lookup nc ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ncID int64
		if err := rows.Scan(&id, &ncID); err != nil {
			return nil, fmt.Errorf("scan nc id: %w", err)
		}
		out[id] = ncID
	}
	return out, rows.Err()
}

// ShareIDFromNC looks up the internal share ID from an NC integer ID.
func (s *Store) ShareIDFromNC(ncID int64) (string, error) {
	var shareID string
	err := s.db.QueryRow(`SELECT share_id FROM share_nc_ids WHERE nc_id = ?`, ncID).Scan(&shareID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lookup share id from nc id: %w", err)
	}
	return shareID, nil
}

// PermissionNames returns human-readable names for a permission bitmask.
func PermissionNames(perms uint32) []string {
	var names []string
	if perms&plugin.PermRead != 0 {
		names = append(names, "read")
	}
	if perms&plugin.PermWrite != 0 {
		names = append(names, "write")
	}
	if perms&plugin.PermReshare != 0 {
		names = append(names, "reshare")
	}
	return names
}
