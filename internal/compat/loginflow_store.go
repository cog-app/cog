package compat

import (
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// A pending Login Flow lives in the system database, so a restart does not
// strand an app half way through signing in and any replica can answer its
// poll. Neither token is stored, only its hash, and the app password a grant
// mints is sealed to a key only the poll token derives: a copy of the
// database neither finishes a flow nor reveals the password.

// errFlowNotFound is returned for an unknown, expired or finished flow.
var errFlowNotFound = errors.New("login flow not found")

// errFlowPending is returned when a flow's user has not granted it yet.
var errFlowPending = errors.New("login flow not yet granted")

var (
	flowKEM  = hpke.DHKEM(ecdh.X25519())
	flowKDF  = hpke.HKDFSHA256()
	flowAEAD = hpke.AES256GCM()
	flowInfo = []byte("cog login flow app password")
)

// loginFlowStore keeps pending Login Flows.
type loginFlowStore struct {
	db    *sql.DB
	clock clock.Clock
}

func hashFlowToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// flowKey derives the key pair a flow's app password is sealed to from its
// poll token.
func flowKey(pollToken string) (hpke.PrivateKey, error) {
	return flowKEM.DeriveKeyPair([]byte(pollToken))
}

// cutoff is the creation time before which a flow has expired.
func (s *loginFlowStore) cutoff() int64 {
	return s.clock().Add(-loginFlowExpiry).Unix()
}

// start records a new flow, dropping expired ones.
func (s *loginFlowStore) start(pollToken, loginToken, client string) error {
	key, err := flowKey(pollToken)
	if err != nil {
		return fmt.Errorf("start login flow: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM login_flows WHERE created_at <= ?`, s.cutoff()); err != nil {
		return fmt.Errorf("prune login flows: %w", err)
	}
	_, err = s.db.Exec(`INSERT INTO login_flows (poll_hash, login_hash, public_key, client, created_at) VALUES (?, ?, ?, ?, ?)`,
		hashFlowToken(pollToken), hashFlowToken(loginToken), key.PublicKey().Bytes(), client, s.clock().Unix())
	if err != nil {
		return fmt.Errorf("start login flow: %w", err)
	}
	return nil
}

// pendingClient returns the name of the app waiting on loginToken while its
// flow is live and not yet granted.
func (s *loginFlowStore) pendingClient(loginToken string) (string, error) {
	var client string
	err := s.db.QueryRow(`SELECT client FROM login_flows WHERE login_hash = ? AND sealed IS NULL AND created_at > ?`,
		hashFlowToken(loginToken), s.cutoff()).Scan(&client)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errFlowNotFound
	}
	if err != nil {
		return "", fmt.Errorf("look up login flow: %w", err)
	}
	return client, nil
}

// cancel drops a flow not yet granted, so the app's polling ends without
// credentials.
func (s *loginFlowStore) cancel(loginToken string) error {
	if _, err := s.db.Exec(`DELETE FROM login_flows WHERE login_hash = ? AND sealed IS NULL`, hashFlowToken(loginToken)); err != nil {
		return fmt.Errorf("cancel login flow: %w", err)
	}
	return nil
}

// grant records that username granted the flow, with the app password
// minted for it, sealed to the flow's key. Only the first grant counts.
func (s *loginFlowStore) grant(loginToken, username, appPassword string) error {
	var pub []byte
	err := s.db.QueryRow(`SELECT public_key FROM login_flows WHERE login_hash = ? AND sealed IS NULL AND created_at > ?`,
		hashFlowToken(loginToken), s.cutoff()).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) {
		return errFlowNotFound
	}
	if err != nil {
		return fmt.Errorf("grant login flow: %w", err)
	}
	pk, err := flowKEM.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("grant login flow: %w", err)
	}
	sealed, err := hpke.Seal(pk, flowKDF, flowAEAD, flowInfo, []byte(appPassword))
	if err != nil {
		return fmt.Errorf("seal app password: %w", err)
	}
	res, err := s.db.Exec(`UPDATE login_flows SET login_name = ?, sealed = ? WHERE login_hash = ? AND sealed IS NULL`,
		username, sealed, hashFlowToken(loginToken))
	if err != nil {
		return fmt.Errorf("grant login flow: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("grant login flow: %w", err)
	} else if n == 0 {
		return errFlowNotFound
	}
	return nil
}

// collect ends a granted flow and returns its login name and app password.
// A flow is collected once.
func (s *loginFlowStore) collect(pollToken string) (string, string, error) {
	var (
		name   string
		sealed []byte
	)
	err := s.db.QueryRow(`DELETE FROM login_flows WHERE poll_hash = ? AND sealed IS NOT NULL AND created_at > ? RETURNING login_name, sealed`,
		hashFlowToken(pollToken), s.cutoff()).Scan(&name, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		var waiting bool
		if err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM login_flows WHERE poll_hash = ? AND created_at > ?)`,
			hashFlowToken(pollToken), s.cutoff()).Scan(&waiting); err != nil {
			return "", "", fmt.Errorf("poll login flow: %w", err)
		}
		if waiting {
			return "", "", errFlowPending
		}
		return "", "", errFlowNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("poll login flow: %w", err)
	}
	key, err := flowKey(pollToken)
	if err != nil {
		return "", "", fmt.Errorf("poll login flow: %w", err)
	}
	pw, err := hpke.Open(key, flowKDF, flowAEAD, flowInfo, sealed)
	if err != nil {
		return "", "", fmt.Errorf("open app password: %w", err)
	}
	return name, string(pw), nil
}

// loginFlowExpiry is how long a flow waits to be granted and collected.
const loginFlowExpiry = 10 * time.Minute
