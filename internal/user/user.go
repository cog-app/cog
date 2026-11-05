package user

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/mattn/go-sqlite3"
	"iter"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"golang.org/x/crypto/bcrypt"
)

var ErrNotFound = errors.New("user not found")
var ErrAuthFailed = errors.New("authentication failed")

// ErrUsernameTaken is returned by Create when another account already has the
// username.
var ErrUsernameTaken = errors.New("username already taken")

type User struct {
	ID              string
	Username        string
	PasswordHash    string
	HomeMount       string
	CreatedAt       time.Time
	IsAdmin         bool
	DisplayName     string
	Avatar          string
	Email           string
	AvatarUpdatedAt int64
	MFAMethod       string // "", "totp", "email", "webauthn"
}

// AvatarURL returns the avatar URL with a cache-busting version parameter.
func (u *User) AvatarURL() string {
	url := "/avatar/" + u.ID
	if u.AvatarUpdatedAt > 0 {
		url += fmt.Sprintf("?v=%d", u.AvatarUpdatedAt)
	}
	return url
}

// UpdateOpts specifies which fields to update. Nil fields are left unchanged.
type UpdateOpts struct {
	DisplayName *string
	IsAdmin     *bool
	Avatar      *string
	Email       *string
}

type Store struct {
	db    *sql.DB
	clock clock.Clock

	reservedMu sync.RWMutex
	reserved   []string // lower-cased, beyond reservedNames
}

func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
}

func (s *Store) Create(username, password string) (*User, error) {
	if err := s.ValidateUsername(username); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := s.clock().Unix()
	homeMount := fmt.Sprintf("local://data/%s", username)

	// One statement, so two concurrent registrations can neither both become
	// the first user (who is made admin) nor take names differing only in
	// case, which would share a directory on a case-insensitive file system.
	var isAdmin bool
	err = s.db.QueryRow(
		`INSERT INTO users (id, username, password_hash, home_mount, created_at, is_admin, display_name)
		SELECT ?, ?, ?, ?, ?, NOT EXISTS (SELECT 1 FROM users), ''
		WHERE NOT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower(?))
		RETURNING is_admin`,
		id, username, string(hash), homeMount, now, username,
	).Scan(&isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("insert user %q: %w", username, ErrUsernameTaken)
	}
	if err != nil {
		var se sqlite3.Error
		if errors.As(err, &se) && se.ExtendedCode == sqlite3.ErrConstraintUnique {
			return nil, fmt.Errorf("insert user %q: %w", username, ErrUsernameTaken)
		}
		return nil, fmt.Errorf("insert user: %w", err)
	}

	return &User{
		ID:           id,
		Username:     username,
		PasswordHash: string(hash),
		HomeMount:    homeMount,
		CreatedAt:    time.Unix(now, 0),
		IsAdmin:      isAdmin,
	}, nil
}

func (s *Store) GetByUsername(username string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method FROM users WHERE username = ?`, username,
	))
}

func (s *Store) GetByID(id string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method FROM users WHERE id = ?`, id,
	))
}

// idChunk bounds the number of bound parameters in one GetByIDs query, well
// under SQLite's SQLITE_MAX_VARIABLE_NUMBER on any build.
const idChunk = 500

// GetByIDs looks users up by id in as few queries as the parameter limit
// allows. The result is keyed by id; ids that match nobody are absent, and
// duplicates and empty ids are ignored.
func (s *Store) GetByIDs(ids []string) (map[string]*User, error) {
	return s.getByColumn("id", ids, func(u *User) string { return u.ID })
}

// GetByUsernames is GetByIDs by username: the result is keyed by username.
func (s *Store) GetByUsernames(usernames []string) (map[string]*User, error) {
	return s.getByColumn("username", usernames, func(u *User) string { return u.Username })
}

