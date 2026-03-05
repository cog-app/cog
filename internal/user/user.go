package user

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var ErrNotFound = errors.New("user not found")
var ErrAuthFailed = errors.New("authentication failed")

type User struct {
	ID           string
	Username     string
	PasswordHash string
	HomeMount    string
	CreatedAt    time.Time
	IsAdmin      bool
	DisplayName  string
	Avatar       string
	Email        string
}

// UpdateOpts specifies which fields to update. Nil fields are left unchanged.
type UpdateOpts struct {
	DisplayName *string
	IsAdmin     *bool
	Avatar      *string
	Email       *string
}

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Create(username, password string) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	homeMount := fmt.Sprintf("local://data/%s", username)

	// Auto-admin: first user becomes admin.
	isAdmin := false
	count, err := s.Count()
	if err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}
	if count == 0 {
		isAdmin = true
	}

	_, err = s.db.Exec(
		`INSERT INTO users (id, username, password_hash, home_mount, created_at, is_admin, display_name) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, username, string(hash), homeMount, now, isAdmin, "",
	)
	if err != nil {
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
		`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email FROM users WHERE username = ?`, username,
	))
}

func (s *Store) GetByID(id string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email FROM users WHERE id = ?`, id,
	))
}

func (s *Store) Authenticate(username, password string) (*User, error) {
	u, err := s.GetByUsername(username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
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
func (s *Store) List() iter.Seq2[User, error] {
	return func(yield func(User, error) bool) {
		rows, err := s.db.Query(
			`SELECT id, username, password_hash, home_mount, created_at, is_admin, display_name, avatar, email FROM users ORDER BY username`,
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
			if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.HomeMount, &createdAt, &isAdmin, &u.DisplayName, &u.Avatar, &u.Email); err != nil {
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
	if opts.DisplayName != nil {
		if _, err := s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, *opts.DisplayName, id); err != nil {
			return fmt.Errorf("update display_name: %w", err)
		}
	}
	if opts.IsAdmin != nil {
		admin := 0
		if *opts.IsAdmin {
			admin = 1
		}
		if _, err := s.db.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, admin, id); err != nil {
			return fmt.Errorf("update is_admin: %w", err)
		}
	}
	if opts.Avatar != nil {
		if _, err := s.db.Exec(`UPDATE users SET avatar = ? WHERE id = ?`, *opts.Avatar, id); err != nil {
			return fmt.Errorf("update avatar: %w", err)
		}
	}
	if opts.Email != nil {
		if _, err := s.db.Exec(`UPDATE users SET email = ? WHERE id = ?`, *opts.Email, id); err != nil {
			return fmt.Errorf("update email: %w", err)
		}
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

// Delete removes a user and their sessions and app passwords.
func (s *Store) Delete(id string) error {
	// No ON DELETE CASCADE in schema, so delete manually.
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
		return fmt.Errorf("delete sessions: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM app_passwords WHERE user_id = ?`, id); err != nil {
		return fmt.Errorf("delete app_passwords: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM group_members WHERE member_type = 'USER' AND member_id = ?`, id); err != nil {
		return fmt.Errorf("delete group_members: %w", err)
	}
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	var createdAt int64
	var isAdmin int
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.HomeMount, &createdAt, &isAdmin, &u.DisplayName, &u.Avatar, &u.Email)
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
