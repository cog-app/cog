package user

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

	_, err = s.db.Exec(
		`INSERT INTO users (id, username, password_hash, home_mount, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, username, string(hash), homeMount, now,
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
	}, nil
}

func (s *Store) GetByUsername(username string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at FROM users WHERE username = ?`, username,
	))
}

func (s *Store) GetByID(id string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		`SELECT id, username, password_hash, home_mount, created_at FROM users WHERE id = ?`, id,
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

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	var createdAt int64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.HomeMount, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	return &u, nil
}

func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
