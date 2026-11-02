package auth

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// WebAuthnStore manages WebAuthn credentials and challenge sessions. Session
// data (containing the active challenge) is encrypted at rest under a
// purpose-derived key.
type WebAuthnStore struct {
	db      *sql.DB
	clock   clock.Clock
	wa      *webauthn.WebAuthn
	sessKey []byte
}

const webauthnSessionLabel = "webauthn_session/v1"

// WebAuthnCredential represents a stored passkey credential.
type WebAuthnCredential struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt time.Time
}

// NewWebAuthnStore creates a new WebAuthn store. baseURL is used to derive
// the RPID (domain) and origin.
// ErrWebAuthnRejected marks a ceremony the client failed: no ceremony in
// progress, an expired one, or a response that does not verify. Any other
// error from Finish* is the server's.
var ErrWebAuthnRejected = errors.New("webauthn verification failed")

// ErrCredentialNotFound is returned when a credential does not exist or
// belongs to another user.
var ErrCredentialNotFound = errors.New("webauthn credential not found")

func NewWebAuthnStore(db *sql.DB, c clock.Clock, baseURL string, secret []byte) (*WebAuthnStore, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Cog",
		RPID:          u.Hostname(),
		RPOrigins:     []string{baseURL},
	})
	if err != nil {
		return nil, fmt.Errorf("create webauthn: %w", err)
	}

	return &WebAuthnStore{
		db:      db,
		clock:   c,
		wa:      wa,
		sessKey: DerivedKey(secret, webauthnSessionLabel),
	}, nil
}

// webAuthnUser adapts a user for the WebAuthn library.
type webAuthnUser struct {
	id          []byte
	name        string
	displayName string
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte                         { return u.id }
func (u *webAuthnUser) WebAuthnName() string                       { return u.name }
func (u *webAuthnUser) WebAuthnDisplayName() string                { return u.displayName }
func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// BeginRegistration starts a WebAuthn credential registration ceremony.
// Returns the creation options JSON and stores the session challenge.
func (s *WebAuthnStore) BeginRegistration(userID, username, displayName string) ([]byte, error) {
	creds, err := s.loadCredentials(userID)
	if err != nil {
		return nil, err
	}

	waUser := &webAuthnUser{
		id:          []byte(userID),
		name:        username,
		displayName: displayName,
		credentials: creds,
	}

	creation, session, err := s.wa.BeginRegistration(waUser)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}

	if err := s.storeSession(userID, session); err != nil {
		return nil, err
	}

	data, err := json.Marshal(creation)
	if err != nil {
		return nil, fmt.Errorf("marshal creation options: %w", err)
	}
	return data, nil
}

// FinishRegistration completes a WebAuthn credential registration.
// Returns the credential name/ID on success.
func (s *WebAuthnStore) FinishRegistration(userID, username, displayName, credName string, body []byte) error {
	creds, err := s.loadCredentials(userID)
	if err != nil {
		return err
	}

	session, err := s.loadSession(userID)
	if err != nil {
		return err
	}
	defer s.deleteSession(userID)

	waUser := &webAuthnUser{
		id:          []byte(userID),
		name:        username,
		displayName: displayName,
		credentials: creds,
	}

	// Parse the credential creation response from the body.
	parsedResponse, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: parse credential creation response: %w", ErrWebAuthnRejected, err)
	}

	credential, err := s.wa.CreateCredential(waUser, *session, parsedResponse)
	if err != nil {
		return fmt.Errorf("%w: create credential: %w", ErrWebAuthnRejected, err)
	}

	return s.saveCredential(userID, credName, credential)
}

// BeginLogin starts a WebAuthn authentication ceremony.
// Returns the assertion options JSON.
func (s *WebAuthnStore) BeginLogin(userID, username, displayName string) ([]byte, error) {
	creds, err := s.loadCredentials(userID)
	if err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("no webauthn credentials")
	}

	waUser := &webAuthnUser{
		id:          []byte(userID),
		name:        username,
		displayName: displayName,
		credentials: creds,
	}

	assertion, session, err := s.wa.BeginLogin(waUser)
	if err != nil {
		return nil, fmt.Errorf("begin login: %w", err)
	}

	if err := s.storeSession(userID, session); err != nil {
		return nil, err
	}

	data, err := json.Marshal(assertion)
	if err != nil {
		return nil, fmt.Errorf("marshal assertion options: %w", err)
	}
	return data, nil
}

// FinishLogin completes a WebAuthn authentication ceremony.
func (s *WebAuthnStore) FinishLogin(userID, username, displayName string, body []byte) error {
	creds, err := s.loadCredentials(userID)
	if err != nil {
		return err
	}

	session, err := s.loadSession(userID)
	if err != nil {
		return err
	}
	defer s.deleteSession(userID)

	waUser := &webAuthnUser{
		id:          []byte(userID),
		name:        username,
		displayName: displayName,
		credentials: creds,
	}

	parsedResponse, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: parse credential assertion response: %w", ErrWebAuthnRejected, err)
	}

	credential, err := s.wa.ValidateLogin(waUser, *session, parsedResponse)
	if err != nil {
		return fmt.Errorf("%w: validate login: %w", ErrWebAuthnRejected, err)
	}

	// Update sign count.
	return s.updateCredentialSignCount(userID, credential)
}

