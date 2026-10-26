package calendar

import (
	"bytes"
	"cmp"
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
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// Calendar is a DAV collection that holds events, as the calendar UI shows it.
// Collections belong to the DAV core (internal/dav); this is a view of one.
type Calendar struct {
	ID          int64  // dav.CollectionID
	UID         string // the collection's path name
	Name        string // display name
	Color       string
	Description string
	SortOrder   int
	Timezone    string // IANA zone for floating times and new events; "" = UTC
	OwnerID     string
}

func calendarFrom(c *dav.Collection) Calendar {
	return Calendar{
		ID: int64(c.ID), UID: c.Name, Name: c.Title(), Color: c.Color, Description: c.Description,
		SortOrder: c.SortOrder, Timezone: c.Timezone, OwnerID: c.OwnerID,
	}
}

// Zone returns the calendar's timezone, or UTC when unset or unknown.
func (c *Calendar) Zone() *time.Location {
	if loc := LoadTimezone(c.Timezone); loc != nil {
		return loc
	}
	return time.UTC
}

// Event is a stored event resource as the web UI shows it: the fields of its
// first component, which is the master when there is one, and the whole
// object when it was loaded.
type Event struct {
	ID         int64
	CalendarID int64 // dav.CollectionID
	UID        string
	URI        string // resource name in the collection
	Version    int64
	Color      string // Cog's own colour; not part of the iCalendar data

	Summary     string
	Location    string
	Description string
	DTStart     int64 // the component's own span; all-day values at UTC midnight
	DTEnd       int64
	IsAllDay    bool
	RRule       string
	// Occurrence is the start of the master occurrence an instance is, as
	// Unix time; 0 for an event that does not recur.
	Occurrence int64
	CreatedAt  int64
	UpdatedAt  int64

	Object *ical.Object // nil unless loaded
}

// EventWithCalendar extends Event with calendar metadata for display.
type EventWithCalendar struct {
	Event
	CalendarName     string
	CalendarColor    string
	CalendarTimezone string
}

// Zone returns the owning calendar's timezone, used for floating times.
func (e *EventWithCalendar) Zone() *time.Location {
	if loc := LoadTimezone(e.CalendarTimezone); loc != nil {
		return loc
	}
	return time.UTC
}

// zonesFor returns how an object's TZIDs map to locations: its own
// VTIMEZONEs for TZIDs Cog cannot resolve, and floating times in the
// calendar's zone. A VTIMEZONE whose rules cannot be read leaves its TZID in
// the floating zone.
func zonesFor(o *ical.Object, floating *time.Location) ical.Zones {
	z := ical.Zones{Floating: floating, Load: tz.Load}
	for id, vtz := range o.Zones {
		if loc, err := tz.FromRules(vtz); err == nil {
			if z.Custom == nil {
				z.Custom = map[string]*time.Location{}
			}
			z.Custom[id] = loc
		}
	}
	return z
}

// eventColumns selects an Event: the resource row and its first component.
const eventColumns = `e.id, e.collection_id, e.uid, e.uri, e.version, e.color, e.created_at, e.updated_at,
	v.summary, v.location, v.description, coalesce(v.dtstart_utc, 0), coalesce(v.end_utc, 0), v.dtstart_is_date, v.rrule`

const eventFrom = ` FROM events e JOIN vevents v ON v.event_id = e.id AND v.position = 0`

func scanEvent(row interface{ Scan(...any) error }) (*Event, error) {
	var e Event
	if err := row.Scan(&e.ID, &e.CalendarID, &e.UID, &e.URI, &e.Version, &e.Color, &e.CreatedAt, &e.UpdatedAt,
		&e.Summary, &e.Location, &e.Description, &e.DTStart, &e.DTEnd, &e.IsAllDay, &e.RRule); err != nil {
		return nil, err
	}
	return &e, nil
}

func queryEvents(q davsql.Querier, where string, args ...any) ([]Event, error) {
	rows, err := q.Query(`SELECT `+eventColumns+eventFrom+` WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		events = append(events, *e)
	}
	return events, rows.Err()
}

// ListEvents returns a calendar's events in start order.
func ListEvents(db *sql.DB, calendarID int64) ([]Event, error) {
	return queryEvents(db, `e.collection_id = ? ORDER BY e.start_utc, e.id`, calendarID)
}

// GetEvent returns an event by row ID, with its object.
func GetEvent(db *sql.DB, id int64) (*Event, error) {
	return getEvent(db, `e.id = ?`, id)
}

// GetEventByUID returns a calendar's event with this UID, with its object.
func GetEventByUID(db *sql.DB, calendarID int64, uid string) (*Event, error) {
	return getEvent(db, `e.collection_id = ? AND e.uid = ?`, calendarID, uid)
}

func getEvent(db *sql.DB, where string, args ...any) (*Event, error) {
	ev, err := scanEvent(db.QueryRow(`SELECT `+eventColumns+eventFrom+` WHERE `+where, args...))
	if err != nil {
		return nil, err
	}
	objs, err := loadObjects(db, []int64{ev.ID})
	if err != nil {
		return nil, err
	}
	ev.Object = objs[ev.ID]
	return ev, nil
}

// inCalendars returns " AND <col> IN (...)" and its arguments, limiting a
// query to the given calendars. Rows of a deleted collection whose purge
// failed are never in the map, so they stay invisible.
func inCalendars(col string, cals map[int64]Calendar) (string, []any) {
	if len(cals) == 0 {
		return " AND 0", nil
	}
	args := make([]any, 0, len(cals))
	for id := range cals {
		args = append(args, id)
	}
	return " AND " + col + " IN (?" + strings.Repeat(",?", len(args)-1) + ")", args
}

func withCalendars(events []Event, cals map[int64]Calendar) []EventWithCalendar {
	out := make([]EventWithCalendar, 0, len(events))
	for _, e := range events {
		c := cals[e.CalendarID]
		out = append(out, EventWithCalendar{Event: e, CalendarName: c.Name, CalendarColor: c.Color, CalendarTimezone: c.Timezone})
	}
	return out
}

// EventsInRange returns the events in cals with an instance that may overlap
// [start, end), with their objects loaded for expansion.
func EventsInRange(db *sql.DB, cals map[int64]Calendar, start, end int64) ([]EventWithCalendar, error) {
	in, args := inCalendars("e.collection_id", cals)
	events, err := queryEvents(db, `e.start_utc < ? AND (e.range_end_utc IS NULL OR e.range_end_utc >= ?)`+in+`
		ORDER BY e.start_utc, e.id`, append([]any{end, start}, args...)...)
	if err != nil {
		return nil, err
	}
	if err := attachObjects(db, events); err != nil {
		return nil, err
	}
	return withCalendars(events, cals), nil
}

func attachObjects(db *sql.DB, events []Event) error {
	ids := make([]int64, len(events))
	for i := range events {
		ids[i] = events[i].ID
	}
	objs, err := loadObjects(db, ids)
	if err != nil {
		return err
	}
	for i := range events {
		events[i].Object = objs[events[i].ID]
	}
	return nil
}

// Instances returns an event's instances overlapping [start, end), each as
// a copy of the event with that instance's fields and times.
func Instances(ev EventWithCalendar, start, end time.Time) ([]EventWithCalendar, error) {
	if ev.Object == nil {
		return nil, fmt.Errorf("event %d: object not loaded", ev.ID)
	}
	ins, err := ev.Object.Instances(zonesFor(ev.Object, ev.Zone()), start, end)
	if err != nil {
		return nil, fmt.Errorf("expand event %s: %w", ev.UID, err)
	}
	out := make([]EventWithCalendar, 0, len(ins))
	for _, in := range ins {
		e := ev
		e.Summary, e.Location, e.Description = in.Event.Summary, in.Event.Location, in.Event.Description
		e.DTStart, e.DTEnd = in.Start.Unix(), in.End.Unix()
		e.IsAllDay = in.Event.Start.IsDate
		if !in.RecurrenceID.IsZero() {
			e.Occurrence = in.RecurrenceID.Unix()
		}
		out = append(out, e)
	}
	return out, nil
}

// upcomingHorizon bounds how far ahead UpcomingEvents looks.
const upcomingHorizon = 366 * 24 * time.Hour

// UpcomingEvents returns the next instances in cals that have not ended.
func UpcomingEvents(db *sql.DB, cals map[int64]Calendar, now int64, limit int) ([]EventWithCalendar, error) {
	from := time.Unix(now, 0)
	to := from.Add(upcomingHorizon)
	events, err := EventsInRange(db, cals, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	var out []EventWithCalendar
	for _, ev := range events {
		ins, err := Instances(ev, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, ins...)
	}
	slices.SortStableFunc(out, func(a, b EventWithCalendar) int { return cmp.Compare(a.DTStart, b.DTStart) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// --- Loading objects ---

func placeholders(n int) string { return "?" + strings.Repeat(",?", n-1) }

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// loadChunk bounds the IDs in one IN list.
const loadChunk = 500

// loadObjects reads the objects of events by ID, in a fixed number of
// queries per chunk of IDs. Each object's Zones holds its calendar's stored
// VTIMEZONEs.
func loadObjects(q davsql.Querier, ids []int64) (map[int64]*ical.Object, error) {
	out := make(map[int64]*ical.Object, len(ids))
	for chunk := range slices.Chunk(ids, loadChunk) {
		if err := loadChunkInto(q, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func loadChunkInto(q davsql.Querier, ids []int64, out map[int64]*ical.Object) error {
	in := placeholders(len(ids))
	args := anys(ids)

	calOf := map[int64]int64{}
	rows, err := q.Query(`SELECT id, collection_id, uid, props, comps FROM events WHERE id IN (`+in+`)`, args...)
	if err != nil {
		return fmt.Errorf("load events: %w", err)
	}
	for rows.Next() {
		var id, calID int64
		var o ical.Object
		var props, comps string
		if err := rows.Scan(&id, &calID, &o.UID, &props, &comps); err != nil {
			rows.Close()
			return fmt.Errorf("scan event: %w", err)
		}
		if err := unmarshal(props, &o.Props); err != nil {
			rows.Close()
			return fmt.Errorf("event %d props: %w", id, err)
		}
		if err := unmarshal(comps, &o.Comps); err != nil {
			rows.Close()
			return fmt.Errorf("event %d comps: %w", id, err)
		}
		out[id] = &o
		calOf[id] = calID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load events: %w", err)
	}

	zones, err := storedZones(q, calOf)
	if err != nil {
		return err
	}
	for id, o := range out {
		o.Zones = zones[calOf[id]]
	}

	byVevent := map[int64]*ical.Event{}
	rows, err = q.Query(`SELECT `+veventColumns+` FROM vevents WHERE event_id IN (`+in+`) ORDER BY event_id, position`, args...)
	if err != nil {
		return fmt.Errorf("load vevents: %w", err)
	}
	for rows.Next() {
		id, eventID, e, err := scanVevent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if o := out[eventID]; o != nil {
			o.Events = append(o.Events, e)
			byVevent[id] = e
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load vevents: %w", err)
	}

	rows, err = q.Query(`SELECT a.vevent_id, a.address, a.cn, a.partstat, a.role, a.rsvp, a.cutype, a.params
		FROM event_attendees a JOIN vevents v ON v.id = a.vevent_id
		WHERE v.event_id IN (`+in+`) ORDER BY a.vevent_id, a.position`, args...)
	if err != nil {
		return fmt.Errorf("load attendees: %w", err)
	}
	for rows.Next() {
		var vid int64
		var a ical.Attendee
		var params string
		if err := rows.Scan(&vid, &a.Address, &a.CN, &a.PartStat, &a.Role, &a.RSVP, &a.CUType, &params); err != nil {
			rows.Close()
			return fmt.Errorf("scan attendee: %w", err)
		}
		if err := unmarshal(params, &a.Params); err != nil {
			rows.Close()
			return fmt.Errorf("attendee params: %w", err)
		}
		if e := byVevent[vid]; e != nil {
			e.Attendees = append(e.Attendees, a)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load attendees: %w", err)
	}

	rows, err = q.Query(`SELECT a.vevent_id, a.action, a.trigger, a.related, a.absolute, a.repeat, a.repeat_duration,
			a.description, a.summary, a.leftovers
		FROM event_alarms a JOIN vevents v ON v.id = a.vevent_id
		WHERE v.event_id IN (`+in+`) ORDER BY a.vevent_id, a.position`, args...)
	if err != nil {
		return fmt.Errorf("load alarms: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var vid int64
		var a ical.Alarm
		var repeat sql.NullInt64
		var left string
		if err := rows.Scan(&vid, &a.Action, &a.Trigger, &a.Related, &a.Absolute, &repeat, &a.RepeatDuration,
			&a.Description, &a.Summary, &left); err != nil {
			return fmt.Errorf("scan alarm: %w", err)
		}
		if repeat.Valid {
			n := int(repeat.Int64)
			a.Repeat = &n
		}
		if err := unmarshal(left, &a.Leftovers); err != nil {
			return fmt.Errorf("alarm leftovers: %w", err)
		}
		if e := byVevent[vid]; e != nil {
			e.Alarms = append(e.Alarms, a)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load alarms: %w", err)
	}
	return nil
}

// storedZones reads the stored VTIMEZONEs of the calendars events belong to,
// by calendar.
func storedZones(q davsql.Querier, calOf map[int64]int64) (map[int64]map[string]*contentline.Component, error) {
	var cals []int64
	for _, c := range calOf {
		if !slices.Contains(cals, c) {
			cals = append(cals, c)
		}
	}
	out := map[int64]map[string]*contentline.Component{}
	if len(cals) == 0 {
		return out, nil
	}
	rows, err := q.Query(`SELECT collection_id, tzid, vtimezone FROM vtimezones WHERE collection_id IN (`+placeholders(len(cals))+`)`, anys(cals)...)
	if err != nil {
		return nil, fmt.Errorf("load time zones: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var calID int64
		var id, data string
		if err := rows.Scan(&calID, &id, &data); err != nil {
			return nil, fmt.Errorf("scan time zone: %w", err)
		}
		var vtz contentline.Component
		if err := json.Unmarshal([]byte(data), &vtz); err != nil {
			return nil, fmt.Errorf("time zone %s: %w", id, err)
		}
		if out[calID] == nil {
			out[calID] = map[string]*contentline.Component{}
		}
		out[calID][id] = &vtz
	}
	return out, rows.Err()
}

const veventColumns = `id, event_id, recurrence_id_local, recurrence_id_tzid, recurrence_id_is_date, this_and_future,
	summary, description, location, url, status, class, transp, priority, sequence,
	dtstart_local, dtstart_tzid, dtstart_is_date, dtend_local, dtend_tzid, dtend_is_date, duration, rrule,
	rdates, exdates, dtstamp, created, last_modified, organizer, categories, leftovers`

func scanVevent(rows *sql.Rows) (int64, int64, *ical.Event, error) {
	var id, eventID int64
	var e ical.Event
	var priority, sequence sql.NullInt64
	var rdates, exdates, dtstamp, created, lastMod, organizer, categories, left string
	if err := rows.Scan(&id, &eventID, &e.RecurrenceID.Local, &e.RecurrenceID.TZID, &e.RecurrenceID.IsDate, &e.ThisAndFuture,
		&e.Summary, &e.Description, &e.Location, &e.URL, &e.Status, &e.Class, &e.Transp, &priority, &sequence,
		&e.Start.Local, &e.Start.TZID, &e.Start.IsDate, &e.End.Local, &e.End.TZID, &e.End.IsDate, &e.Duration, &e.RRule,
		&rdates, &exdates, &dtstamp, &created, &lastMod, &organizer, &categories, &left); err != nil {
		return 0, 0, nil, fmt.Errorf("scan vevent: %w", err)
	}
	if priority.Valid {
		n := int(priority.Int64)
		e.Priority = &n
	}
	if sequence.Valid {
		n := int(sequence.Int64)
		e.Sequence = &n
	}
	for _, f := range []struct {
		src string
		dst any
	}{
		{rdates, &e.RDates}, {exdates, &e.ExDates}, {dtstamp, &e.DTStamp}, {created, &e.Created},
		{lastMod, &e.LastModified}, {organizer, &e.Organizer}, {categories, &e.Categories}, {left, &e.Leftovers},
	} {
		if err := unmarshal(f.src, f.dst); err != nil {
			return 0, 0, nil, fmt.Errorf("vevent %d: %w", id, err)
		}
	}
	return id, eventID, &e, nil
}

// unmarshal reads a JSON column; "" is the absent value.
func unmarshal(s string, v any) error {
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

// --- Writes ---
//
// Every write bumps the collection's version in the same transaction
// (davsql), whether it comes from DAV or the web UI, so sync tokens and ETags
// always reflect it.

// errUIDConflict is returned when another resource in the calendar has the UID.
type errUIDConflict struct{ uri string }

func (e *errUIDConflict) Error() string { return "UID already used by " + e.uri }

// stored reports a written event.
type stored struct {
	id      int64
	version int64
	created bool
	data    []byte // the text served for this version
}

// render writes an object as the text Cog serves.
func render(o *ical.Object) ([]byte, error) {
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}); err != nil {
		return nil, fmt.Errorf("write event %s: %w", o.UID, err)
	}
	return b.Bytes(), nil
}

// storeObject writes o at uri in a calendar, inside tx, after checking cond.
// floating is the calendar's zone. The colour, a Cog extension, is kept.
func storeObject(tx *sql.Tx, calID int64, floating *time.Location, uri string, o *ical.Object, cond davsql.Conditions, now int64) (*stored, error) {
	var cur *int64
	var curID, curVersion int64
	switch err := tx.QueryRow(`SELECT id, version FROM events WHERE collection_id = ? AND uri = ?`, calID, uri).Scan(&curID, &curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("read event %s: %w", uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return nil, dav.ErrPrecondition
	}
	var other string
	switch err := tx.QueryRow(`SELECT uri FROM events WHERE collection_id = ? AND uid = ? AND uri != ?`, calID, o.UID, uri).Scan(&other); {
	case err == nil:
		return nil, &errUIDConflict{other}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("check UID %s: %w", o.UID, err)
	}

	// Zones the object names without defining, defined by an earlier write.
	known, err := storedZones(tx, map[int64]int64{0: calID})
	if err != nil {
		return nil, err
	}
	sent := o.Zones
	o.Zones = map[string]*contentline.Component{}
	for id, vtz := range known[calID] {
		o.Zones[id] = vtz
	}
	for id, vtz := range sent {
		o.Zones[id] = vtz
	}
	z := zonesFor(o, floating)

	// The master first: the web UI reads position 0.
	slices.SortStableFunc(o.Events, func(a, b *ical.Event) int {
		switch {
		case a.IsOverride() == b.IsOverride():
			return 0
		case !a.IsOverride():
			return -1
		}
		return 1
	})
	var startUTC, endUTC any
	if start, end, unbounded, err := o.Bounds(z); err == nil {
		startUTC = start.Unix()
		if !unbounded {
			endUTC = end.Unix()
		}
	}
	data, err := render(o)
	if err != nil {
		return nil, err
	}
	props, err := marshal(o.Props)
	if err != nil {
		return nil, err
	}
	comps, err := marshal(o.Comps)
	if err != nil {
		return nil, err
	}

	version, err := davsql.Bump(tx, calID)
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(`INSERT INTO events (collection_id, uid, uri, version, size, start_utc, range_end_utc, props, comps, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			uid = excluded.uid, version = excluded.version, size = excluded.size,
			start_utc = excluded.start_utc, range_end_utc = excluded.range_end_utc,
			props = excluded.props, comps = excluded.comps, updated_at = excluded.updated_at
		RETURNING id`,
		calID, o.UID, uri, version, len(data), startUTC, endUTC, orEmpty(props, "[]"), orEmpty(comps, "[]"), now, now).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("store event %s: %w", uri, err)
	}
	if _, err := tx.Exec(`DELETE FROM vevents WHERE event_id = ?`, id); err != nil {
		return nil, fmt.Errorf("replace event %s: %w", uri, err)
	}
	for pos, e := range o.Events {
		if err := insertVevent(tx, id, pos, e, z); err != nil {
			return nil, fmt.Errorf("store event %s: %w", uri, err)
		}
	}
	var redefined []string
	for tzid, vtz := range sent {
		js, err := json.Marshal(vtz)
		if err != nil {
			return nil, err
		}
		// A definition that is new or different changes the times of the
		// other events naming this TZID, which were read with the old one
		// (or, with none, in the calendar's zone).
		if old, ok := known[calID][tzid]; !ok || !sameJSON(old, js) {
			redefined = append(redefined, tzid)
		}
		if _, err := tx.Exec(`INSERT INTO vtimezones (collection_id, tzid, vtimezone) VALUES (?, ?, ?)
			ON CONFLICT(collection_id, tzid) DO UPDATE SET vtimezone = excluded.vtimezone`, calID, tzid, string(js)); err != nil {
			return nil, fmt.Errorf("store time zone %s: %w", tzid, err)
		}
	}
	if len(redefined) > 0 {
		others, err := usersOf(tx, calID, id, redefined)
		if err != nil {
			return nil, err
		}
		if err := recomputeTimes(tx, others, floating, now); err != nil {
			return nil, err
		}
	}
	if err := davsql.Revive(tx, calID, uri); err != nil {
		return nil, err
	}
	if err := ensureNotifications(tx, id, o, z, now); err != nil {
		return nil, err
	}
	return &stored{id: id, version: version, created: cur == nil, data: data}, nil
}

// sameJSON reports whether a stored component encodes to js.
func sameJSON(c *contentline.Component, js []byte) bool {
	old, err := json.Marshal(c)
	return err == nil && bytes.Equal(old, js)
}

func orEmpty(s, empty string) string {
	if s == "" {
		return empty
	}
	return s
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

func insertVevent(tx *sql.Tx, eventID int64, pos int, e *ical.Event, z ical.Zones) error {
	var endUTC any
	if _, end, err := e.Span(z); err == nil {
		endUTC = end.Unix()
	}
	js := make([]string, 0, 8)
	for _, v := range []any{e.RDates, e.ExDates, e.DTStamp, e.Created, e.LastModified, e.Organizer, e.Categories, e.Leftovers} {
		s, err := marshal(v)
		if err != nil {
			return err
		}
		js = append(js, s)
	}
	var priority, sequence any
	if e.Priority != nil {
		priority = *e.Priority
	}
	if e.Sequence != nil {
		sequence = *e.Sequence
	}
	var vid int64
	err := tx.QueryRow(`INSERT INTO vevents (event_id, position, recurrence_id_local, recurrence_id_tzid, recurrence_id_is_date, this_and_future,
			summary, description, location, url, status, class, transp, priority, sequence,
			dtstart_local, dtstart_tzid, dtstart_is_date, dtstart_utc, dtend_local, dtend_tzid, dtend_is_date, end_utc, duration, rrule,
			rdates, exdates, dtstamp, created, last_modified, organizer, categories, leftovers)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		eventID, pos, e.RecurrenceID.Local, e.RecurrenceID.TZID, e.RecurrenceID.IsDate, e.ThisAndFuture,
		e.Summary, e.Description, e.Location, e.URL, e.Status, e.Class, e.Transp, priority, sequence,
		e.Start.Local, e.Start.TZID, e.Start.IsDate, utcOf(e.Start, z), e.End.Local, e.End.TZID, e.End.IsDate, endUTC, e.Duration, e.RRule,
		orEmpty(js[0], "[]"), orEmpty(js[1], "[]"), js[2], js[3], js[4], js[5], orEmpty(js[6], "[]"), orEmpty(js[7], "{}")).Scan(&vid)
	if err != nil {
		return fmt.Errorf("insert vevent: %w", err)
	}
	for i, a := range e.Attendees {
		params, err := marshal(a.Params)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO event_attendees (vevent_id, position, address, email, cn, partstat, role, rsvp, cutype, params)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			vid, i, a.Address, strings.ToLower(ical.Email(a.Address)), a.CN, a.PartStat, a.Role, a.RSVP, a.CUType, orEmpty(params, "[]")); err != nil {
			return fmt.Errorf("insert attendee: %w", err)
		}
	}
	for i, a := range e.Alarms {
		left, err := marshal(a.Leftovers)
		if err != nil {
			return err
		}
		var repeat any
		if a.Repeat != nil {
			repeat = *a.Repeat
		}
		if _, err := tx.Exec(`INSERT INTO event_alarms (vevent_id, position, action, trigger, related, absolute, repeat, repeat_duration, description, summary, leftovers)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			vid, i, a.Action, a.Trigger, a.Related, a.Absolute, repeat, a.RepeatDuration, a.Description, a.Summary, orEmpty(left, "{}")); err != nil {
			return fmt.Errorf("insert alarm: %w", err)
		}
	}
	return nil
}

// deleteEvent removes the event at uri inside tx and leaves a tombstone.
func deleteEvent(tx *sql.Tx, calID int64, uri string, cond davsql.Conditions, now int64) error {
	var version int64
	err := tx.QueryRow(`SELECT version FROM events WHERE collection_id = ? AND uri = ?`, calID, uri).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return dav.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read event %s: %w", uri, err)
	}
	if err := cond.Check(&version); err != nil {
		return dav.ErrPrecondition
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE collection_id = ? AND uri = ?`, calID, uri); err != nil {
		return fmt.Errorf("delete event %s: %w", uri, err)
	}
	_, err = davsql.Delete(tx, calID, uri, now)
	return err
}

// ParseObject reads one event resource from iCalendar text.
func ParseObject(ics string) (*ical.Object, error) {
	comps, err := contentline.Parse(strings.NewReader(ics))
	if err != nil {
		return nil, err
	}
	if len(comps) != 1 || !comps[0].Is("VCALENDAR") {
		return nil, errors.New("not one VCALENDAR")
	}
	o, err := ical.DecodeEvents(comps[0])
	if err != nil {
		return nil, err
	}
	if o.UID == "" {
		return nil, errors.New("VEVENT missing UID")
	}
	return o, nil
}

// Save is one event written by Cog: an object stored in a calendar. From
// is the event being edited, or nil for a new one; moving it to another
// calendar deletes it from the old one. Color, when set, is the new
// event's Cog colour; an edited event keeps its own.
type Save struct {
	Cal    *Calendar
	From   *Event
	Object *ical.Object
	Color  string
}

// SaveEvent stores one event written by Cog (web UI, seeds, tests).
func SaveEvent(db *sql.DB, cal *Calendar, from *Event, o *ical.Object, now time.Time) (*Event, error) {
	evs, err := SaveEvents(db, now, Save{Cal: cal, From: from, Object: o})
	if err != nil {
		return nil, err
	}
	return evs[0], nil
}

// SaveEvents stores events written by Cog in one transaction, so a series
// split in two is never seen half done.
func SaveEvents(db *sql.DB, now time.Time, saves ...Save) ([]*Event, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("save event: %w", err)
	}
	defer tx.Rollback()
	ids := make([]int64, len(saves))
	for i, sv := range saves {
		uri := sv.Object.UID + ".ics"
		if sv.From != nil {
			uri = sv.From.URI
			if sv.From.CalendarID != sv.Cal.ID {
				if err := deleteEvent(tx, sv.From.CalendarID, sv.From.URI, davsql.Conditions{}, now.Unix()); err != nil {
					return nil, err
				}
			}
		}
		st, err := storeObject(tx, sv.Cal.ID, sv.Cal.Zone(), uri, sv.Object, davsql.Conditions{}, now.Unix())
		if err != nil {
			return nil, err
		}
		color := sv.Color
		if sv.From != nil {
			color = sv.From.Color
		}
		if color != "" {
			if _, err := tx.Exec(`UPDATE events SET color = ? WHERE id = ?`, color, st.id); err != nil {
				return nil, fmt.Errorf("set event colour: %w", err)
			}
		}
		ids[i] = st.id
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("save event: %w", err)
	}
	out := make([]*Event, len(ids))
	for i, id := range ids {
		if out[i], err = GetEvent(db, id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SaveICS parses ics and stores it as a new event in cal.
func SaveICS(db *sql.DB, cal *Calendar, ics string, now time.Time) (*Event, error) {
	o, err := ParseObject(ics)
	if err != nil {
		return nil, err
	}
	return SaveEvent(db, cal, nil, o, now)
}

// RenderEvent returns the iCalendar text Cog serves for an event.
func RenderEvent(ev *Event) (string, error) {
	if ev.Object == nil {
		return "", fmt.Errorf("event %d: object not loaded", ev.ID)
	}
	data, err := render(ev.Object)
	return string(data), err
}

// SetEventColor sets the per-event color, a Cog extension that is not part
// of the ICS, so it does not change the event's version.
func SetEventColor(db *sql.DB, id int64, color string) error {
	if _, err := db.Exec(`UPDATE events SET color = ? WHERE id = ?`, color, id); err != nil {
		return fmt.Errorf("set event color: %w", err)
	}
	return nil
}

// DeleteEventByID removes an event and leaves a tombstone for DAV clients.
func DeleteEventByID(db *sql.DB, id int64, now time.Time) error {
	ev, err := GetEvent(db, id)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete event: %w", err)
	}
	defer tx.Rollback()
	if err := deleteEvent(tx, ev.CalendarID, ev.URI, davsql.Conditions{}, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// RecomputeEventTimes re-derives a calendar's indexed times and reminders
// after its zone changes, since floating times are read in that zone. The
// iCalendar data is unchanged, so versions and DAV clients are unaffected.
func RecomputeEventTimes(db *sql.DB, calendarID int64, zone *time.Location, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("recompute event times: %w", err)
	}
	defer tx.Rollback()
	ids, err := eventIDs(tx, `SELECT id FROM events WHERE collection_id = ?`, calendarID)
	if err != nil {
		return err
	}
	if err := recomputeTimes(tx, ids, zone, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func eventIDs(q davsql.Querier, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("find events: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("find events: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// recomputeTimes re-derives the indexed times and reminders of events, all
// in one calendar whose zone is floating, from their stored rows.
func recomputeTimes(tx *sql.Tx, ids []int64, floating *time.Location, now int64) error {
	objs, err := loadObjects(tx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		o := objs[id]
		if o == nil {
			continue
		}
		z := zonesFor(o, floating)
		var startUTC, endUTC any
		if start, end, unbounded, err := o.Bounds(z); err == nil {
			startUTC = start.Unix()
			if !unbounded {
				endUTC = end.Unix()
			}
		}
		if _, err := tx.Exec(`UPDATE events SET start_utc = ?, range_end_utc = ? WHERE id = ?`, startUTC, endUTC, id); err != nil {
			return fmt.Errorf("update event %d times: %w", id, err)
		}
		vids, err := eventIDs(tx, `SELECT id FROM vevents WHERE event_id = ? ORDER BY position`, id)
		if err != nil {
			return err
		}
		for i, vid := range vids {
			if i >= len(o.Events) {
				break
			}
			e := o.Events[i]
			var end any
			if _, t, err := e.Span(z); err == nil {
				end = t.Unix()
			}
			if _, err := tx.Exec(`UPDATE vevents SET dtstart_utc = ?, end_utc = ? WHERE id = ?`, utcOf(e.Start, z), end, vid); err != nil {
				return fmt.Errorf("update event %d times: %w", id, err)
			}
		}
		if err := ensureNotifications(tx, id, o, z, now); err != nil {
			return fmt.Errorf("update event %d reminders: %w", id, err)
		}
	}
	return nil
}

// usersOf returns the other events in a calendar that use any of tzids, in
// their times or their RDATE and EXDATE values.
func usersOf(tx *sql.Tx, calID, except int64, tzids []string) ([]int64, error) {
	var conds []string
	var args []any
	args = append(args, calID, except)
	for _, id := range tzids {
		needle, err := json.Marshal(id)
		if err != nil {
			return nil, err
		}
		conds = append(conds, `v.dtstart_tzid = ? OR v.dtend_tzid = ? OR v.recurrence_id_tzid = ?
			OR instr(v.rdates, ?) > 0 OR instr(v.exdates, ?) > 0`)
		tag := `"tz":` + string(needle)
		args = append(args, id, id, id, tag, tag)
	}
	return eventIDs(tx, `SELECT DISTINCT v.event_id FROM vevents v JOIN events e ON e.id = v.event_id
		WHERE e.collection_id = ? AND e.id != ? AND (`+strings.Join(conds, " OR ")+`)`, args...)
}
