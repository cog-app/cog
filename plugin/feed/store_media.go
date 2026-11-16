package feed

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
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
			package_state = 'done', package_error = '',
			edges_edit = '', edges_error = ''
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

// PendingCleanPackage returns up to limit packaged downloads whose clean
// audio has no track yet and has not failed, ordered by item_id.
func (s *Store) PendingCleanPackage(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE package_state = 'done' AND clean_audio_path != ''
			AND clean_track_path = '' AND clean_track_error = ''
		ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending clean package: %w", err)
	}
	return scanInt64IDs(rows, "pending clean package")
}

// SaveCleanTrack records the packaged clean track: it is both the clean
// track and the clean audio the player is given.
func (s *Store) SaveCleanTrack(itemID int64, path string) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_track_path = ?, clean_audio_path = ?,
			clean_track_error = '', edges_edit = '', edges_error = ''
		WHERE item_id = ?`, path, path, itemID)
	if err != nil {
		return fmt.Errorf("save clean track: %w", err)
	}
	return nil
}

// ResetCleanAudio forgets a clean file that no longer exists and asks for the
// item to be cleaned again.
func (s *Store) ResetCleanAudio(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_audio_path = '', needs_profanity = 1,
			edges_edit = '', edges_error = ''
		WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("reset clean audio: %w", err)
	}
	return nil
}

// MarkCleanTrackError records why the clean audio could not be packaged. It
// is not tried again until a new clean file is set.
func (s *Store) MarkCleanTrackError(itemID int64, msg string) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_track_error = ? WHERE item_id = ?`, msg, itemID)
	if err != nil {
		return fmt.Errorf("mark clean track error: %w", err)
	}
	return nil
}

// hlsKeySetting names the key media links are signed with.
const hlsKeySetting = "hls_key"

// HLSKey returns the key the user's media links are signed with, making one
// the first time.
func (s *Store) HLSKey() ([]byte, error) {
	if key, err := s.ExistingHLSKey(); err != nil || key != nil {
		return key, err
	}
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
	return decodeHLSKey(v)
}

// ExistingHLSKey returns the key media links are signed with, or nil when
// none has been made, without writing: links are checked on every segment.
func (s *Store) ExistingHLSKey() ([]byte, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM feed_settings WHERE key = ?`, hlsKeySetting).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hls key: %w", err)
	}
	return decodeHLSKey(v)
}

func decodeHLSKey(v string) ([]byte, error) {
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

// PackagedItemIDs returns the item ids of every packaged download, ordered.
func (s *Store) PackagedItemIDs() ([]int64, error) {
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE package_state = 'done' ORDER BY item_id`)
	if err != nil {
		return nil, fmt.Errorf("packaged item ids: %w", err)
	}
	return scanInt64IDs(rows, "packaged item ids")
}

// WantEdges records that players want the skip presentation of the items. It
// reports whether any item was not wanted before, which is when the edges
// worker has something new to do.
func (s *Store) WantEdges(itemIDs ...int64) (bool, error) {
	if len(itemIDs) == 0 {
		return false, nil
	}
	// Read first: a master request for an item already wanted is the steady
	// state and must not take the write lock.
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE edges_wanted = 0 AND item_id IN (`+placeholders(len(itemIDs))+`)`, int64Args(itemIDs)...)
	if err != nil {
		return false, fmt.Errorf("want edges: %w", err)
	}
	unwanted, err := scanInt64IDs(rows, "want edges")
	if err != nil {
		return false, err
	}
	if len(unwanted) == 0 {
		return false, nil
	}
	if _, err := s.db.Exec(`UPDATE downloads SET edges_wanted = 1
		WHERE item_id IN (`+placeholders(len(unwanted))+`)`, int64Args(unwanted)...); err != nil {
		return false, fmt.Errorf("want edges: %w", err)
	}
	return true, nil
}

// PendingEdges returns up to limit wanted items whose edges are to be made:
// packaged, silence detected, no clean file still waiting for its track (the
// Clean rendition is part of the plan), and neither made nor failed.
func (s *Store) PendingEdges(limit int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE edges_wanted = 1 AND package_state = 'done' AND silence_state = 'done'
			AND edges_edit = '' AND edges_error = ''
			AND NOT (clean_audio_path != '' AND clean_track_path = '' AND clean_track_error = '')
		ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending edges: %w", err)
	}
	return scanInt64IDs(rows, "pending edges")
}

// SetEdges records that the item's edges for the edit list exist (an edit
// list with no edge pieces has an empty set).
func (s *Store) SetEdges(itemID int64, edit string) error {
	_, err := s.db.Exec(`UPDATE downloads SET edges_edit = ?, edges_error = ''
		WHERE item_id = ?`, edit, itemID)
	if err != nil {
		return fmt.Errorf("set edges: %w", err)
	}
	return nil
}

// MarkEdgesError records that the edges for the edit list could not be made.
// The edit is kept, so it is not tried again until an input changes and
// ResetEdges clears it.
func (s *Store) MarkEdgesError(itemID int64, edit, msg string) error {
	_, err := s.db.Exec(`UPDATE downloads SET edges_edit = ?, edges_error = ?
		WHERE item_id = ?`, edit, msg, itemID)
	if err != nil {
		return fmt.Errorf("mark edges error: %w", err)
	}
	return nil
}

// ResetEdges forgets the edges (and their error) of the items, or of every
// item when none is given. Wanted items are made again.
func (s *Store) ResetEdges(itemIDs ...int64) error {
	q := `UPDATE downloads SET edges_edit = '', edges_error = ''
		WHERE (edges_edit != '' OR edges_error != '')`
	var args []any
	if len(itemIDs) > 0 {
		q += ` AND item_id IN (` + placeholders(len(itemIDs)) + `)`
		args = int64Args(itemIDs)
	}
	if _, err := s.db.Exec(q, args...); err != nil {
		return fmt.Errorf("reset edges: %w", err)
	}
	return nil
}

// ErrEditNotFound is returned by EditCuts for an edit list no player was served.
var ErrEditNotFound = errors.New("feed: unknown edit list")

// RecordEdit remembers the cuts an edit list applied. The id names the cuts,
// so one already recorded is left as it is.
func (s *Store) RecordEdit(itemID int64, edit string, applied []hls.Cut) error {
	buf, err := json.Marshal(applied)
	if err != nil {
		return fmt.Errorf("encode edit cuts: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO hls_edits(item_id, id, cuts, created_at)
		VALUES(?, ?, ?, unixepoch())`, itemID, edit, string(buf)); err != nil {
		return fmt.Errorf("record edit: %w", err)
	}
	return nil
}

// EditCuts returns the cuts of a recorded edit list, or ErrEditNotFound.
func (s *Store) EditCuts(itemID int64, edit string) ([]hls.Cut, error) {
	var buf string
	err := s.db.QueryRow(`SELECT cuts FROM hls_edits WHERE item_id = ? AND id = ?`,
		itemID, edit).Scan(&buf)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEditNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("edit cuts: %w", err)
	}
	var cuts []hls.Cut
	if err := json.Unmarshal([]byte(buf), &cuts); err != nil {
		return nil, fmt.Errorf("decode edit cuts: %w", err)
	}
	return cuts, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}
