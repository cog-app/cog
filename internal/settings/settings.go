// Package settings provides a key/value store for runtime-configurable system settings.
package settings

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

const KeyPublicRegistration = "public_registration"
const KeyInviteExpiryHours = "invite_expiry_hours"
const DefaultInviteExpiryHours = 72

// Federation: whether this server shares with other servers over OCM, and the
// servers it may (comma or newline separated; empty allows any).
const (
	KeyFederationEnabled = "federation_enabled"
	KeyFederationPeers   = "federation_peers"
)

// MFA enforcement: "" (disabled), "all" (all users), "admins" (admin users only).
const KeyMFARequired = "mfa_required"

// KeyDAVTombstoneDays is how many days CalDAV/CardDAV deletions are kept for
// incremental sync; older sync tokens make clients resync fully.
const KeyDAVTombstoneDays = "dav_tombstone_days"
const DefaultDAVTombstoneDays = 90

// Email template override keys. Each stores a Go template string that is
// parsed on top of the embedded defaults, replacing matching {{define}} blocks.
const (
	KeyEmailOverrideHTML = "email_override_html"
	KeyEmailOverrideText = "email_override_text"
)

// Media/profanity setting keys.
const (
	KeyWhisperEnabled   = "whisper_enabled"
	KeyWhisperModel     = "whisper_model" // tiny, base, small, medium, large
	KeyTTSEngine        = "tts_engine"    // none, piper, system
	KeyTTSModel         = "tts_model"     // piper model name
	KeyTTSCommand       = "tts_command"   // system command template
	KeyMediaCacheVolume = "media_cache_volume"
)

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
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}
	return val, nil
}

// GetMany returns the values for several keys in one query. Keys that are
// not set are absent from the map, which reads as "" like Get.
func (s *Store) GetMany(keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.db.Query(`SELECT key, value FROM system_settings WHERE key IN (?`+strings.Repeat(`, ?`, len(keys)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("get settings: scan: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	return out, nil
}

const upsertSQL = `INSERT INTO system_settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`

// Set upserts a key/value pair.
func (s *Store) Set(key, value string) error {
	if _, err := s.db.Exec(upsertSQL, key, value); err != nil {
		return fmt.Errorf("set setting %q: %w", key, err)
	}
	return nil
}

// SetMany upserts several key/value pairs in one transaction, so a form that
// saves a group of settings either stores all of them or none.
func (s *Store) SetMany(values map[string]string) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set settings: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	stmt, err := tx.Prepare(upsertSQL)
	if err != nil {
		return fmt.Errorf("set settings: prepare: %w", err)
	}
	defer stmt.Close()
	for k, v := range values {
		if _, err := stmt.Exec(k, v); err != nil {
			return fmt.Errorf("set setting %q: %w", k, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set settings: commit: %w", err)
	}
	return nil
}

// GetBool returns true if the value for key is "true". A database error is
// logged and reads as false.
func (s *Store) GetBool(key string) bool {
	val, err := s.Get(key)
	if err != nil {
		slog.Error("read setting", "key", key, "err", err)
		return false
	}
	return val == "true"
}

// GetInt returns the integer value for a key, or the provided default if not
// found or invalid. A database error or an unparsable stored value is logged
// and reads as the default.
func (s *Store) GetInt(key string, defaultVal int) int {
	val, err := s.Get(key)
	if err != nil {
		slog.Error("read setting", "key", key, "err", err)
		return defaultVal
	}
	if val == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		slog.Warn("setting is not an integer; using default", "key", key, "value", val, "default", defaultVal)
		return defaultVal
	}
	return n
}

// SetInt sets an integer value.
func (s *Store) SetInt(key string, val int) error {
	return s.Set(key, strconv.Itoa(val))
}

// FormatBool is how SetBool stores a boolean, for use with SetMany.
func FormatBool(val bool) string {
	if val {
		return "true"
	}
	return "false"
}

// SetBool sets a boolean value as "true" or "false".
func (s *Store) SetBool(key string, val bool) error {
	return s.Set(key, FormatBool(val))
}