// getByColumn looks up the users whose column (a trusted constant) is one of
// values, chunked under SQLite's parameter limit.
func (s *Store) getByColumn(column string, values []string, key func(*User) string) (map[string]*User, error) {
	out := make(map[string]*User, len(values))
	seen := make(map[string]bool, len(values))
	args := make([]any, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		args = append(args, v)
	}
	for start := 0; start < len(args); start += idChunk {
		chunk := args[start:min(start+idChunk, len(args))]
		if err := s.getChunk(column, chunk, key, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) getChunk(column string, args []any, key func(*User) string, out map[string]*User) error {
	rows, err := s.db.Query(`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method
		FROM users WHERE `+column+` IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return fmt.Errorf("users by %s: %w", column, err)
	}
	defer rows.Close()
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return err
		}
		out[key(u)] = u
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("users by %s: %w", column, err)
	}
	return nil
}

// GetByEmail looks up a user by exact email match. Empty email returns
// ErrNotFound to prevent matching accounts that haven't set one.
func (s *Store) GetByEmail(email string) (*User, error) {
	if email == "" {
		return nil, ErrNotFound
	}
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method FROM users WHERE email = ?`, email,
	))
}

// GetByEmails looks users up by email, ignoring case, in one query. The
// result is keyed by the lower-cased address; when several users share one,
// the first by username wins. Empty addresses match nobody.
func (s *Store) GetByEmails(emails []string) (map[string]*User, error) {
	var args []any
	for _, e := range emails {
		if e != "" {
			args = append(args, strings.ToLower(e))
		}
	}
	out := map[string]*User{}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method
		FROM users WHERE email != '' AND lower(email) IN (?`+strings.Repeat(",?", len(args)-1)+`) ORDER BY username`, args...)
	if err != nil {
		return nil, fmt.Errorf("users by email: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		u, err := s.scanUser(rows)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(u.Email)
		if _, ok := out[key]; !ok {
			out[key] = u
		}
	}
	return out, rows.Err()
}

// dummyBcryptHash is a pre-computed bcrypt hash for the empty password.
// Used to make Authenticate run bcrypt even for unknown usernames so the
// caller can't distinguish "no such user" from "wrong password" via timing
// (audit security.md #25).
var dummyBcryptHash = []byte("$2a$10$abcdefghijklmnopqrstuOuukDpEPiQyGyDk/JFFwjVjVnKEZRiou")

func (s *Store) Authenticate(username, password string) (*User, error) {
	u, err := s.GetByUsername(username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Run bcrypt against a dummy hash so the response time matches
			// the wrong-password path. Result is discarded.
			_ = bcrypt.CompareHashAndPassword(dummyBcryptHash, []byte(password))
			return nil, ErrAuthFailed
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, ErrAuthFailed
	}

	return u, nil
}

// List returns all users ordered by username.
// Usernames returns all usernames sorted alphabetically.
func (s *Store) Usernames() ([]string, error) {
	rows, err := s.db.Query(`SELECT username FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (s *Store) List() iter.Seq2[User, error] {
	return func(yield func(User, error) bool) {
		rows, err := s.db.Query(
			`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email, avatar_updated_at, mfa_method FROM users ORDER BY username`,
		)
		if err != nil {
			yield(User{}, fmt.Errorf("list users: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var u User
			var createdAt int64
			var isAdmin int
			if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.HomeMount, &createdAt, &isAdmin, &u.DisplayName, &u.Avatar, &u.Email, &u.AvatarUpdatedAt, &u.MFAMethod); err != nil {
				yield(User{}, fmt.Errorf("scan user: %w", err))
				return
			}
			u.CreatedAt = time.Unix(createdAt, 0)
			u.IsAdmin = isAdmin != 0
			if !yield(u, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(User{}, err)
		}
	}
}

// Count returns the total number of users.
func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

// Update modifies user fields specified in opts.
func (s *Store) Update(id string, opts UpdateOpts) error {
	// One statement, so the fields change together or not at all.
	var sets []string
	var args []any
	if opts.DisplayName != nil {
		sets, args = append(sets, "display_name = ?"), append(args, *opts.DisplayName)
	}
	if opts.IsAdmin != nil {
		admin := 0
		if *opts.IsAdmin {
			admin = 1
		}
		sets, args = append(sets, "is_admin = ?"), append(args, admin)
	}
	if opts.Avatar != nil {
		sets, args = append(sets, "avatar = ?", "avatar_updated_at = ?"), append(args, *opts.Avatar, s.clock().Unix())
	}
	if opts.Email != nil {
		sets, args = append(sets, "email = ?"), append(args, *opts.Email)
	}
	if len(sets) == 0 {
		return nil
	}
	if _, err := s.db.Exec(`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, id)...); err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

// UpdatePassword changes a user's password.
func (s *Store) UpdatePassword(id, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	_, err = s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id)
	return err
}

// SetMFAMethod updates the user's MFA method.
func (s *Store) SetMFAMethod(id, method string) (string, error) {
	_, err := s.db.Exec(`UPDATE users SET mfa_method = ? WHERE id = ?`, method, id)
	return method, err
}

// Delete removes a user and their sessions, app passwords and group
// memberships, in one transaction so a failure part way cannot leave a
// user who can no longer log in but still exists.
func (s *Store) Delete(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// No ON DELETE CASCADE in schema, so delete manually.
	for _, q := range []struct{ what, sql string }{
		{"sessions", `DELETE FROM sessions WHERE user_id = ?`},
		{"app_passwords", `DELETE FROM app_passwords WHERE user_id = ?`},
		{"group_members", `DELETE FROM group_members WHERE member_type = 'USER' AND member_id = ?`},
		{"group_membership_closure", `DELETE FROM group_membership_closure WHERE descendant_user_id = ?`},
	} {
		if _, err := tx.Exec(q.sql, id); err != nil {
			return fmt.Errorf("delete %s: %w", q.what, err)
		}
	}
	res, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var createdAt int64
	var isAdmin int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.HomeMount, &createdAt, &isAdmin, &u.DisplayName, &u.Avatar, &u.Email, &u.AvatarUpdatedAt, &u.MFAMethod)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	u.IsAdmin = isAdmin != 0
	return &u, nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
