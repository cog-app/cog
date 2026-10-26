// Package icalsql stores calendar objects (internal/dav/ical) as rows: one
// row per resource, one per component (the master and each override), and
// child rows for attendees, alarms and relations. Plugins that hold calendar
// data (events, tasks and journals) create these tables with the same
// columns under their own names and store through Tables, so loading,
// writing, time zones and derived times work the same everywhere.
//
// The iCalendar text is generated from the rows on read; nothing stores it.
package icalsql

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// Tables names one plugin's tables. Every plugin also has vtimezones, which
// holds clients' own VTIMEZONEs per collection.
type Tables struct {
	Objects    string // one row per resource
	Components string // one row per component; object_id references Objects
	Attendees  string // component_id references Components
	Alarms     string
	// Relations holds RELATED-TO; "" for a plugin that keeps it among the
	// leftovers.
	Relations string
	// Attachments holds ATTACH; "" for a plugin that keeps it among the
	// leftovers.
	Attachments string
	// AfterWrite, when set, runs in the transaction after an object is
	// stored or its derived times are recomputed; the calendar keeps its
	// reminders with it.
	AfterWrite func(tx *sql.Tx, id int64, o *ical.Object, z ical.Zones, now int64) error
}

// Render writes an object as the text Cog serves.
func Render(o *ical.Object) ([]byte, error) {
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}); err != nil {
		return nil, fmt.Errorf("write %s: %w", o.UID, err)
	}
	return b.Bytes(), nil
}

