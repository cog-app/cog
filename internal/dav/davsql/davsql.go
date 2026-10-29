// Package davsql keeps DAV sync state inside a plugin's own database: a
// version counter per collection and tombstones for deleted resources.
//
// Every function runs in the caller's transaction, so a resource write and
// its version, or a delete and its tombstone, commit together. The tables are
// declared in each plugin's schema; Schema holds the DDL to copy.
package davsql

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Schema is the DDL every plugin database that holds DAV resources includes.
const Schema = `CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0,
	modified INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);
`

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Bump increments a collection's counter and returns the new value, the
// version for the resource being written. now (unix seconds) becomes the
// collection's modification time, which feeds send as Last-Modified.
func Bump(tx Querier, collectionID, now int64) (int64, error) {
	var v int64
	err := tx.QueryRow(`INSERT INTO dav_sync (collection_id, token, modified) VALUES (?, 1, ?)
		ON CONFLICT(collection_id) DO UPDATE SET token = token + 1, modified = MAX(modified, excluded.modified)
		RETURNING token`, collectionID, now).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("bump sync token of collection %d: %w", collectionID, err)
	}
	return v, nil
}

// Delete records that uri was removed from a collection at a new version and
// returns that version. Call it in the transaction that deletes the row.
func Delete(tx Querier, collectionID int64, uri string, now int64) (int64, error) {
	v, err := Bump(tx, collectionID, now)
	if err != nil {
		return 0, err
	}
	if err := Tombstone(tx, collectionID, uri, v, now); err != nil {
		return 0, err
	}
	return v, nil
}

// Tombstone records uri as deleted at version. Use it when several resources
// go in one change (a task and its subtasks) and share a version.
func Tombstone(tx Querier, collectionID int64, uri string, version, now int64) error {
	if _, err := tx.Exec(`INSERT INTO dav_tombstones (collection_id, uri, version, deleted_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET version = excluded.version, deleted_at = excluded.deleted_at`,
		collectionID, uri, version, now); err != nil {
		return fmt.Errorf("tombstone %s in collection %d: %w", uri, collectionID, err)
	}
	return nil
}

// Revive forgets a tombstone, for a PUT that recreates a deleted URI.
func Revive(tx Querier, collectionID int64, uri string) error {
	if _, err := tx.Exec(`DELETE FROM dav_tombstones WHERE collection_id = ? AND uri = ?`, collectionID, uri); err != nil {
		return fmt.Errorf("revive %s in collection %d: %w", uri, collectionID, err)
	}
	return nil
}

// State is a collection's sync state in one database.
type State struct {
	Token    int64 // highest version written
	Floor    int64 // highest version whose tombstones were pruned
	Modified int64 // unix seconds of the last write or deletion
}

// GetState returns one collection's state; a collection never written to has
// the zero state.
func GetState(q Querier, collectionID int64) (State, error) {
	var s State
	err := q.QueryRow(`SELECT token, floor, modified FROM dav_sync WHERE collection_id = ?`, collectionID).Scan(&s.Token, &s.Floor, &s.Modified)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("sync state of collection %d: %w", collectionID, err)
	}
	return s, nil
}

// States returns the state of several collections in one query.
func States(q Querier, collectionIDs []int64) (map[int64]State, error) {
	out := make(map[int64]State, len(collectionIDs))
	if len(collectionIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(collectionIDs))
	for i, id := range collectionIDs {
		args[i] = id
	}
	rows, err := q.Query(`SELECT collection_id, token, floor, modified FROM dav_sync
		WHERE collection_id IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("sync states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s State
		if err := rows.Scan(&id, &s.Token, &s.Floor, &s.Modified); err != nil {
			return nil, fmt.Errorf("scan sync state: %w", err)
		}
		out[id] = s
	}
	return out, rows.Err()
}

// Deleted returns the URIs deleted from a collection after version since,
// and the version each was deleted at, in version order.
func Deleted(q Querier, collectionID, since int64) ([]string, []int64, error) {
	rows, err := q.Query(`SELECT uri, version FROM dav_tombstones WHERE collection_id = ? AND version > ? ORDER BY version`,
		collectionID, since)
	if err != nil {
		return nil, nil, fmt.Errorf("tombstones of collection %d: %w", collectionID, err)
	}
	defer rows.Close()
	var uris []string
	var versions []int64
	for rows.Next() {
		var uri string
		var v int64
		if err := rows.Scan(&uri, &v); err != nil {
			return nil, nil, fmt.Errorf("scan tombstone: %w", err)
		}
		uris = append(uris, uri)
		versions = append(versions, v)
	}
	return uris, versions, rows.Err()
}

// Prune drops a collection's tombstones deleted before cutoff (unix seconds)
// and raises its floor to the highest version dropped, so a sync token that
// predates them is refused rather than silently missing deletions. It only
// writes when there is something to prune.
func Prune(db *sql.DB, collectionID, cutoff int64) error {
	var maxV sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM dav_tombstones WHERE collection_id = ? AND deleted_at < ?`,
		collectionID, cutoff).Scan(&maxV); err != nil {
		return fmt.Errorf("find expired tombstones of collection %d: %w", collectionID, err)
	}
	if !maxV.Valid {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("prune collection %d: %w", collectionID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM dav_tombstones WHERE collection_id = ? AND version <= ?`, collectionID, maxV.Int64); err != nil {
		return fmt.Errorf("prune collection %d: %w", collectionID, err)
	}
	if _, err := tx.Exec(`UPDATE dav_sync SET floor = MAX(floor, ?) WHERE collection_id = ?`, maxV.Int64, collectionID); err != nil {
		return fmt.Errorf("raise floor of collection %d: %w", collectionID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("prune collection %d: %w", collectionID, err)
	}
	return nil
}

// Purge forgets a deleted collection's sync state. Call it in the transaction
// that deletes the collection's rows.
func Purge(tx Querier, collectionID int64) error {
	if _, err := tx.Exec(`DELETE FROM dav_tombstones WHERE collection_id = ?`, collectionID); err != nil {
		return fmt.Errorf("purge tombstones of collection %d: %w", collectionID, err)
	}
	if _, err := tx.Exec(`DELETE FROM dav_sync WHERE collection_id = ?`, collectionID); err != nil {
		return fmt.Errorf("purge sync state of collection %d: %w", collectionID, err)
	}
	return nil
}

// Conditions are a request's If-Match and If-None-Match, reduced to versions.
// The DAV core parses the ETags; backends check them against the row they
// read inside their write transaction, so the check and the write are one
// atomic step.
type Conditions struct {
	IfMatch        []int64 // any of these versions; empty means no If-Match
	IfMatchAny     bool    // If-Match: *
	IfMatchNone    bool    // If-Match named only ETags of other collections: can never match
	IfNoneMatchAny bool    // If-None-Match: *
}

// ErrPrecondition is returned by Check when the conditions fail.
var ErrPrecondition = errors.New("precondition failed")

// Check evaluates the conditions against the current version of a resource,
// or nil when it does not exist.
func (c Conditions) Check(current *int64) error {
	if c.IfNoneMatchAny && current != nil {
		return ErrPrecondition
	}
	if c.IfMatchNone {
		return ErrPrecondition
	}
	if c.IfMatchAny && current == nil {
		return ErrPrecondition
	}
	if len(c.IfMatch) > 0 {
		if current == nil {
			return ErrPrecondition
		}
		if !slices.Contains(c.IfMatch, *current) {
			return ErrPrecondition
		}
	}
	return nil
}
