package feed

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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
			edges_edit = '', edges_error = '', edges_index = ''
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
			clean_track_error = '', edges_edit = '', edges_error = '', edges_index = ''
		WHERE item_id = ?`, path, path, itemID)
	if err != nil {
		return fmt.Errorf("save clean track: %w", err)
	}
	return nil
}

// ResetCleanAudio forgets a clean file that no longer exists and asks for the
// item to be cleaned again.
func (s *Store) ResetCleanAudio(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_audio_path = '', needs_profanity = 1, profanity_error = '',
			edges_edit = '', edges_error = '', edges_index = ''
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

// ItemEdges is what Collect needs of a packaged item's edges.
type ItemEdges struct {
	// Edit is the edit whose edges are recorded: "" when none are, or they
	// failed.
	Edit string
	// Pending is an item whose edges are wanted and still being made:
	// neither recorded nor failed.
	Pending bool
}

// PackagedEdges maps every packaged download's item id to its edges.
func (s *Store) PackagedEdges() (map[int64]ItemEdges, error) {
	rows, err := s.db.Query(`SELECT item_id, CASE WHEN edges_error = '' THEN edges_edit ELSE '' END,
			edges_wanted = 1 AND edges_edit = '' AND edges_error = ''
		FROM downloads WHERE package_state = 'done'`)
	if err != nil {
		return nil, fmt.Errorf("packaged edges: %w", err)
	}
	defer rows.Close()
	out := map[int64]ItemEdges{}
	for rows.Next() {
		var id int64
		var e ItemEdges
		if err := rows.Scan(&id, &e.Edit, &e.Pending); err != nil {
			return nil, fmt.Errorf("packaged edges: %w", err)
		}
		out[id] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("packaged edges: %w", err)
	}
	return out, nil
}

// wantedRefresh is how stale a pending item's edges_wanted_at may grow
// before a player asking for it again moves it to the front of the queue.
// Within it, asking again only reads.
const wantedRefresh = 60 // s

// WantEdges records that players want the skip presentation of the items,
// and when: the edges worker makes the most recently asked first. It
// reports whether it wrote, which is when the worker has something new to
// do: an item not wanted before, or one still pending asked for again more
// than wantedRefresh after it last was.
func (s *Store) WantEdges(itemIDs ...int64) (bool, error) {
	if len(itemIDs) == 0 {
		return false, nil
	}
	// Read first: a master request for an item already wanted is the steady
	// state and must not take the write lock.
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE (edges_wanted = 0
				OR edges_edit = '' AND edges_error = '' AND edges_wanted_at < unixepoch() - ?)
			AND item_id IN (`+placeholders(len(itemIDs))+`)`,
		append([]any{wantedRefresh}, int64Args(itemIDs)...)...)
	if err != nil {
		return false, fmt.Errorf("want edges: %w", err)
	}
	asked, err := scanInt64IDs(rows, "want edges")
	if err != nil {
		return false, err
	}
	if len(asked) == 0 {
		return false, nil
	}
	if _, err := s.db.Exec(`UPDATE downloads SET edges_wanted = 1, edges_wanted_at = unixepoch()
		WHERE item_id IN (`+placeholders(len(asked))+`)`, int64Args(asked)...); err != nil {
		return false, fmt.Errorf("want edges: %w", err)
	}
	return true, nil
}

// edgesRetryAfter is how long after failing an item's edges are tried
// again: what failed may have been passing (a full disk, a killed ffmpeg).
const edgesRetryAfter = 6 * time.Hour

// PendingEdges returns up to limit wanted items whose edges are to be made,
// most recently asked for first: packaged, silence detected, and neither
// made nor failed less than edgesRetryAfter ago (the worker clears the
// error of a failed item it takes, RetryEdges). The Clean
// rendition is part of the plan, so an item whose clean audio is still to
// come waits: a clean file waiting for its track, or profanity muting that
// can still run: muting runs at all (muting, the profanity plugin is
// there), has not failed, and has its captions or they are being made and
// have not failed. Edges made before the clean track lands would be reset
// under a player and made twice. A failure (MarkProfanityError,
// MarkTranscribeError) ends the wait: the edges are made from what there
// is, and are made again if a retry lands a clean track.
func (s *Store) PendingEdges(limit int, muting bool) ([]int64, error) {
	rows, err := s.db.Query(`SELECT item_id FROM downloads
		WHERE edges_wanted = 1 AND package_state = 'done' AND silence_state = 'done'
			AND (edges_edit = '' AND edges_error = ''
				OR edges_error != '' AND edges_failed_at < unixepoch() - ?)
			AND NOT (clean_audio_path != '' AND clean_track_path = '' AND clean_track_error = '')
			AND NOT (? AND needs_profanity = 1 AND profanity_error = ''
				AND (captions_path != '' OR needs_transcribe = 1 AND transcribe_error = ''))
		ORDER BY edges_wanted_at DESC, item_id LIMIT ?`,
		int64(edgesRetryAfter/time.Second), muting, limit)
	if err != nil {
		return nil, fmt.Errorf("pending edges: %w", err)
	}
	return scanInt64IDs(rows, "pending edges")
}

