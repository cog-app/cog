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
// that keeps its resources in one table with id, collection_id, uid, uri,
// version and size columns, and generates their text from rows (Render).
// Plugins embed it and add Put and Delete, which need their own columns.
type Table struct {
	Name string // table name
	// Component is the type of every resource, or "" for a table whose
	// component column names each one's.
	Component Component
	DB        func(owner string) (*sql.DB, error)
	// Render generates the text of the rows with these IDs, in collection c,
	// reading through q. size holds the length of what it generates, so
	// listings without data need not render.
	Render func(q davsql.Querier, c *Collection, ids []int64) (map[int64][]byte, error)
	// Load reads the models of the rows with these IDs (Object or Card
	// set), for ModelReader; nil for a table without them.
	Load func(q davsql.Querier, c *Collection, ids []int64) (map[int64]Model, error)
}

func (t *Table) columns() string {
	comp := "'" + string(t.Component) + "'"
	if t.Component == "" {
		comp = "component"
	}
	return "id, uri, uid, version, created_version, size, " + comp
}

func (t *Table) scan(rows *sql.Rows) ([]Resource, []int64, error) {
	defer rows.Close()
	var out []Resource
	var ids []int64
	for rows.Next() {
		var r Resource
		var id int64
		if err := rows.Scan(&id, &r.URI, &r.UID, &r.Version, &r.Created, &r.Size, &r.Component); err != nil {
			return nil, nil, fmt.Errorf("scan %s: %w", t.Name, err)
		}
		out = append(out, r)
		ids = append(ids, id)
	}
	return out, ids, rows.Err()
}

// Query returns the resources in collection c matching a condition on the
// table.
func (t *Table) Query(q davsql.Querier, c *Collection, withData bool, where string, args ...any) ([]Resource, error) {
	rows, err := q.Query(`SELECT `+t.columns()+` FROM `+t.Name+` WHERE `+where+` ORDER BY uri`, args...)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", t.Name, err)
	}
	out, ids, err := t.scan(rows)
	if err != nil || !withData || len(out) == 0 {
		return out, err
	}
	data, err := t.Render(q, c, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Data = data[ids[i]]
		out[i].Size = int64(len(out[i].Data))
	}
	return out, nil
}

func (t *Table) Components() []Component { return []Component{t.Component} }

// Usage sums the size column over every row of owner's table: a recount
// each time, so nothing a write path forgets can drift it.
func (t *Table) Usage(owner string) (int64, error) {
	db, err := t.DB(owner)
	if err != nil {
		return 0, err
	}
	var n int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM ` + t.Name).Scan(&n); err != nil {
		return 0, fmt.Errorf("dav: usage of %s for %s: %w", t.Name, owner, err)
	}
	return n, nil
}

func (t *Table) List(owner string, c *Collection, withData bool) ([]Resource, error) {
	db, err := t.DB(owner)
	if err != nil {
		return nil, err
	}
	return t.Query(db, c, withData, "collection_id = ?", c.ID)
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
	return t.Query(db, c, withData, "collection_id = ? AND uri IN (?"+strings.Repeat(",?", len(uris)-1)+")", args...)
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
	changed, err := t.Query(tx, c, withData, "collection_id = ? AND version > ?", c.ID, since)
	if err != nil {
		return nil, err
	}
	ch := &Changes{Changed: changed, State: state}
	if since > 0 {
		if ch.Deleted, ch.DeletedAt, err = davsql.Deleted(tx, int64(c.ID), since); err != nil {
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
