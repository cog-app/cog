package feed

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// PendingPackage returns up to limit ready downloads the packager has not
// looked at, ordered by item_id.
func (s *Store) PendingPackage(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE state = 'ready' AND package_state = '' ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending package: %w", err)
	}
	return scanInt64IDs(rows, "pending package")
}

// SavePackage records a download's HLS tracks: audioPath becomes its
// local_path, videoPath its video_path ("" for audio alone), and bytes their
// combined size.
func (s *Store) SavePackage(itemID int64, audioPath, videoPath string, bytes int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET local_path = ?, video_path = ?, bytes = ?,
			package_state = 'done', package_error = ''
		WHERE item_id = ?`, audioPath, videoPath, bytes, itemID)
	if err != nil {
		return fmt.Errorf("save package: %w", err)
	}
	return nil
}

// MarkPackageError records why a download could not be packaged. It keeps
// its files and is not tried again until it is downloaded again.
func (s *Store) MarkPackageError(itemID int64, msg string) error {
	_, err := s.db.Exec(`UPDATE downloads SET package_state = 'error', package_error = ?
		WHERE item_id = ?`, msg, itemID)
	if err != nil {
		return fmt.Errorf("mark package error: %w", err)
	}
	return nil
}

// hlsKeySetting names the key media links are signed with.
const hlsKeySetting = "hls_key"

// HLSKey returns the key the user's media links are signed with, making one
// the first time.
func (s *Store) HLSKey() ([]byte, error) {
	fresh, err := newHLSKey()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`INSERT INTO feed_settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO NOTHING`, hlsKeySetting, fresh); err != nil {
		return nil, fmt.Errorf("hls key: %w", err)
	}
	var v string
	if err := s.db.QueryRow(`SELECT value FROM feed_settings WHERE key = ?`, hlsKeySetting).Scan(&v); err != nil {
		return nil, fmt.Errorf("hls key: %w", err)
	}
	key, err := hex.DecodeString(v)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("hls key: stored key is not 32 hex bytes (%d bytes, %v)", len(key), err)
	}
	return key, nil
}

// RotateHLSKey replaces the user's media link key, so every link signed
// with the old one stops working.
func (s *Store) RotateHLSKey() error {
	fresh, err := newHLSKey()
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO feed_settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, hlsKeySetting, fresh); err != nil {
		return fmt.Errorf("rotate hls key: %w", err)
	}
	return nil
}

func newHLSKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("hls key: %w", err)
	}
	return hex.EncodeToString(b), nil
}
