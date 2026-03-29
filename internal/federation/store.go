package federation

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"iter"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// Store provides CRUD operations on the federated_shares table.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// NewStore creates a new federation store.
func NewStore(db *sql.DB, c clock.Clock) *Store {
	return &Store{db: db, clock: c}
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

// Create inserts a new federated share.
func (s *Store) Create(fs *FederatedShare) error {
	if fs.ID == "" {
		id, err := generateID()
		if err != nil {
			return err
		}
		fs.ID = id
	}
	if fs.Token == "" {
		token, err := generateToken()
		if err != nil {
			return err
		}
		fs.Token = token
	}
	now := s.clock().Unix()
	fs.CreatedAt = now
	fs.UpdatedAt = now

	_, err := s.db.Exec(`INSERT INTO federated_shares
		(id, direction, state, share_id, remote_id, remote_host, remote_user,
		 local_user_id, token, resource_name, resource_type, owner_name, owner_host,
		 permissions, mount_path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fs.ID, fs.Direction, fs.State, fs.ShareID, fs.RemoteID, fs.RemoteHost, fs.RemoteUser,
		fs.LocalUserID, fs.Token, fs.ResourceName, fs.ResourceType, fs.OwnerName, fs.OwnerHost,
		fs.Permissions, fs.MountPath, fs.CreatedAt, fs.UpdatedAt)
	return err
}

// Get returns a federated share by ID.
func (s *Store) Get(id string) (*FederatedShare, error) {
	var fs FederatedShare
	err := s.db.QueryRow(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
		local_user_id, token, resource_name, resource_type, owner_name, owner_host,
		permissions, mount_path, created_at, updated_at
		FROM federated_shares WHERE id = ?`, id).Scan(
		&fs.ID, &fs.Direction, &fs.State, &fs.ShareID, &fs.RemoteID, &fs.RemoteHost, &fs.RemoteUser,
		&fs.LocalUserID, &fs.Token, &fs.ResourceName, &fs.ResourceType, &fs.OwnerName, &fs.OwnerHost,
		&fs.Permissions, &fs.MountPath, &fs.CreatedAt, &fs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// GetByToken returns a federated share by its shared secret token.
func (s *Store) GetByToken(token string) (*FederatedShare, error) {
	var fs FederatedShare
	err := s.db.QueryRow(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
		local_user_id, token, resource_name, resource_type, owner_name, owner_host,
		permissions, mount_path, created_at, updated_at
		FROM federated_shares WHERE token = ?`, token).Scan(
		&fs.ID, &fs.Direction, &fs.State, &fs.ShareID, &fs.RemoteID, &fs.RemoteHost, &fs.RemoteUser,
		&fs.LocalUserID, &fs.Token, &fs.ResourceName, &fs.ResourceType, &fs.OwnerName, &fs.OwnerHost,
		&fs.Permissions, &fs.MountPath, &fs.CreatedAt, &fs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// GetByRemoteID returns an outbound federated share by its remote-assigned ID.
func (s *Store) GetByRemoteID(remoteHost, remoteID string) (*FederatedShare, error) {
	var fs FederatedShare
	err := s.db.QueryRow(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
		local_user_id, token, resource_name, resource_type, owner_name, owner_host,
		permissions, mount_path, created_at, updated_at
		FROM federated_shares WHERE remote_host = ? AND remote_id = ?`, remoteHost, remoteID).Scan(
		&fs.ID, &fs.Direction, &fs.State, &fs.ShareID, &fs.RemoteID, &fs.RemoteHost, &fs.RemoteUser,
		&fs.LocalUserID, &fs.Token, &fs.ResourceName, &fs.ResourceType, &fs.OwnerName, &fs.OwnerHost,
		&fs.Permissions, &fs.MountPath, &fs.CreatedAt, &fs.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &fs, nil
}

// ListInbound returns inbound federated shares for a user, filtered by state.
// Pass empty state to return all states.
func (s *Store) ListInbound(userID, state string) iter.Seq2[FederatedShare, error] {
	return func(yield func(FederatedShare, error) bool) {
		var rows *sql.Rows
		var err error
		if state == "" {
			rows, err = s.db.Query(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
				local_user_id, token, resource_name, resource_type, owner_name, owner_host,
				permissions, mount_path, created_at, updated_at
				FROM federated_shares WHERE direction = 'inbound' AND local_user_id = ?
				ORDER BY created_at DESC`, userID)
		} else {
			rows, err = s.db.Query(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
				local_user_id, token, resource_name, resource_type, owner_name, owner_host,
				permissions, mount_path, created_at, updated_at
				FROM federated_shares WHERE direction = 'inbound' AND local_user_id = ? AND state = ?
				ORDER BY created_at DESC`, userID, state)
		}
		if err != nil {
			yield(FederatedShare{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var fs FederatedShare
			if err := rows.Scan(&fs.ID, &fs.Direction, &fs.State, &fs.ShareID, &fs.RemoteID,
				&fs.RemoteHost, &fs.RemoteUser, &fs.LocalUserID, &fs.Token, &fs.ResourceName,
				&fs.ResourceType, &fs.OwnerName, &fs.OwnerHost, &fs.Permissions, &fs.MountPath,
				&fs.CreatedAt, &fs.UpdatedAt); err != nil {
				yield(FederatedShare{}, err)
				return
			}
			if !yield(fs, nil) {
				return
			}
		}
	}
}

// ListOutbound returns outbound federated shares for a user.
func (s *Store) ListOutbound(userID string) iter.Seq2[FederatedShare, error] {
	return func(yield func(FederatedShare, error) bool) {
		rows, err := s.db.Query(`SELECT id, direction, state, share_id, remote_id, remote_host, remote_user,
			local_user_id, token, resource_name, resource_type, owner_name, owner_host,
			permissions, mount_path, created_at, updated_at
			FROM federated_shares WHERE direction = 'outbound' AND local_user_id = ?
			ORDER BY created_at DESC`, userID)
		if err != nil {
			yield(FederatedShare{}, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var fs FederatedShare
			if err := rows.Scan(&fs.ID, &fs.Direction, &fs.State, &fs.ShareID, &fs.RemoteID,
				&fs.RemoteHost, &fs.RemoteUser, &fs.LocalUserID, &fs.Token, &fs.ResourceName,
				&fs.ResourceType, &fs.OwnerName, &fs.OwnerHost, &fs.Permissions, &fs.MountPath,
				&fs.CreatedAt, &fs.UpdatedAt); err != nil {
				yield(FederatedShare{}, err)
				return
			}
			if !yield(fs, nil) {
				return
			}
		}
	}
}

// Accept marks an inbound federated share as accepted with the given mount path.
func (s *Store) Accept(id, mountPath string) error {
	now := s.clock().Unix()
	res, err := s.db.Exec(`UPDATE federated_shares SET state = 'accepted', mount_path = ?, updated_at = ?
		WHERE id = ? AND direction = 'inbound' AND state = 'pending'`, mountPath, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("federated share %s not found or not pending", id)
	}
	return nil
}

// Reject marks an inbound federated share as rejected.
func (s *Store) Reject(id string) error {
	now := s.clock().Unix()
	_, err := s.db.Exec(`UPDATE federated_shares SET state = 'rejected', updated_at = ?
		WHERE id = ? AND direction = 'inbound' AND state = 'pending'`, now, id)
	return err
}

// Remove marks a federated share as removed.
func (s *Store) Remove(id string) error {
	now := s.clock().Unix()
	_, err := s.db.Exec(`UPDATE federated_shares SET state = 'removed', updated_at = ? WHERE id = ?`, now, id)
	return err
}

// UpdateState updates the state of a federated share.
func (s *Store) UpdateState(id, state string) error {
	now := s.clock().Unix()
	_, err := s.db.Exec(`UPDATE federated_shares SET state = ?, updated_at = ? WHERE id = ?`, state, now, id)
	return err
}

// SetRemoteID sets the remote-assigned ID for an outbound share.
func (s *Store) SetRemoteID(id, remoteID string) error {
	_, err := s.db.Exec(`UPDATE federated_shares SET remote_id = ? WHERE id = ?`, remoteID, id)
	return err
}

// NCIDFor returns the NC-compatible integer ID for a federated share, creating one if needed.
func (s *Store) NCIDFor(fedShareID string) (int64, error) {
	var ncID int64
	err := s.db.QueryRow(`SELECT nc_id FROM federated_share_nc_ids WHERE federated_share_id = ?`, fedShareID).Scan(&ncID)
	if err == nil {
		return ncID, nil
	}
	res, err := s.db.Exec(`INSERT INTO federated_share_nc_ids (federated_share_id) VALUES (?)`, fedShareID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FedShareIDFromNC returns the federated share ID for a NC integer ID.
func (s *Store) FedShareIDFromNC(ncID int64) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT federated_share_id FROM federated_share_nc_ids WHERE nc_id = ?`, ncID).Scan(&id)
	return id, err
}

// ListAcceptedMounts returns all accepted inbound federated shares for a user.
// These represent remote directories mounted in the user's file tree.
func (s *Store) ListAcceptedMounts(userID string) ([]FederatedShare, error) {
	var shares []FederatedShare
	for fs, err := range s.ListInbound(userID, "accepted") {
		if err != nil {
			return nil, err
		}
		shares = append(shares, fs)
	}
	return shares, nil
}
