package tasks

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// backend serves tasks (VTODO) and journals (VJOURNAL) to the DAV core.
// Both are stored as rows in one table (internal/dav/icalsql), which names
// each resource's component, and their text is generated on read.
type backend struct {
	dav.Table
	p *Plugin
}

func newBackend(p *Plugin) *backend {
	b := &backend{p: p}
	b.Table = dav.Table{Name: "tasks", DB: p.DB, Render: func(q davsql.Querier, _ *dav.Collection, ids []int64) (map[int64][]byte, error) {
		return tables.RenderIDs(q, ids)
	}, Load: func(q davsql.Querier, _ *dav.Collection, ids []int64) (map[int64]dav.Model, error) {
		objs, err := tables.Load(q, ids)
		if err != nil {
			return nil, err
		}
		out := make(map[int64]dav.Model, len(objs))
		for id, o := range objs {
			out[id] = dav.Model{Object: o}
		}
		return out, nil
	}}
	return b
}

var (
	_ dav.Backend          = (*backend)(nil)
	_ dav.TimezoneObserver = (*backend)(nil)
	_ dav.ModelReader      = (*backend)(nil)
)

func (b *backend) Key() string { return "t" }
func (b *backend) Components() []dav.Component {
	return []dav.Component{dav.VTODO, dav.VJOURNAL}
}

func (b *backend) Put(owner string, c *dav.Collection, req *dav.PutRequest) (*dav.PutResult, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	o, err := ical.Decode(req.Object, ical.Kind(req.Component))
	if err != nil {
		return nil, fmt.Errorf("decode %s %s: %w", req.Component, req.URI, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("put task: %w", err)
	}
	defer tx.Rollback()
	l := listFrom(c)
	st, err := tables.Store(tx, l.ID, l.Zone(), req.URI, o, req.Conditions, b.p.ctx.Now().Unix())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("put task: %w", err)
	}
	return &dav.PutResult{Version: st.Version, Created: st.Created, Data: st.Data}, nil
}

// Delete removes one task or journal entry. Over DAV a task's subtasks are
// separate resources, so they stay, and become roots.
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
	if err := tables.Delete(tx, int64(c.ID), uri, cond, b.p.ctx.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// TimezoneChanged re-derives the times of a list's tasks, since floating
// times are read in the collection's zone.
func (b *backend) TimezoneChanged(owner string, c *dav.Collection) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	l := listFrom(c)
	return tables.RecomputeCollection(db, l.ID, l.Zone(), b.p.ctx.Now())
}

// Purge removes a deleted collection's tasks, journals and time zones.
func (b *backend) Purge(owner string, id dav.CollectionID) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	return tables.Purge(db, int64(id))
}
