package calendar

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/davsql"
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

// Event is a single calendar event with indexed fields and raw ICS blob.
type Event struct {
	ID          int64
	CalendarID  int64 // dav.CollectionID
	UID         string
	URI         string // resource name in the collection
	Version     int64
	Summary     string
	Location    string
	Description string
	DTStart     int64
	DTEnd       int64
	IsAllDay    bool
	Color       string
	ICSData     string
	CreatedAt   int64
	UpdatedAt   int64
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

const eventColumns = `id, collection_id, uid, uri, version, summary, location, description,
	dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at`

func scanEvent(row interface{ Scan(...any) error }) (*Event, error) {
	var e Event
	if err := row.Scan(&e.ID, &e.CalendarID, &e.UID, &e.URI, &e.Version, &e.Summary, &e.Location, &e.Description,
		&e.DTStart, &e.DTEnd, &e.IsAllDay, &e.Color, &e.ICSData, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func queryEvents(q davsql.Querier, query string, args ...any) ([]Event, error) {
	rows, err := q.Query(query, args...)
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
	return queryEvents(db, `SELECT `+eventColumns+` FROM events WHERE collection_id = ? ORDER BY dtstart`, calendarID)
}

// GetEvent returns an event by row ID.
func GetEvent(db *sql.DB, id int64) (*Event, error) {
	return scanEvent(db.QueryRow(`SELECT `+eventColumns+` FROM events WHERE id = ?`, id))
}

// GetEventByUID returns a calendar's event with this UID.
func GetEventByUID(db *sql.DB, calendarID int64, uid string) (*Event, error) {
	return scanEvent(db.QueryRow(`SELECT `+eventColumns+` FROM events WHERE collection_id = ? AND uid = ?`, calendarID, uid))
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

// UpcomingEvents returns events in cals starting or ending from now.
func UpcomingEvents(db *sql.DB, cals map[int64]Calendar, now int64, limit int) ([]EventWithCalendar, error) {
	in, args := inCalendars("collection_id", cals)
	events, err := queryEvents(db, `SELECT `+eventColumns+` FROM events WHERE (dtstart >= ? OR dtend >= ?)`+in+`
		ORDER BY dtstart LIMIT ?`, append(append([]any{now, now}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	return withCalendars(events, cals), nil
}

// EventsInRange returns events in cals overlapping [start, end), plus every
// recurring event that starts before end, for the caller to expand.
func EventsInRange(db *sql.DB, cals map[int64]Calendar, start, end int64) ([]EventWithCalendar, error) {
	in, args := inCalendars("collection_id", cals)
	events, err := queryEvents(db, `SELECT `+eventColumns+` FROM events
		WHERE dtstart < ? AND (dtend > ? OR ics_data LIKE '%RRULE:%')`+in+` ORDER BY dtstart`,
		append([]any{end, start}, args...)...)
	if err != nil {
		return nil, err
	}
	return withCalendars(events, cals), nil
}

// EventsInRangeForCalendar is EventsInRange for one calendar.
func EventsInRangeForCalendar(db *sql.DB, calendarID, start, end int64) ([]Event, error) {
	return queryEvents(db, `SELECT `+eventColumns+` FROM events
		WHERE collection_id = ? AND dtstart < ? AND (dtend > ? OR ics_data LIKE '%RRULE:%') ORDER BY dtstart`,
		calendarID, end, start)
}

// --- Writes ---
//
// Every write bumps the collection's version in the same transaction
// (davsql), whether it comes from DAV or the web UI, so sync tokens and ETags
// always reflect it.

// errUIDConflict is returned when another resource in the calendar has the UID.
type errUIDConflict struct{ uri string }

func (e *errUIDConflict) Error() string { return "UID already used by " + e.uri }

// putEvent stores ics at uri in a calendar, inside tx, and returns the row ID,
// its new version and whether it was created. fields must be parsed from ics.
func putEvent(tx *sql.Tx, calID int64, uri, uid, ics string, fields VEventFields, cond davsql.Conditions, now int64) (id, version int64, created bool, err error) {
	var cur *int64
	var curID, curVersion int64
	switch err := tx.QueryRow(`SELECT id, version FROM events WHERE collection_id = ? AND uri = ?`, calID, uri).Scan(&curID, &curVersion); {
	case err == nil:
		cur = &curVersion
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, false, fmt.Errorf("read event %s: %w", uri, err)
	}
	if err := cond.Check(cur); err != nil {
		return 0, 0, false, dav.ErrPrecondition
	}
	var other string
	switch err := tx.QueryRow(`SELECT uri FROM events WHERE collection_id = ? AND uid = ? AND uri != ?`, calID, uid, uri).Scan(&other); {
	case err == nil:
		return 0, 0, false, &errUIDConflict{other}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, 0, false, fmt.Errorf("check UID %s: %w", uid, err)
	}
	version, err = davsql.Bump(tx, calID)
	if err != nil {
		return 0, 0, false, err
	}
	start, end := eventUnix(fields)
	err = tx.QueryRow(`INSERT INTO events (collection_id, uid, uri, version, summary, location, description,
			dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(collection_id, uri) DO UPDATE SET
			uid = excluded.uid, version = excluded.version, summary = excluded.summary,
			location = excluded.location, description = excluded.description,
			dtstart = excluded.dtstart, dtend = excluded.dtend, is_all_day = excluded.is_all_day,
			ics_data = excluded.ics_data, updated_at = excluded.updated_at
		RETURNING id`,
		calID, uid, uri, version, fields.Summary, fields.Location, fields.Description,
		start, end, fields.IsAllDay, fields.Color, ics, now, now).Scan(&id)
	if err != nil {
		return 0, 0, false, fmt.Errorf("store event %s: %w", uri, err)
	}
	if err := davsql.Revive(tx, calID, uri); err != nil {
		return 0, 0, false, err
	}
	if err := EnsureNotifications(tx, id, start, ics); err != nil {
		return 0, 0, false, err
	}
	return id, version, cur == nil, nil
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

// SaveEvent stores an event written by Cog (web UI, seeds, tests) in cal.
// from is the event being edited, or nil for a new one; moving it to another
// calendar deletes it from the old one in the same transaction.
func SaveEvent(db *sql.DB, cal *Calendar, from *Event, ics string, now time.Time) (*Event, error) {
	fields, err := ParseVEventIn(ics, cal.Zone())
	if err != nil {
		return nil, err
	}
	uri := fields.UID + ".ics"
	if from != nil {
		uri = from.URI
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("save event: %w", err)
	}
	defer tx.Rollback()
	if from != nil && from.CalendarID != cal.ID {
		if err := deleteEvent(tx, from.CalendarID, from.URI, davsql.Conditions{}, now.Unix()); err != nil {
			return nil, err
		}
	}
	id, _, _, err := putEvent(tx, cal.ID, uri, fields.UID, ics, fields, davsql.Conditions{}, now.Unix())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("save event: %w", err)
	}
	return GetEvent(db, id)
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

// Attendee represents an event attendee.
type Attendee struct {
	ID          int64
	EventID     int64
	Email       string
	CogUser     string
	DisplayName string
	Status      string // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role        string // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
}

// ReplaceAttendees replaces an event's attendees.
func ReplaceAttendees(db *sql.DB, eventID int64, attendees []AttendeeField) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("replace attendees: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM event_attendees WHERE event_id = ?`, eventID); err != nil {
		return fmt.Errorf("clear attendees: %w", err)
	}
	for _, a := range attendees {
		role := a.Role
		if role == "" {
			role = "REQ-PARTICIPANT"
		}
		if _, err := tx.Exec(`INSERT INTO event_attendees (event_id, email, display_name, role) VALUES (?, ?, ?, ?)`,
			eventID, a.Email, a.CN, role); err != nil {
			return fmt.Errorf("add attendee: %w", err)
		}
	}
	return tx.Commit()
}

// ListAttendees returns all attendees for an event.
func ListAttendees(db *sql.DB, eventID int64) ([]Attendee, error) {
	rows, err := db.Query(`SELECT id, event_id, email, cog_user, display_name, status, role FROM event_attendees WHERE event_id = ? ORDER BY id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attendees []Attendee
	for rows.Next() {
		var a Attendee
		if err := rows.Scan(&a.ID, &a.EventID, &a.Email, &a.CogUser, &a.DisplayName, &a.Status, &a.Role); err != nil {
			return nil, err
		}
		attendees = append(attendees, a)
	}
	return attendees, rows.Err()
}

// UpdateAttendeeStatus updates the status of an attendee by event ID and email.
func UpdateAttendeeStatus(db *sql.DB, eventID int64, email, status string) error {
	_, err := db.Exec(`UPDATE event_attendees SET status = ? WHERE event_id = ? AND email = ?`, status, eventID, email)
	return err
}

// RecomputeEventTimes re-derives a calendar's indexed times and reminders from
// ics_data after its zone changes, since floating times are read in that
// zone. The ICS is untouched, so versions and DAV clients are unaffected.
func RecomputeEventTimes(db *sql.DB, calendarID int64, zone *time.Location) error {
	events, err := ListEvents(db, calendarID)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("recompute event times: %w", err)
	}
	defer tx.Rollback()
	for _, ev := range events {
		fields, err := ParseVEventIn(ev.ICSData, zone)
		if err != nil {
			// Stored as received; DAV PUT validated the syntax, so this is a
			// value the parser cannot read. Its times stay as they were.
			continue
		}
		start, end := eventUnix(fields)
		if start == ev.DTStart && end == ev.DTEnd {
			continue
		}
		if _, err := tx.Exec(`UPDATE events SET dtstart = ?, dtend = ? WHERE id = ?`, start, end, ev.ID); err != nil {
			return fmt.Errorf("update event %d times: %w", ev.ID, err)
		}
		if err := EnsureNotifications(tx, ev.ID, start, ev.ICSData); err != nil {
			return fmt.Errorf("update event %d reminders: %w", ev.ID, err)
		}
	}
	return tx.Commit()
}

// eventUnix returns the indexed start and end for parsed fields, using 0 for
// missing values.
func eventUnix(f VEventFields) (start, end int64) {
	if !f.DTStart.IsZero() {
		start = f.DTStart.Unix()
	}
	if !f.DTEnd.IsZero() {
		end = f.DTEnd.Unix()
	}
	return start, end
}
