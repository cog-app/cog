// Package settings provides a key/value store for runtime-configurable system settings.
package settings

import (
	"database/sql"
	"strconv"
)

const KeyPublicRegistration = "public_registration"
const KeyInviteExpiryHours = "invite_expiry_hours"
const DefaultInviteExpiryHours = 72

// SMTP setting keys.
const (
	KeySMTPHost = "smtp_host"
	KeySMTPPort = "smtp_port"
	KeySMTPUser = "smtp_user"
	KeySMTPPass = "smtp_pass"
	KeySMTPFrom = "smtp_from"
)

// Store is a key/value settings store backed by the system_settings table.
type Store struct {
	db *sql.DB
}

// NewStore creates a new settings store.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Get returns the value for a key, or "" if not found.
func (s *Store) Get(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// Set upserts a key/value pair.
func (s *Store) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO system_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

// GetBool returns true if the value for key is "true".
func (s *Store) GetBool(key string) bool {
	val, err := s.Get(key)
	return err == nil && val == "true"
}

// GetInt returns the integer value for a key, or the provided default if not found or invalid.
func (s *Store) GetInt(key string, defaultVal int) int {
	val, err := s.Get(key)
	if err != nil || val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}

// SetInt sets an integer value.
func (s *Store) SetInt(key string, val int) error {
	return s.Set(key, strconv.Itoa(val))
}

// SetBool sets a boolean value as "true" or "false".
func (s *Store) SetBool(key string, val bool) error {
	v := "false"
	if val {
		v = "true"
	}
	return s.Set(key, v)
}
