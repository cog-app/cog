package dav

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// Table implements the read side of a Backend, and purging, for a plugin
// that keeps one component type in one table with collection_id, uid, uri,
// version and a data column. Plugins embed it and add Put and Delete, which
// need their own columns.
type Table struct {
	Name      string // table name
	Data      string // column holding the resource text
	Component Component
	DB        func(owner string) (*sql.DB, error)
}

func (t *Table) columns(withData bool) string {
	data := "NULL"
	if withData {
		data = t.Data
	}
	return "uri, uid, version, length(" + t.Data + "), " + data
}

func (t *Table) scan(rows *sql.Rows) ([]Resource, error) {
	defer rows.Close()
	var out []Resource
	for rows.Next() {
		r := Resource{Component: t.Component}
		var data sql.NullString
		if err := rows.Scan(&r.URI, &r.UID, &r.Version, &r.Size, &data); err != nil {
			return nil, fmt.Errorf("scan %s: %w", t.Name, err)
		}
		if data.Valid {
			r.Data = []byte(data.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Query returns the resources matching a condition on the table.
func (t *Table) Query(q davsql.Querier, withData bool, where string, args ...any) ([]Resource, error) {
	rows, err := q.Query(`SELECT `+t.columns(withData)+` FROM `+t.Name+` WHERE `+where+` ORDER BY uri`, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", t.Name, err)
	}
	return t.scan(rows)
}

func (t *Table) Components() []Component { return []Component{t.Component} }

func (t *Table) List(owner string, c *Collection, withData bool) ([]Resource, error) {
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	return t.Query(db, withData, "collection_id = ?", c.ID)
}

func (t *Table) Get(owner string, c *Collection, uris []string, withData bool) ([]Resource, error) {
	if len(uris) == 0 {
		return nil, nil
	}
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	args := []any{c.ID}
	for _, u := range uris {
		args = append(args, u)
	}
	return t.Query(db, withData, "collection_id = ? AND uri IN (?"+strings.Repeat(",?", len(uris)-1)+")", args...)
}

func (t *Table) FindUID(owner string, c *Collection, uid string) (string, error) {
	db, err := t.DB(owner)
	if err != nil {
		return "", err
	}
	var uri string
	err = db.QueryRow(`SELECT uri FROM `+t.Name+` WHERE collection_id = ? AND uid = ?`, c.ID, uid).Scan(&uri)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find %s %s: %w", t.Name, uid, err)
	}
	return uri, nil
}

func (t *Table) Changes(owner string, c *Collection, since int64, withData bool) (*Changes, error) {
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	defer tx.Rollback()
	state, err := davsql.GetState(tx, int64(c.ID))
	if err != nil {
		return nil, err
	}
	changed, err := t.Query(tx, withData, "collection_id = ? AND version > ?", c.ID, since)
	if err != nil {
		return nil, err
	}
	ch := &Changes{Changed: changed, State: state}
	if since > 0 {
		if ch.Deleted, err = davsql.Deleted(tx, int64(c.ID), since); err != nil {
			return nil, err
		}
	}
	return ch, nil
}

func (t *Table) States(owner string, ids []CollectionID) (map[CollectionID]davsql.State, error) {
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	return States(db, ids)
}

// States reads the sync state of collections from a plugin database.
func States(db *sql.DB, ids []CollectionID) (map[CollectionID]davsql.State, error) {
	raw := make([]int64, len(ids))
	for i, id := range ids {
		raw[i] = int64(id)
	}
	m, err := davsql.States(db, raw)
	if err != nil {
		return nil, err
	}
	out := make(map[CollectionID]davsql.State, len(m))
	for id, s := range m {
		out[CollectionID(id)] = s
	}
	return out, nil
}

func (t *Table) Prune(owner string, c *Collection, cutoff time.Time) error {
	db, err := t.DB(owner)
	if err != nil {
		return err
	}
	return davsql.Prune(db, int64(c.ID), cutoff.Unix())
}

func (t *Table) Purge(owner string, id CollectionID) error {
	db, err := t.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("purge %s: %w", t.Name, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM `+t.Name+` WHERE collection_id = ?`, id); err != nil {
		return fmt.Errorf("purge %s: %w", t.Name, err)
	}
	if err := davsql.Purge(tx, int64(id)); err != nil {
		return err
	}
	return tx.Commit()
}
