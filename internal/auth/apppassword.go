package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type AppPassword struct {
	ID        string
	UserID    string
	Name      string
	TokenHash string
	CreatedAt time.Time
}

type AppPasswordStore struct {
	db *sql.DB
}

func NewAppPasswordStore(db *sql.DB) *AppPasswordStore {
	return &AppPasswordStore{db: db}
}

// Create generates a new app password and returns the plaintext token.
// The token is hashed before storage; the caller must present the plaintext to the user.
func (s *AppPasswordStore) Create(userID, name string) (plaintext string, ap *AppPassword, err error) {
	token, err := generateToken(32)
	if err != nil {
		return "", nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hash app password: %w", err)
	}

	id, err := generateToken(16)
	if err != nil {
		return "", nil, err
	}

	now := time.Now()
	_, err = s.db.Exec(
		`INSERT INTO app_passwords (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, name, string(hash), now.Unix(),
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert app password: %w", err)
	}

	return token, &AppPassword{
		ID:        id,
		UserID:    userID,
		Name:      name,
		TokenHash: string(hash),
		CreatedAt: now,
	}, nil
}

// Validate checks a plaintext token against all app passwords and returns the
// owning user ID if valid.
func (s *AppPasswordStore) Validate(token string) (userID string, err error) {
	rows, err := s.db.Query(`SELECT id, user_id, token_hash FROM app_passwords`)
	if err != nil {
		return "", fmt.Errorf("query app passwords: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, uid, hash string
		if err := rows.Scan(&id, &uid, &hash); err != nil {
			return "", fmt.Errorf("scan app password: %w", err)
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil {
			return uid, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", errors.New("invalid app password")
}