// ListCredentials returns all WebAuthn credentials for a user.
func (s *WebAuthnStore) ListCredentials(userID string) ([]WebAuthnCredential, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, created_at FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list webauthn credentials: %w", err)
	}
	defer rows.Close()

	var result []WebAuthnCredential
	for rows.Next() {
		var c WebAuthnCredential
		var createdAt int64
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &createdAt); err != nil {
			return nil, fmt.Errorf("scan webauthn credential: %w", err)
		}
		c.CreatedAt = time.Unix(createdAt, 0)
		result = append(result, c)
	}
	return result, rows.Err()
}

// DeleteCredential removes a WebAuthn credential. It returns
// ErrCredentialNotFound when the user has no credential with that id.
func (s *WebAuthnStore) DeleteCredential(id, userID string) error {
	res, err := s.db.Exec(`DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete webauthn credential: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

// CredentialCount returns the number of WebAuthn credentials for a user.
func (s *WebAuthnStore) CredentialCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM webauthn_credentials WHERE user_id = ?`, userID).Scan(&count)
	return count, err
}

// --- internal helpers ---

func (s *WebAuthnStore) loadCredentials(userID string) ([]webauthn.Credential, error) {
	rows, err := s.db.Query(`SELECT credential_data FROM webauthn_credentials WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("load webauthn credentials: %w", err)
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(data), &cred); err != nil {
			return nil, fmt.Errorf("unmarshal credential: %w", err)
		}
		creds = append(creds, cred)
	}
	return creds, rows.Err()
}

func (s *WebAuthnStore) saveCredential(userID, name string, cred *webauthn.Credential) error {
	id, err := generateToken(16)
	if err != nil {
		return err
	}

	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("marshal credential: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO webauthn_credentials (id, user_id, name, credential_data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, name, string(data), s.clock().Unix(),
	)
	return err
}

func (s *WebAuthnStore) updateCredentialSignCount(userID string, cred *webauthn.Credential) error {
	data, err := json.Marshal(cred)
	if err != nil {
		return err
	}

	// Find matching credential by credential ID and update its data.
	rows, err := s.db.Query(`SELECT id, credential_data FROM webauthn_credentials WHERE user_id = ?`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var rowID, rowData string
		if err := rows.Scan(&rowID, &rowData); err != nil {
			return err
		}
		var stored webauthn.Credential
		if err := json.Unmarshal([]byte(rowData), &stored); err != nil {
			// One corrupt row must not lock the user out of their other keys.
			slog.Error("webauthn store: decode stored credential", "user", userID, "credential", rowID, "err", err)
			continue
		}
		if string(stored.ID) == string(cred.ID) {
			_, err := s.db.Exec(`UPDATE webauthn_credentials SET credential_data = ? WHERE id = ?`, string(data), rowID)
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list webauthn credentials: %w", err)
	}
	return nil
}

func (s *WebAuthnStore) storeSession(userID string, session *webauthn.SessionData) error {
	id, err := generateToken(16)
	if err != nil {
		return err
	}

	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	encrypted, err := encryptSecret(data, s.sessKey)
	if err != nil {
		return fmt.Errorf("encrypt session: %w", err)
	}

	// Audit security.md #55: DELETE-then-INSERT was a two-statement window
	// during which loadSession could see zero rows. Wrap in a transaction so
	// the prior session is replaced atomically.
	expires := s.clock().Add(5 * time.Minute)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin webauthn session tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM webauthn_sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("cleanup old webauthn session: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO webauthn_sessions (id, user_id, session_data, expires_at) VALUES (?, ?, ?, ?)`,
		id, userID, encrypted, expires.Unix(),
	); err != nil {
		return fmt.Errorf("insert webauthn session: %w", err)
	}
	return tx.Commit()
}

func (s *WebAuthnStore) loadSession(userID string) (*webauthn.SessionData, error) {
	var data string
	var expiresAt int64
	err := s.db.QueryRow(
		`SELECT session_data, expires_at FROM webauthn_sessions WHERE user_id = ? ORDER BY expires_at DESC LIMIT 1`,
		userID,
	).Scan(&data, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no ceremony in progress", ErrWebAuthnRejected)
	}
	if err != nil {
		return nil, fmt.Errorf("load webauthn session: %w", err)
	}

	if time.Unix(expiresAt, 0).Before(s.clock()) {
		return nil, fmt.Errorf("%w: session expired", ErrWebAuthnRejected)
	}

	decrypted, err := decryptSecret(data, s.sessKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt session: %w", err)
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(decrypted, &session); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &session, nil
}

func (s *WebAuthnStore) deleteSession(userID string) {
	if _, err := s.db.Exec(`DELETE FROM webauthn_sessions WHERE user_id = ?`, userID); err != nil {
		slog.Warn("webauthn store: delete session", "user", userID, "err", err)
	}
}