// RenderIDs generates the text of stored objects, for dav.Table.Render.
func (t *Tables) RenderIDs(q davsql.Querier, ids []int64) (map[int64][]byte, error) {
	objs, err := t.Load(q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]byte, len(objs))
	for id, o := range objs {
		if out[id], err = Render(o); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Placeholders returns n comma-separated "?".
func Placeholders(n int) string { return "?" + strings.Repeat(",?", n-1) }

// Anys converts a slice to query arguments.
func Anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// Unmarshal reads a JSON column; "" is the absent value.
func Unmarshal(s string, v any) error {
	if s == "" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

// marshal writes a JSON column, with "" for an absent value.
func marshal(v any) (string, error) {
	switch x := v.(type) {
	case ical.DateTime:
		if x.IsZero() {
			return "", nil
		}
	case *ical.Organizer:
		if x == nil {
			return "", nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if s := string(b); s != "null" {
		return s, nil
	}
	return "", nil
}

func orEmpty(s, empty string) string {
	if s == "" {
		return empty
	}
	return s
}

// --- Loading ---

// loadChunk bounds the IDs in one IN list.
const loadChunk = 500

// Load reads objects by ID, in a fixed number of queries per chunk of IDs.
// Each object's Zones holds its collection's stored VTIMEZONEs.
func (t *Tables) Load(q davsql.Querier, ids []int64) (map[int64]*ical.Object, error) {
	out := make(map[int64]*ical.Object, len(ids))
	for chunk := range slices.Chunk(ids, loadChunk) {
		if err := t.loadChunk(q, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (t *Tables) loadChunk(q davsql.Querier, ids []int64, out map[int64]*ical.Object) error {
	in := Placeholders(len(ids))
	args := Anys(ids)

	collOf := map[int64]int64{}
	kindOf := map[int64]ical.Kind{}
	rows, err := q.Query(`SELECT id, collection_id, component, uid, props, comps FROM `+t.Objects+` WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return fmt.Errorf("load %s: %w", t.Objects, err)
	}
	for rows.Next() {
		var id, collID int64
		var kind, props, comps string
		var o ical.Object
		if err := rows.Scan(&id, &collID, &kind, &o.UID, &props, &comps); err != nil {
			rows.Close()
			return fmt.Errorf("scan %s: %w", t.Objects, err)
		}
		if err := Unmarshal(props, &o.Props); err != nil {
			rows.Close()
			return fmt.Errorf("%s %d props: %w", t.Objects, id, err)
		}
		if err := Unmarshal(comps, &o.Others); err != nil {
			rows.Close()
			return fmt.Errorf("%s %d comps: %w", t.Objects, id, err)
		}
		out[id] = &o
		collOf[id] = collID
		kindOf[id] = ical.Kind(kind)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load %s: %w", t.Objects, err)
	}

	colls := make([]int64, 0, 1)
	for _, c := range collOf {
		if !slices.Contains(colls, c) {
			colls = append(colls, c)
		}
	}
	zones, err := StoredZones(q, colls)
	if err != nil {
		return err
	}
	for id, o := range out {
		o.Zones = zones[collOf[id]]
	}

	byComp := map[int64]*ical.Component{}
	rows, err = q.Query(`SELECT `+componentColumns+` FROM `+t.Components+` WHERE object_id IN (`+in+`) ORDER BY object_id, position`, args...)
	if err != nil {
		return fmt.Errorf("load %s: %w", t.Components, err)
	}
	for rows.Next() {
		id, objID, e, err := scanComponent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if o := out[objID]; o != nil {
			e.Kind = kindOf[objID]
			o.Components = append(o.Components, e)
			byComp[id] = e
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load %s: %w", t.Components, err)
	}

	child := func(table, cols string) (*sql.Rows, error) {
		return q.Query(`SELECT a.component_id, `+cols+` FROM `+table+` a JOIN `+t.Components+` v ON v.id = a.component_id
			WHERE v.object_id IN (`+in+`) ORDER BY a.component_id, a.position`, args...)
	}

	rows, err = child(t.Attendees, `a.address, a.cn, a.partstat, a.role, a.rsvp, a.cutype, a.params`)
	if err != nil {
		return fmt.Errorf("load attendees: %w", err)
	}
	for rows.Next() {
		var cid int64
		var a ical.Attendee
		var params string
		if err := rows.Scan(&cid, &a.Address, &a.CN, &a.PartStat, &a.Role, &a.RSVP, &a.CUType, &params); err != nil {
			rows.Close()
			return fmt.Errorf("scan attendee: %w", err)
		}
		if err := Unmarshal(params, &a.Params); err != nil {
			rows.Close()
			return fmt.Errorf("attendee params: %w", err)
		}
		if e := byComp[cid]; e != nil {
			e.Attendees = append(e.Attendees, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load attendees: %w", err)
	}

	rows, err = child(t.Alarms, `a.action, a.trigger, a.related, a.absolute, a.repeat, a.repeat_duration, a.description, a.summary, a.leftovers`)
	if err != nil {
		return fmt.Errorf("load alarms: %w", err)
	}
	for rows.Next() {
		var cid int64
		var a ical.Alarm
		var repeat sql.NullInt64
		var left string
		if err := rows.Scan(&cid, &a.Action, &a.Trigger, &a.Related, &a.Absolute, &repeat, &a.RepeatDuration,
			&a.Description, &a.Summary, &left); err != nil {
			rows.Close()
			return fmt.Errorf("scan alarm: %w", err)
		}
		if repeat.Valid {
			n := int(repeat.Int64)
			a.Repeat = &n
		}
		if err := Unmarshal(left, &a.Leftovers); err != nil {
			rows.Close()
			return fmt.Errorf("alarm leftovers: %w", err)
		}
		if e := byComp[cid]; e != nil {
			e.Alarms = append(e.Alarms, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load alarms: %w", err)
	}

	if t.Attachments != "" {
		rows, err = child(t.Attachments, `a.value, a.inline, a.managed_id, a.fmttype, a.size, a.filename, a.params`)
		if err != nil {
			return fmt.Errorf("load attachments: %w", err)
		}
		for rows.Next() {
			var cid int64
			var a ical.Attachment
			var size sql.NullInt64
			var params string
			if err := rows.Scan(&cid, &a.Value, &a.Inline, &a.ManagedID, &a.FmtType, &size, &a.Filename, &params); err != nil {
				rows.Close()
				return fmt.Errorf("scan attachment: %w", err)
			}
			if size.Valid {
				a.Size = &size.Int64
			}
			if err := Unmarshal(params, &a.Params); err != nil {
				rows.Close()
				return fmt.Errorf("attachment params: %w", err)
			}
			if e := byComp[cid]; e != nil {
				e.Attachments = append(e.Attachments, a)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("load attachments: %w", err)
		}
	}

	if t.Relations == "" {
		return nil
	}
	rows, err = child(t.Relations, `a.reltype, a.related_uid, a.params`)
	if err != nil {
		return fmt.Errorf("load relations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var r ical.Relation
		var params string
		if err := rows.Scan(&cid, &r.Type, &r.UID, &params); err != nil {
			return fmt.Errorf("scan relation: %w", err)
		}
		if err := Unmarshal(params, &r.Params); err != nil {
			return fmt.Errorf("relation params: %w", err)
		}
		if e := byComp[cid]; e != nil {
			e.Relations = append(e.Relations, r)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load relations: %w", err)
	}
	return nil
}

// StoredZones reads the stored VTIMEZONEs of collections, by collection.
func StoredZones(q davsql.Querier, colls []int64) (map[int64]map[string]*contentline.Component, error) {
	out := map[int64]map[string]*contentline.Component{}
	if len(colls) == 0 {
		return out, nil
	}
	rows, err := q.Query(`SELECT collection_id, tzid, vtimezone FROM vtimezones WHERE collection_id IN (`+Placeholders(len(colls))+`)`, Anys(colls)...)
	if err != nil {
		return nil, fmt.Errorf("load time zones: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var collID int64
		var id, data string
		if err := rows.Scan(&collID, &id, &data); err != nil {
			return nil, fmt.Errorf("scan time zone: %w", err)
		}
		var vtz contentline.Component
		if err := json.Unmarshal([]byte(data), &vtz); err != nil {
			return nil, fmt.Errorf("time zone %s: %w", id, err)
		}
		if out[collID] == nil {
			out[collID] = map[string]*contentline.Component{}
		}
		out[collID][id] = &vtz
	}
	return out, rows.Err()
}

const componentColumns = `id, object_id, recurrence_id_local, recurrence_id_tzid, recurrence_id_is_date, this_and_future,
	summary, description, location, url, status, class, transp, priority, sequence, percent_complete,
	dtstart_local, dtstart_tzid, dtstart_is_date, dtend_local, dtend_tzid, dtend_is_date,
	due_local, due_tzid, due_is_date, duration, completed, rrule,
	rdates, exdates, dtstamp, created, last_modified, organizer, categories, sort_order, leftovers`

func nullInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func scanComponent(rows *sql.Rows) (int64, int64, *ical.Component, error) {
	var id, objID int64
	var e ical.Component
	var priority, sequence, percent, sortOrder sql.NullInt64
	var completed, rdates, exdates, dtstamp, created, lastMod, organizer, categories, left string
	if err := rows.Scan(&id, &objID, &e.RecurrenceID.Local, &e.RecurrenceID.TZID, &e.RecurrenceID.IsDate, &e.ThisAndFuture,
		&e.Summary, &e.Description, &e.Location, &e.URL, &e.Status, &e.Class, &e.Transp, &priority, &sequence, &percent,
		&e.Start.Local, &e.Start.TZID, &e.Start.IsDate, &e.End.Local, &e.End.TZID, &e.End.IsDate,
		&e.Due.Local, &e.Due.TZID, &e.Due.IsDate, &e.Duration, &completed, &e.RRule,
		&rdates, &exdates, &dtstamp, &created, &lastMod, &organizer, &categories, &sortOrder, &left); err != nil {
		return 0, 0, nil, fmt.Errorf("scan component: %w", err)
	}
	e.Priority, e.Sequence, e.PercentComplete, e.SortOrder = nullInt(priority), nullInt(sequence), nullInt(percent), nullInt(sortOrder)
	for _, f := range []struct {
		src string
		dst any
	}{
		{completed, &e.Completed}, {rdates, &e.RDates}, {exdates, &e.ExDates}, {dtstamp, &e.DTStamp}, {created, &e.Created},
		{lastMod, &e.LastModified}, {organizer, &e.Organizer}, {categories, &e.Categories}, {left, &e.Leftovers},
	} {
		if err := Unmarshal(f.src, f.dst); err != nil {
			return 0, 0, nil, fmt.Errorf("component %d: %w", id, err)
		}
	}
	return id, objID, &e, nil
}

// --- Writes ---
//
// Every write bumps the collection's version in the same transaction
// (davsql), whether it comes from DAV or the web UI, so sync tokens and ETags
// always reflect it.

// Stored reports a written object.
type Stored struct {
	ID      int64
	Version int64
	Created bool
	Data    []byte // the text served for this version
}

func bounds(o *ical.Object, z ical.Zones) (startUTC, endUTC any) {
	if start, end, unbounded, err := o.Bounds(z); err == nil {
		startUTC = start.Unix()
		if !unbounded {
			endUTC = end.Unix()
		}
	}
	return startUTC, endUTC
}

func utcOf(d ical.DateTime, z ical.Zones) any {
	if d.IsZero() {
		return nil
	}
	t, err := d.Time(z)
	if err != nil {
		return nil
	}
	return t.Unix()
}

// Store writes o at uri in a collection, inside tx, after checking cond, and
// renders the text served for it. floating is the collection's zone. It
// returns dav.ErrPrecondition when cond fails and a *dav.UIDConflict when
// another resource in the collection has o's UID. Columns the shared layout
// does not know (the calendar's colour) are kept.
func (t *Tables) Store(tx *sql.Tx, collID int64, floating *time.Location, uri string, o *ical.Object, cond davsql.Conditions, now int64) (*Stored, error) {
	var cur *int64
	var curID, curVersion int64
	switch err := tx.QueryRow(`SELECT id, version FROM `+t.Objects+` WHERE collection_id = ? AND uri = ?`, collID, uri).Scan(&curID, &curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read %s: %w", uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return nil, dav.ErrPrecondition
	}
	var other string
	switch err := tx.QueryRow(`SELECT uri FROM `+t.Objects+` WHERE collection_id = ? AND uid = ? AND uri != ?`, collID, o.UID, uri).Scan(&other); {
	case err == nil:
		return nil, &dav.UIDConflict{URI: other}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("check UID %s: %w", o.UID, err)
	}
	if len(o.Components) == 0 {
		return nil, ical.ErrNoComponent
	}

	// Zones the object names without defining, defined by an earlier write.
	known, err := StoredZones(tx, []int64{collID})
	if err != nil {
		return nil, err
	}
	sent := o.Zones
	o.Zones = map[string]*contentline.Component{}
	for id, vtz := range known[collID] {
		o.Zones[id] = vtz
	}
	for id, vtz := range sent {
		o.Zones[id] = vtz
	}
	z := ical.ZonesFor(o, floating)

	// The master first: web UIs read position 0.
	o.SortMaster()
	startUTC, endUTC := bounds(o, z)
	data, err := Render(o)
	if err != nil {
		return nil, err
	}
	props, err := marshal(o.Props)
	if err != nil {
		return nil, err
	}
	comps, err := marshal(o.Others)
	if err != nil {
		return nil, err
	}

	version, err := davsql.Bump(tx, collID)
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO `+t.Objects+` (collection_id, component, uid, uri, version, size, start_utc, range_end_utc, props, comps, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			component = excluded.component, uid = excluded.uid, version = excluded.version, size = excluded.size,
			start_utc = excluded.start_utc, range_end_utc = excluded.range_end_utc,
			props = excluded.props, comps = excluded.comps, updated_at = excluded.updated_at
		RETURNING id`,
		collID, string(o.Components[0].Kind), o.UID, uri, version, len(data), startUTC, endUTC,
		orEmpty(props, "[]"), orEmpty(comps, "[]"), now, now).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("store %s: %w", uri, err)
	}
	if _, err := tx.Exec(`DELETE FROM `+t.Components+` WHERE object_id = ?`, id); err != nil {
		return nil, fmt.Errorf("replace %s: %w", uri, err)
	}
	for pos, e := range o.Components {
		if err := t.insertComponent(tx, id, pos, e, z); err != nil {
			return nil, fmt.Errorf("store %s: %w", uri, err)
		}
	}
	var redefined []string
	for tzid, vtz := range sent {
		js, err := json.Marshal(vtz)
		if err != nil {
			return nil, err
		}
		// A definition that is new or different changes the times of the
		// other objects naming this TZID, which were read with the old one
		// (or, with none, in the collection's zone).
		if old, ok := known[collID][tzid]; !ok || !sameJSON(old, js) {
			redefined = append(redefined, tzid)
		}
		if _, err := tx.Exec(`INSERT INTO vtimezones (collection_id, tzid, vtimezone) VALUES (?, ?, ?)
			ON CONFLICT(collection_id, tzid) DO UPDATE SET vtimezone = excluded.vtimezone`, collID, tzid, string(js)); err != nil {
			return nil, fmt.Errorf("store time zone %s: %w", tzid, err)
		}
	}
	if len(redefined) > 0 {
		others, err := t.usersOf(tx, collID, id, redefined)
		if err != nil {
			return nil, err
		}
		if err := t.Recompute(tx, others, floating, now); err != nil {
			return nil, err
		}
	}
	if err := davsql.Revive(tx, collID, uri); err != nil {
		return nil, err
	}
	if t.AfterWrite != nil {
		if err := t.AfterWrite(tx, id, o, z, now); err != nil {
			return nil, err
		}
	}
	return &Stored{ID: id, Version: version, Created: cur == nil, Data: data}, nil
}

// sameJSON reports whether a stored component encodes to js.
func sameJSON(c *contentline.Component, js []byte) bool {
	old, err := json.Marshal(c)
	return err == nil && bytes.Equal(old, js)
}

func intOrNil(n *int) any {
	if n == nil {
		return nil
	}
	return *n
}

// isParent marks the relation that names a component's parent: its first
// PARENT relation (ical.Component.Parent).
func isParent(rels []ical.Relation, i int) bool {
	for j := range rels {
		if rels[j].IsParent() {
			return j == i
		}
	}
	return false
}

func (t *Tables) insertComponent(tx *sql.Tx, objID int64, pos int, e *ical.Component, z ical.Zones) error {
	var endUTC any
	if _, end, err := e.Span(z); err == nil {
		endUTC = end.Unix()
	}
	js := make([]string, 0, 9)
	for _, v := range []any{e.Completed, e.RDates, e.ExDates, e.DTStamp, e.Created, e.LastModified, e.Organizer, e.Categories, e.Leftovers} {
		s, err := marshal(v)
		if err != nil {
			return err
		}
		js = append(js, s)
	}
	var cid int64
	err := tx.QueryRow(`INSERT INTO `+t.Components+` (object_id, position, recurrence_id_local, recurrence_id_tzid, recurrence_id_is_date, this_and_future,
			summary, description, location, url, status, class, transp, priority, sequence, percent_complete,
			dtstart_local, dtstart_tzid, dtstart_is_date, dtstart_utc, dtend_local, dtend_tzid, dtend_is_date,
			due_local, due_tzid, due_is_date, due_utc, end_utc, duration, completed, completed_utc, rrule,
			rdates, exdates, dtstamp, created, last_modified, organizer, categories, sort_order, leftovers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		objID, pos, e.RecurrenceID.Local, e.RecurrenceID.TZID, e.RecurrenceID.IsDate, e.ThisAndFuture,
		e.Summary, e.Description, e.Location, e.URL, e.Status, e.Class, e.Transp, intOrNil(e.Priority), intOrNil(e.Sequence), intOrNil(e.PercentComplete),
		e.Start.Local, e.Start.TZID, e.Start.IsDate, utcOf(e.Start, z), e.End.Local, e.End.TZID, e.End.IsDate,
		e.Due.Local, e.Due.TZID, e.Due.IsDate, utcOf(e.Due, z), endUTC, e.Duration, js[0], utcOf(e.Completed, z), e.RRule,
		orEmpty(js[1], "[]"), orEmpty(js[2], "[]"), js[3], js[4], js[5], js[6], orEmpty(js[7], "[]"), intOrNil(e.SortOrder), orEmpty(js[8], "{}")).Scan(&cid)
	if err != nil {
		return fmt.Errorf("insert component: %w", err)
	}
	for i, a := range e.Attendees {
		params, err := marshal(a.Params)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO `+t.Attendees+` (component_id, position, address, email, cn, partstat, role, rsvp, cutype, params)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cid, i, a.Address, strings.ToLower(ical.Email(a.Address)), a.CN, a.PartStat, a.Role, a.RSVP, a.CUType, orEmpty(params, "[]")); err != nil {
			return fmt.Errorf("insert attendee: %w", err)
		}
	}
	for i, a := range e.Alarms {
		left, err := marshal(a.Leftovers)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO `+t.Alarms+` (component_id, position, action, trigger, related, absolute, repeat, repeat_duration, description, summary, leftovers)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cid, i, a.Action, a.Trigger, a.Related, a.Absolute, intOrNil(a.Repeat), a.RepeatDuration, a.Description, a.Summary, orEmpty(left, "{}")); err != nil {
			return fmt.Errorf("insert alarm: %w", err)
		}
	}
	if t.Attachments != "" {
		for i, a := range e.Attachments {
			params, err := marshal(a.Params)
			if err != nil {
				return err
			}
			var size any
			if a.Size != nil {
				size = *a.Size
			}
			if _, err := tx.Exec(`INSERT INTO `+t.Attachments+` (component_id, position, value, inline, managed_id, fmttype, size, filename, params)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				cid, i, a.Value, a.Inline, a.ManagedID, a.FmtType, size, a.Filename, orEmpty(params, "[]")); err != nil {
				return fmt.Errorf("insert attachment: %w", err)
			}
		}
	}
	if t.Relations == "" {
		return nil
	}
	for i, r := range e.Relations {
		params, err := marshal(r.Params)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO `+t.Relations+` (component_id, position, reltype, related_uid, is_parent, params) VALUES (?, ?, ?, ?, ?, ?)`,
			cid, i, r.Type, r.UID, isParent(e.Relations, i), orEmpty(params, "[]")); err != nil {
			return fmt.Errorf("insert relation: %w", err)
		}
	}
	return nil
}

// Delete removes the object at uri inside tx and leaves a tombstone. It
// returns dav.ErrNotFound when there is none and dav.ErrPrecondition when
// cond fails.
func (t *Tables) Delete(tx *sql.Tx, collID int64, uri string, cond davsql.Conditions, now int64) error {
	var version int64
	err := tx.QueryRow(`SELECT version FROM `+t.Objects+` WHERE collection_id = ? AND uri = ?`, collID, uri).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return dav.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", uri, err)
	}
	if err := cond.Check(&version); err != nil {
		return dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM `+t.Objects+` WHERE collection_id = ? AND uri = ?`, collID, uri); err != nil {
		return fmt.Errorf("delete %s: %w", uri, err)
	}
	_, err = davsql.Delete(tx, collID, uri, now)
	return err
}

// Purge removes a deleted collection's objects and the time zones they used.
func (t *Tables) Purge(db *sql.DB, collID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("purge collection: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{t.Objects, "vtimezones"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE collection_id = ?`, collID); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	if err := davsql.Purge(tx, collID); err != nil {
		return err
	}
	return tx.Commit()
}

// IDs runs a query returning one ID per row.
func IDs(q davsql.Querier, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("find rows: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("find rows: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Strings runs a query returning one string per row.
func Strings(q davsql.Querier, query string, args ...any) ([]string, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecomputeCollection re-derives a collection's indexed times after its zone
// changes, since floating times are read in that zone. The iCalendar data
// is unchanged, so versions and DAV clients are unaffected.
func (t *Tables) RecomputeCollection(db *sql.DB, collID int64, floating *time.Location, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("recompute times: %w", err)
	}
	defer tx.Rollback()
	ids, err := IDs(tx, `SELECT id FROM `+t.Objects+` WHERE collection_id = ?`, collID)
	if err != nil {
		return err
	}
	if err := t.Recompute(tx, ids, floating, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// Recompute re-derives the indexed times of objects, all in one collection
// whose zone is floating, from their stored rows, and runs AfterWrite.
func (t *Tables) Recompute(tx *sql.Tx, ids []int64, floating *time.Location, now int64) error {
	objs, err := t.Load(tx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		o := objs[id]
		if o == nil {
			continue
		}
		z := ical.ZonesFor(o, floating)
		startUTC, endUTC := bounds(o, z)
		if _, err := tx.Exec(`UPDATE `+t.Objects+` SET start_utc = ?, range_end_utc = ? WHERE id = ?`, startUTC, endUTC, id); err != nil {
			return fmt.Errorf("update %d times: %w", id, err)
		}
		cids, err := IDs(tx, `SELECT id FROM `+t.Components+` WHERE object_id = ? ORDER BY position`, id)
		if err != nil {
			return err
		}
		for i, cid := range cids {
			if i >= len(o.Components) {
				break
			}
			e := o.Components[i]
			var end any
			if _, et, err := e.Span(z); err == nil {
				end = et.Unix()
			}
			if _, err := tx.Exec(`UPDATE `+t.Components+` SET dtstart_utc = ?, due_utc = ?, completed_utc = ?, end_utc = ? WHERE id = ?`,
				utcOf(e.Start, z), utcOf(e.Due, z), utcOf(e.Completed, z), end, cid); err != nil {
				return fmt.Errorf("update %d times: %w", id, err)
			}
		}
		if t.AfterWrite != nil {
			if err := t.AfterWrite(tx, id, o, z, now); err != nil {
				return fmt.Errorf("after recomputing %d: %w", id, err)
			}
		}
	}
	return nil
}

// usersOf returns the other objects in a collection that use any of tzids,
// in their times or their RDATE and EXDATE values.
func (t *Tables) usersOf(tx *sql.Tx, collID, except int64, tzids []string) ([]int64, error) {
	var conds []string
	var args []any
	args = append(args, collID, except)
	for _, id := range tzids {
		needle, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		conds = append(conds, `v.dtstart_tzid = ? OR v.dtend_tzid = ? OR v.due_tzid = ? OR v.recurrence_id_tzid = ?
			OR instr(v.rdates, ?) > 0 OR instr(v.exdates, ?) > 0`)
		tag := `"tz":` + string(needle)
		args = append(args, id, id, id, id, tag, tag)
	}
	return IDs(tx, `SELECT DISTINCT v.object_id FROM `+t.Components+` v JOIN `+t.Objects+` e ON e.id = v.object_id
		WHERE e.collection_id = ? AND e.id != ? AND (`+strings.Join(conds, " OR ")+`)`, args...)
}
