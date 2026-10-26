package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// backend serves tasks (VTODO) and journals (VJOURNAL) to the DAV core. The
// ICS is stored and served verbatim; the task columns are derived from it on
// write. Both tables share one counter per collection.
type backend struct{ p *Plugin }

var _ dav.Backend = (*backend)(nil)

func (b *backend) Key() string { return "t" }
func (b *backend) Components() []dav.Component {
	return []dav.Component{dav.VTODO, dav.VJOURNAL}
}

// resources selects both tables as one; where is applied to each.
func resources(withData bool, where string) string {
	data := "NULL"
	if withData {
		data = "ics_data"
	}
	cols := "uri, uid, version, length(ics_data), " + data
	return `SELECT ` + cols + `, 'VTODO' FROM tasks WHERE ` + where +
		` UNION ALL SELECT ` + cols + `, 'VJOURNAL' FROM journals WHERE ` + where
}

func scanResources(rows *sql.Rows) ([]dav.Resource, error) {
	defer rows.Close()
	var out []dav.Resource
	for rows.Next() {
		var r dav.Resource
		var data sql.NullString
		if err := rows.Scan(&r.URI, &r.UID, &r.Version, &r.Size, &data, &r.Component); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		if data.Valid {
			r.Data = []byte(data.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (b *backend) List(owner string, c *dav.Collection, withData bool) ([]dav.Resource, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(resources(withData, "collection_id = ?"), c.ID, c.ID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return scanResources(rows)
}

func (b *backend) Get(owner string, c *dav.Collection, uris []string, withData bool) ([]dav.Resource, error) {
	if len(uris) == 0 {
		return nil, nil
	}
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	in := "collection_id = ? AND uri IN (?" + strings.Repeat(",?", len(uris)-1) + ")"
	args := []any{c.ID}
	for _, u := range uris {
		args = append(args, u)
	}
	rows, err := db.Query(resources(withData, in), append(args, args...)...)
	if err != nil {
		return nil, fmt.Errorf("get tasks: %w", err)
	}
	return scanResources(rows)
}

func (b *backend) FindUID(owner string, c *dav.Collection, uid string) (string, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return "", err
	}
	var uri string
	err = db.QueryRow(`SELECT uri FROM tasks WHERE collection_id = ? AND uid = ?
		UNION ALL SELECT uri FROM journals WHERE collection_id = ? AND uid = ? LIMIT 1`, c.ID, uid, c.ID, uid).Scan(&uri)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find task %s: %w", uid, err)
	}
	return uri, nil
}

func (b *backend) Put(owner string, c *dav.Collection, req *dav.PutRequest) (*dav.PutResult, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	now := b.p.ctx.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("put task: %w", err)
	}
	defer tx.Rollback()

	// A URI keeps its component type; the core checks other backends, and
	// this checks the other table.
	other := "journals"
	if req.Component == dav.VJOURNAL {
		other = "tasks"
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM `+other+` WHERE collection_id = ? AND uri = ?`, c.ID, req.URI).Scan(&n); err != nil {
		return nil, fmt.Errorf("put task: %w", err)
	}
	if n > 0 {
		return nil, fmt.Errorf("%s exists as another component type", req.URI)
	}

	var version int64
	var created bool
	if req.Component == dav.VJOURNAL {
		version, created, err = putJournal(tx, int64(c.ID), req, now)
	} else {
		version, created, err = b.putTodo(tx, int64(c.ID), req, now)
	}
	var conflict *errUIDConflict
	if errors.As(err, &conflict) {
		return nil, &dav.UIDConflict{URI: conflict.uri}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("put task: %w", err)
	}
	return &dav.PutResult{Version: version, Created: created, Data: req.Data}, nil
}

func unixPtr(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	u := t.Unix()
	return &u
}

func (b *backend) putTodo(tx *sql.Tx, listID int64, req *dav.PutRequest, now int64) (int64, bool, error) {
	f, err := ParseVTodo(string(req.Data))
	if err != nil {
		return 0, false, fmt.Errorf("parse task %s: %w", req.URI, err)
	}
	row := taskRow{
		listID: listID, uid: req.UID, uri: req.URI, summary: f.Summary, description: f.Description,
		priority: f.Priority, percentComplete: f.PercentComplete, status: f.Status,
		completedAt: unixPtr(f.Completed), dueAt: unixPtr(f.Due), startAt: unixPtr(f.Start), ics: string(req.Data),
	}
	if row.status == "" {
		row.status = "NEEDS-ACTION"
	}
	// RELATED-TO names the parent by UID; it may arrive later in a sync,
	// in which case the task is a root until it is written again.
	if f.RelatedTo != "" {
		var pid int64
		switch err := tx.QueryRow(`SELECT id FROM tasks WHERE collection_id = ? AND uid = ?`, listID, f.RelatedTo).Scan(&pid); {
		case err == nil:
			row.parentID = &pid
		case !errors.Is(err, sql.ErrNoRows):
			return 0, false, fmt.Errorf("find parent task: %w", err)
		}
	}
	_, version, created, err := putTask(tx, row, req.Conditions, now)
	return version, created, err
}

func putJournal(tx *sql.Tx, listID int64, req *dav.PutRequest, now int64) (int64, bool, error) {
	var cur *int64
	var curVersion int64
	switch err := tx.QueryRow(`SELECT version FROM journals WHERE collection_id = ? AND uri = ?`, listID, req.URI).Scan(&curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, fmt.Errorf("read journal %s: %w", req.URI, err)
	}
	if err := req.Conditions.Check(cur); err != nil {
		return 0, false, dav.ErrPrecondition
	}
	if other, err := otherURI(tx, listID, req.UID, req.URI); err != nil {
		return 0, false, err
	} else if other != "" {
		return 0, false, &errUIDConflict{other}
	}
	summary := ""
	for _, sub := range req.Object.Components {
		if p := sub.Prop("SUMMARY"); sub.Is("VJOURNAL") && p != nil {
			summary = unescapeICS(p.Value)
			break
		}
	}
	version, err := davsql.Bump(tx, listID)
	if err != nil {
		return 0, false, err
	}
	if _, err := tx.Exec(`INSERT INTO journals (collection_id, uid, uri, version, summary, ics_data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET uid = excluded.uid, version = excluded.version,
			summary = excluded.summary, ics_data = excluded.ics_data, updated_at = excluded.updated_at`,
		listID, req.UID, req.URI, version, summary, string(req.Data), now, now); err != nil {
		return 0, false, fmt.Errorf("store journal %s: %w", req.URI, err)
	}
	if err := davsql.Revive(tx, listID, req.URI); err != nil {
		return 0, false, err
	}
	return version, cur == nil, nil
}

// Delete removes one task or journal. Over DAV a task's subtasks are separate
// resources, so they stay, as roots.
func (b *backend) Delete(owner string, c *dav.Collection, uri string, cond davsql.Conditions) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	defer tx.Rollback()
	var table string
	var version int64
	err = tx.QueryRow(`SELECT 'tasks', version FROM tasks WHERE collection_id = ? AND uri = ?
		UNION ALL SELECT 'journals', version FROM journals WHERE collection_id = ? AND uri = ?`,
		c.ID, uri, c.ID, uri).Scan(&table, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return dav.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read task %s: %w", uri, err)
	}
	if err := cond.Check(&version); err != nil {
		return dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM `+table+` WHERE collection_id = ? AND uri = ?`, c.ID, uri); err != nil {
		return fmt.Errorf("delete task %s: %w", uri, err)
	}
	if _, err := davsql.Delete(tx, int64(c.ID), uri, b.p.ctx.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *backend) Changes(owner string, c *dav.Collection, since int64, withData bool) (*dav.Changes, error) {
	db, err := b.p.DB(owner)
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
	rows, err := tx.Query(resources(withData, "collection_id = ? AND version > ?"), c.ID, since, c.ID, since)
	if err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	changed, err := scanResources(rows)
	if err != nil {
		return nil, err
	}
	ch := &dav.Changes{Changed: changed, State: state}
	if since > 0 {
		if ch.Deleted, err = davsql.Deleted(tx, int64(c.ID), since); err != nil {
			return nil, err
		}
	}
	return ch, nil
}

func (b *backend) States(owner string, ids []dav.CollectionID) (map[dav.CollectionID]davsql.State, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	return dav.States(db, ids)
}

func (b *backend) Prune(owner string, c *dav.Collection, cutoff time.Time) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	return davsql.Prune(db, int64(c.ID), cutoff.Unix())
}

func (b *backend) Purge(owner string, id dav.CollectionID) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("purge task list: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"tasks", "journals"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE collection_id = ?`, id); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	if err := davsql.Purge(tx, int64(id)); err != nil {
		return err
	}
	return tx.Commit()
}
