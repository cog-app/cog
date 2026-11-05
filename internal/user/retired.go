package user

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A deleted account's username lives on outside this server: remote servers
// address federated shares to name@host, other people's events and contacts
// hold its addresses, and people trust shares that come from it. So Delete
// retires the name, and no new account may take it until an admin releases
// it, knowing whoever gets it inherits that reputation.

// ErrUsernameRetired is returned by Create for the name of a deleted account.
var ErrUsernameRetired = fmt.Errorf("%w: it belonged to a deleted account", ErrUsernameTaken)

// RetiredName is the username of a deleted account.
type RetiredName struct {
	Username  string
	Email     string // the address the account last had, or ""
	RetiredAt time.Time
}

// Retired returns the retired usernames, most recently retired first.
func (s *Store) Retired() ([]RetiredName, error) {
	rows, err := s.db.Query(`SELECT username, email, retired_at FROM retired_usernames ORDER BY retired_at DESC, username`)
	if err != nil {
		return nil, fmt.Errorf("list retired usernames: %w", err)
	}
	defer rows.Close()
	var out []RetiredName
	for rows.Next() {
		var r RetiredName
		var at int64
		if err := rows.Scan(&r.Username, &r.Email, &at); err != nil {
			return nil, fmt.Errorf("list retired usernames: %w", err)
		}
		r.RetiredAt = time.Unix(at, 0)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list retired usernames: %w", err)
	}
	return out, nil
}

// Release lets a new account take a retired username, and an admin give
// out the address it last had.
func (s *Store) Release(username string) error {
	res, err := s.db.Exec(`DELETE FROM retired_usernames WHERE username = ?`, username)
	if err != nil {
		return fmt.Errorf("release username %q: %w", username, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("release username %q: %w", username, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RetiredWithEmail returns the retired username whose account last had the
// address email, or "" if none did.
func (s *Store) RetiredWithEmail(email string) (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT username FROM retired_usernames WHERE email != '' AND lower(email) = lower(?)`, strings.TrimSpace(email)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("look up retired email: %w", err)
	}
	return name, nil
}
