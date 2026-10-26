package calendar

import (
	"bytes"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// backend serves events to the DAV core. Events are stored as rows
// (internal/dav/ical models) and their iCalendar text is generated on read.
type backend struct {
	dav.Table
	p     *Plugin
	inbox *inbox
}

func newBackend(p *Plugin) *backend {
	b := &backend{p: p, inbox: newInbox(p)}
	b.Table = dav.Table{Name: "events", Component: dav.VEVENT, DB: p.DB, Render: func(q davsql.Querier, _ *dav.Collection, ids []int64) (map[int64][]byte, error) {
		return tables.RenderIDs(q, ids)
	}}
	return b
}

var (
	_ dav.Backend          = (*backend)(nil)
	_ dav.TimeRangeQuerier = (*backend)(nil)
	_ dav.BusyRedactor     = (*backend)(nil)
	_ dav.TimezoneObserver = (*backend)(nil)
	_ dav.InboxProvider    = (*backend)(nil)
	_ dav.UIDLocator       = (*backend)(nil)
	_ dav.FreeBusyQuerier  = (*backend)(nil)
)

// Inbox implements dav.InboxProvider: schedule inboxes live with events.
func (b *backend) Inbox() dav.InboxStore { return b.inbox }

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
	o, err := ical.Decode(req.Object, ical.VEVENT)
	if err != nil {
		return nil, fmt.Errorf("decode event %s: %w", req.URI, err)
	}
	var added int64
	for _, a := range req.Attachments {
		added += int64(len(a.Data))
	}
	if added > 0 {
		if err := b.p.ctx.QuotaCheck(owner, added); err != nil {
			return nil, err
		}
	}
	now := b.p.ctx.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("put event: %w", err)
	}
	defer tx.Rollback()
	st, err := tables.Store(tx, int64(c.ID), zoneOf(c), req.URI, o, req.Conditions, now)
	if err != nil {
		return nil, err
	}
	// Store has bumped the version, so tx holds the write lock that
	// collectAttachments takes to remove files.
	if err := storeAttachments(tx, b.p.attachmentDir(owner), req.Attachments, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("put event: %w", err)
	}
	b.p.collected(owner)
	return &dav.PutResult{Version: st.Version, Created: st.Created, Data: st.Data}, nil
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
	if err := tables.Delete(tx, int64(c.ID), uri, cond, b.p.ctx.Now().Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete event: %w", err)
	}
	b.p.collected(owner)
	return nil
}

// InRange narrows a time-range query to events whose instances may overlap
// it, from the indexed bounds; the client filters instances exactly.
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
	return b.Query(db, c, withData, `collection_id = ? AND start_utc < ? AND (range_end_utc IS NULL OR range_end_utc >= ?)`, c.ID, hi, lo)
}

// Redact shows a free/busy-only sharee when the owner is busy, nothing else.
func (b *backend) Redact(c *dav.Collection, r *dav.Resource) error {
	comps, err := contentline.Parse(bytes.NewReader(r.Data))
	if err != nil {
		return fmt.Errorf("redact event %s: %w", r.URI, err)
	}
	o, err := ical.Decode(comps[0], ical.VEVENT)
	if err != nil {
		return fmt.Errorf("redact event %s: %w", r.URI, err)
	}
	busy := o.Busy()
	var out bytes.Buffer
	if err := contentline.Write(&out, []*contentline.Component{busy.Encode(busy.IANAZones)}); err != nil {
		return fmt.Errorf("redact event %s: %w", r.URI, err)
	}
	r.Data = out.Bytes()
	return nil
}

func (b *backend) TimezoneChanged(owner string, c *dav.Collection) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	return RecomputeEventTimes(db, int64(c.ID), zoneOf(c), b.p.ctx.Now())
}

// Purge removes a deleted calendar's events, the time zones they used and
// the attachments only they named.
func (b *backend) Purge(owner string, id dav.CollectionID) error {
	db, err := b.p.DB(owner)
	if err != nil {
		return err
	}
	if err := tables.Purge(db, int64(id)); err != nil {
		return err
	}
	b.p.collected(owner)
	return nil
}
