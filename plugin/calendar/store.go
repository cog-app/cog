package calendar

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/icalsql"
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
	Kind        dav.Kind
	// ReadOnly calendars are filled by Cog (a subscription, the
	// birthdays): nobody edits their events.
	ReadOnly bool
}

func calendarFrom(c *dav.Collection) Calendar {
	return Calendar{
		ID: int64(c.ID), UID: c.Name, Name: c.Title(), Color: c.Color, Description: c.Description,
		SortOrder: c.SortOrder, Timezone: c.Timezone, OwnerID: c.OwnerID, Kind: c.Kind, ReadOnly: c.ReadOnly(),
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

// tables are where events are stored; AfterWrite keeps their reminders.
var tables = &icalsql.Tables{
	Objects: "events", Components: "vevents", Attendees: "event_attendees", Alarms: "event_alarms",
	Attachments: "event_attachments",
	AfterWrite: func(tx *sql.Tx, id int64, o *ical.Object, z ical.Zones, now int64) error {
		return ensureNotifications(tx, id, o, z, now)
	},
}

// eventColumns selects an Event: the resource row and its first component.
const eventColumns = `e.id, e.collection_id, e.uid, e.uri, e.version, e.color, e.created_at, e.updated_at,
	v.summary, v.location, v.description, coalesce(v.dtstart_utc, 0), coalesce(v.end_utc, 0), v.dtstart_is_date, v.rrule`

const eventFrom = ` FROM events e JOIN vevents v ON v.object_id = e.id AND v.position = 0`

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
	objs, err := tables.Load(db, []int64{ev.ID})
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
	objs, err := tables.Load(db, ids)
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
	ins, err := ev.Object.Instances(ical.ZonesFor(ev.Object, ev.Zone()), start, end)
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

// --- Writes ---
//
// Every write bumps the collection's version in the same transaction
// (davsql), whether it comes from DAV or the web UI, so sync tokens and ETags
// always reflect it.

// ParseObject reads one event resource from iCalendar text.
func ParseObject(ics string) (*ical.Object, error) {
	comps, err := contentline.Parse(strings.NewReader(ics))
	if err != nil {
		return nil, err
	}
	if len(comps) != 1 || !comps[0].Is("VCALENDAR") {
		return nil, errors.New("not one VCALENDAR")
	}
	o, err := ical.Decode(comps[0], ical.VEVENT)
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
				if err := tables.Delete(tx, sv.From.CalendarID, sv.From.URI, davsql.Conditions{}, now.Unix()); err != nil {
					return nil, err
				}
			}
		}
		st, err := tables.Store(tx, sv.Cal.ID, sv.Cal.Zone(), uri, sv.Object, davsql.Conditions{}, now.Unix())
		if err != nil {
			return nil, err
		}
		color := sv.Color
		if sv.From != nil {
			color = sv.From.Color
		}
		if color != "" {
			if _, err := tx.Exec(`UPDATE events SET color = ? WHERE id = ?`, color, st.ID); err != nil {
				return nil, fmt.Errorf("set event colour: %w", err)
			}
		}
		ids[i] = st.ID
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
	data, err := icalsql.Render(ev.Object)
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
	if err := tables.Delete(tx, ev.CalendarID, ev.URI, davsql.Conditions{}, now.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// RecomputeEventTimes re-derives a calendar's indexed times and reminders
// after its zone changes, since floating times are read in that zone. The
// iCalendar data is unchanged, so versions and DAV clients are unaffected.
func RecomputeEventTimes(db *sql.DB, calendarID int64, zone *time.Location, now time.Time) error {
	return tables.RecomputeCollection(db, calendarID, zone, now)
}
