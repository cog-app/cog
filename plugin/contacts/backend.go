package contacts

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// backend serves cards to the DAV core. The vCard is stored and served
// verbatim; the indexed columns are derived from it on write.
type backend struct {
	dav.Table
	p *Plugin
}

var _ dav.Backend = (*backend)(nil)

func newBackend(p *Plugin) *backend {
	return &backend{Table: dav.Table{Name: "cards", Data: "vcard_data", Component: dav.VCARD, DB: p.DB}, p: p}
}

func (b *backend) Key() string { return "c" }

func (b *backend) Put(owner string, c *dav.Collection, req *dav.PutRequest) (*dav.PutResult, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	vcard := string(req.Data)
	fields, err := ParseVCard(vcard)
	if err != nil {
		return nil, fmt.Errorf("parse card %s: %w", req.URI, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("put card: %w", err)
	}
	defer tx.Rollback()
	_, version, created, err := putCard(tx, int64(c.ID), req.URI, req.UID, vcard, fields, req.Conditions, b.p.ctx.Now().Unix())
	var conflict *errUIDConflict
	if errors.As(err, &conflict) {
		return nil, &dav.UIDConflict{URI: conflict.uri}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("put card: %w", err)
	}
	return &dav.PutResult{Version: version, Created: created, Data: req.Data}, nil
}

func (b *backend) Delete(owner string, c *dav.Collection, uri string, cond davsql.Conditions) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete card: %w", err)
	}
	defer tx.Rollback()
	if err := deleteCard(tx, int64(c.ID), uri, cond, b.p.ctx.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}
