package calendar

import (
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
)

// backend serves events to the DAV core. The ICS is stored and served
// verbatim; the indexed columns are derived from it on write.
type backend struct {
	dav.Table
	p *Plugin
}

func newBackend(p *Plugin) *backend {
	return &backend{Table: dav.Table{Name: "events", Data: "ics_data", Component: dav.VEVENT, DB: p.DB}, p: p}
}

var (
	_ dav.Backend          = (*backend)(nil)
	_ dav.TimeRangeQuerier = (*backend)(nil)
	_ dav.BusyRedactor     = (*backend)(nil)
	_ dav.TimezoneObserver = (*backend)(nil)
)

func (b *backend) Key() string                 { return "e" }
func (b *backend) Components() []dav.Component { return []dav.Component{dav.VEVENT} }

func zoneOf(c *dav.Collection) *time.Location {
	if loc := LoadTimezone(c.Timezone); loc != nil {
		return loc
	}
	return time.UTC
}

func (b *backend) Put(owner string, c *dav.Collection, req *dav.PutRequest) (*dav.PutResult, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	ics := string(req.Data)
	fields, err := ParseVEventIn(ics, zoneOf(c))
	if err != nil {
		return nil, fmt.Errorf("parse event %s: %w", req.URI, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("put event: %w", err)
	}
	defer tx.Rollback()
	_, version, created, err := putEvent(tx, int64(c.ID), req.URI, req.UID, ics, fields, req.Conditions, b.p.ctx.Now().Unix())
	var conflict *errUIDConflict
	if errors.As(err, &conflict) {
		return nil, &dav.UIDConflict{URI: conflict.uri}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("put event: %w", err)
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
		return fmt.Errorf("delete event: %w", err)
	}
	defer tx.Rollback()
	if err := deleteEvent(tx, int64(c.ID), uri, cond, b.p.ctx.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *backend) InRange(owner string, c *dav.Collection, comp dav.Component, start, end time.Time, withData bool) ([]dav.Resource, error) {
	db, err := b.p.DB(owner)
	if err != nil {
		return nil, err
	}
	// An open bound is unlimited.
	lo, hi := int64(-1<<62), int64(1<<62)
	if !start.IsZero() {
		lo = start.Unix()
	}
	if !end.IsZero() {
		hi = end.Unix()
	}
	return b.Query(db, withData, `collection_id = ? AND dtstart < ?
		AND (dtend > ? OR ics_data LIKE '%RRULE:%' OR ics_data LIKE '%RDATE%')`, c.ID, hi, lo)
}

// Redact shows a free/busy-only sharee when the owner is busy, nothing else.
func (b *backend) Redact(c *dav.Collection, r *dav.Resource) error {
	f, err := ParseVEventIn(string(r.Data), zoneOf(c))
	if err != nil {
		return fmt.Errorf("redact event %s: %w", r.URI, err)
	}
	r.Data = []byte(GenerateICSFull(f.UID, "Busy", "", "", f.DTStart, f.DTEnd, f.IsAllDay, f.RRule, nil, nil, time.Unix(0, 0).UTC()))
	return nil
}

func (b *backend) TimezoneChanged(owner string, c *dav.Collection) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	return RecomputeEventTimes(db, int64(c.ID), zoneOf(c))
}