// SetEdges records that the item's edges for the edit list exist, and where
// in each file its media is (an edit list with no edge pieces has an empty
// set).
func (s *Store) SetEdges(itemID int64, edit string, index EdgesIndex) error {
	buf, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("encode edges index: %w", err)
	}
	_, err = s.db.Exec(`UPDATE downloads SET edges_edit = ?, edges_error = '', edges_index = ?
		WHERE item_id = ?`, edit, string(buf), itemID)
	if err != nil {
		return fmt.Errorf("set edges: %w", err)
	}
	return nil
}

// MarkEdgesError records that the edges for the edit list ("" when the
// item could not be planned) could not be made. The edit is kept, so it is
// not tried again until an input changes and ResetEdges clears it, or
// edgesRetryAfter passes.
func (s *Store) MarkEdgesError(itemID int64, edit, msg string) error {
	_, err := s.db.Exec(`UPDATE downloads SET edges_edit = ?, edges_error = ?, edges_index = '',
			edges_failed_at = unixepoch()
		WHERE item_id = ?`, edit, msg, itemID)
	if err != nil {
		return fmt.Errorf("mark edges error: %w", err)
	}
	return nil
}

// RetryEdges clears the error of an item whose edges failed, so the worker
// makes them again. It is the worker's, for an item PendingEdges returned.
func (s *Store) RetryEdges(itemID int64) error {
	if _, err := s.db.Exec(`UPDATE downloads SET edges_edit = '', edges_error = '', edges_index = ''
		WHERE item_id = ? AND edges_error != ''`, itemID); err != nil {
		return fmt.Errorf("retry edges: %w", err)
	}
	return nil
}

// ForgetEdges forgets the item's edges if they are still those recorded
// for edit, so the worker makes them again. The master calls it on what it
// read, which the worker may have replaced since.
func (s *Store) ForgetEdges(itemID int64, edit string) error {
	if _, err := s.db.Exec(`UPDATE downloads SET edges_edit = '', edges_error = '', edges_index = ''
		WHERE item_id = ? AND edges_edit = ? AND edges_edit != ''`, itemID, edit); err != nil {
		return fmt.Errorf("forget edges: %w", err)
	}
	return nil
}

// ResetEdges forgets the edges (and their error) of the items, or of every
// item when none is given. Wanted items are made again.
func (s *Store) ResetEdges(itemIDs ...int64) error {
	q := `UPDATE downloads SET edges_edit = '', edges_error = '', edges_index = ''
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

// RecordEdit remembers the cuts an edit list applied and the media duration
// they were planned against. The id names the cuts, so one already recorded
// is left as it is.
func (s *Store) RecordEdit(itemID int64, edit string, applied []hls.Cut, duration float64) error {
	// Read first: a master request for an edit list already served is the
	// steady state and must not take the write lock.
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM hls_edits WHERE item_id = ? AND id = ?`,
		itemID, edit).Scan(&n); err != nil {
		return fmt.Errorf("record edit: %w", err)
	}
	if n > 0 {
		return nil
	}
	buf, err := json.Marshal(applied)
	if err != nil {
		return fmt.Errorf("encode edit cuts: %w", err)
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO hls_edits(item_id, id, cuts, duration, created_at)
		VALUES(?, ?, ?, ?, unixepoch())`, itemID, edit, string(buf), duration); err != nil {
		return fmt.Errorf("record edit: %w", err)
	}
	return nil
}

// EditCuts returns the cuts of a recorded edit list and the media duration
// they were planned against (0 when unknown), or ErrEditNotFound.
func (s *Store) EditCuts(itemID int64, edit string) ([]hls.Cut, float64, error) {
	var buf string
	var duration float64
	err := s.db.QueryRow(`SELECT cuts, duration FROM hls_edits WHERE item_id = ? AND id = ?`,
		itemID, edit).Scan(&buf, &duration)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrEditNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("edit cuts: %w", err)
	}
	var cuts []hls.Cut
	if err := json.Unmarshal([]byte(buf), &cuts); err != nil {
		return nil, 0, fmt.Errorf("decode edit cuts: %w", err)
	}
	return cuts, duration, nil
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
