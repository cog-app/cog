package calendar

import (
	"crypto/md5"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Calendar is a user's named calendar collection.
type Calendar struct {
	ID          int64
	UID         string
	Name        string
	Color       string
	Description string
	SortOrder   int
	CTag        int64
	SyncToken   int64
	Timezone    string // IANA zone for floating times and new events; "" = UTC
	CreatedAt   int64
	UpdatedAt   int64
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
	CalendarID  int64
	UID         string
	ETag        string
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

// --- Calendar CRUD ---

func ListCalendars(db *sql.DB) ([]Calendar, error) {
	rows, err := db.Query(`SELECT id, uid, name, color, description, sort_order, ctag, sync_token, timezone, created_at, updated_at FROM calendars ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cals []Calendar
	for rows.Next() {
		var c Calendar
		if err := rows.Scan(&c.ID, &c.UID, &c.Name, &c.Color, &c.Description, &c.SortOrder, &c.CTag, &c.SyncToken, &c.Timezone, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		cals = append(cals, c)
	}
	return cals, rows.Err()
}

func GetCalendar(db *sql.DB, id int64) (*Calendar, error) {
	var c Calendar
	err := db.QueryRow(`SELECT id, uid, name, color, description, sort_order, ctag, sync_token, timezone, created_at, updated_at FROM calendars WHERE id = ?`, id).
		Scan(&c.ID, &c.UID, &c.Name, &c.Color, &c.Description, &c.SortOrder, &c.CTag, &c.SyncToken, &c.Timezone, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func GetCalendarByUID(db *sql.DB, uid string) (*Calendar, error) {
	var c Calendar
	err := db.QueryRow(`SELECT id, uid, name, color, description, sort_order, ctag, sync_token, timezone, created_at, updated_at FROM calendars WHERE uid = ?`, uid).
		Scan(&c.ID, &c.UID, &c.Name, &c.Color, &c.Description, &c.SortOrder, &c.CTag, &c.SyncToken, &c.Timezone, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func CreateCalendar(db *sql.DB, uid, name, color, description, timezone string) (*Calendar, error) {
	if color == "" {
		color = "#0082c9"
	}
	res, err := db.Exec(`INSERT INTO calendars (uid, name, color, description, timezone) VALUES (?, ?, ?, ?, ?)`, uid, name, color, description, timezone)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetCalendar(db, id)
}

func UpdateCalendar(db *sql.DB, id int64, name, color, description string) error {
	_, err := db.Exec(`UPDATE calendars SET name = ?, color = ?, description = ?, updated_at = unixepoch() WHERE id = ?`, name, color, description, id)
	return err
}

// SetCalendarTimezone changes a calendar's zone and recomputes its events'
// indexed times, since floating times are read in that zone.
func SetCalendarTimezone(db *sql.DB, id int64, timezone string) error {
	if _, err := db.Exec(`UPDATE calendars SET timezone = ?, updated_at = unixepoch() WHERE id = ? AND timezone != ?`, timezone, id, timezone); err != nil {
		return err
	}
	return RecomputeEventTimes(db, id)
}

func DeleteCalendar(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM calendars WHERE id = ?`, id)
	return err
}

func BumpCTag(db *sql.DB, calendarID int64) error {
	_, err := db.Exec(`UPDATE calendars SET ctag = ctag + 1, sync_token = sync_token + 1, updated_at = unixepoch() WHERE id = ?`, calendarID)
	return err
}

// EnsureDefaultCalendar creates a "personal" calendar if none exist.
func EnsureDefaultCalendar(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM calendars`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := CreateCalendar(db, "personal", "Personal", "#0082c9", "", "")
		return err
	}
	return nil
}

// --- Event CRUD ---

func ListEvents(db *sql.DB, calendarID int64) ([]Event, error) {
	rows, err := db.Query(`SELECT id, calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at
		FROM events WHERE calendar_id = ? ORDER BY dtstart`, calendarID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func GetEventByUID(db *sql.DB, calendarID int64, uid string) (*Event, error) {
	var e Event
	err := db.QueryRow(`SELECT id, calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at
		FROM events WHERE calendar_id = ? AND uid = ?`, calendarID, uid).
		Scan(&e.ID, &e.CalendarID, &e.UID, &e.ETag, &e.Summary, &e.Location, &e.Description, &e.DTStart, &e.DTEnd, &e.IsAllDay, &e.Color, &e.ICSData, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func GetEvent(db *sql.DB, id int64) (*Event, error) {
	var e Event
	err := db.QueryRow(`SELECT id, calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at
		FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.CalendarID, &e.UID, &e.ETag, &e.Summary, &e.Location, &e.Description, &e.DTStart, &e.DTEnd, &e.IsAllDay, &e.Color, &e.ICSData, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// UpsertEvent inserts or updates an event, bumps the calendar's ctag.
func UpsertEvent(db *sql.DB, calendarID int64, uid, icsData string, fields VEventFields) (*Event, error) {
	etag := fmt.Sprintf("%x", md5.Sum([]byte(icsData)))
	dtstart, dtend := eventUnix(fields)

	_, err := db.Exec(`INSERT INTO events (calendar_id, uid, etag, summary, location, description, dtstart, dtend, is_all_day, color, ics_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(calendar_id, uid) DO UPDATE SET
			etag = excluded.etag,
			summary = excluded.summary,
			location = excluded.location,
			description = excluded.description,
			dtstart = excluded.dtstart,
			dtend = excluded.dtend,
			is_all_day = excluded.is_all_day,
			color = excluded.color,
			ics_data = excluded.ics_data,
			updated_at = unixepoch()`,
		calendarID, uid, etag, fields.Summary, fields.Location, fields.Description,
		dtstart, dtend, fields.IsAllDay, fields.Color, icsData)
	if err != nil {
		return nil, err
	}

	if err := BumpCTag(db, calendarID); err != nil {
		return nil, err
	}

	return GetEventByUID(db, calendarID, uid)
}

func DeleteEvent(db *sql.DB, calendarID int64, uid string) error {
	// Record deletion for sync-token tracking before deleting.
	var syncToken int64
	if err := db.QueryRow(`SELECT sync_token FROM calendars WHERE id = ?`, calendarID).Scan(&syncToken); err != nil {
		return fmt.Errorf("get sync token: %w", err)
	}
	if _, err := db.Exec(`INSERT INTO deleted_events (calendar_id, uid, deleted_at_token) VALUES (?, ?, ?)`,
		calendarID, uid, syncToken+1); err != nil {
		return fmt.Errorf("record deletion: %w", err)
	}

	_, err := db.Exec(`DELETE FROM events WHERE calendar_id = ? AND uid = ?`, calendarID, uid)
	if err != nil {
		return err
	}
	return BumpCTag(db, calendarID)
}

func DeleteEventByID(db *sql.DB, id int64) error {
	var calID int64
	if err := db.QueryRow(`SELECT calendar_id FROM events WHERE id = ?`, id).Scan(&calID); err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM events WHERE id = ?`, id); err != nil {
		return err
	}
	return BumpCTag(db, calID)
}

// UpcomingEvents returns events starting from now, joined with calendar info.
func UpcomingEvents(db *sql.DB, now int64, limit int) ([]EventWithCalendar, error) {
	rows, err := db.Query(`SELECT e.id, e.calendar_id, e.uid, e.etag, e.summary, e.location, e.description,
		e.dtstart, e.dtend, e.is_all_day, e.color, e.ics_data, e.created_at, e.updated_at,
		c.name, c.color, c.timezone
		FROM events e JOIN calendars c ON e.calendar_id = c.id
		WHERE e.dtstart >= ? OR e.dtend >= ?
		ORDER BY e.dtstart LIMIT ?`, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []EventWithCalendar
	for rows.Next() {
		var ev EventWithCalendar
		if err := rows.Scan(&ev.ID, &ev.CalendarID, &ev.UID, &ev.ETag, &ev.Summary, &ev.Location, &ev.Description,
			&ev.DTStart, &ev.DTEnd, &ev.IsAllDay, &ev.Color, &ev.ICSData, &ev.CreatedAt, &ev.UpdatedAt,
			&ev.CalendarName, &ev.CalendarColor, &ev.CalendarTimezone); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// AllEvents returns all events joined with calendar info, ordered by start time.
func AllEvents(db *sql.DB, limit int) ([]EventWithCalendar, error) {
	rows, err := db.Query(`SELECT e.id, e.calendar_id, e.uid, e.etag, e.summary, e.location, e.description,
		e.dtstart, e.dtend, e.is_all_day, e.color, e.ics_data, e.created_at, e.updated_at,
		c.name, c.color, c.timezone
		FROM events e JOIN calendars c ON e.calendar_id = c.id
		ORDER BY e.dtstart LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []EventWithCalendar
	for rows.Next() {
		var ev EventWithCalendar
		if err := rows.Scan(&ev.ID, &ev.CalendarID, &ev.UID, &ev.ETag, &ev.Summary, &ev.Location, &ev.Description,
			&ev.DTStart, &ev.DTEnd, &ev.IsAllDay, &ev.Color, &ev.ICSData, &ev.CreatedAt, &ev.UpdatedAt,
			&ev.CalendarName, &ev.CalendarColor, &ev.CalendarTimezone); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// EventsInRange returns events overlapping the given time range (unix timestamps).
func EventsInRange(db *sql.DB, start, end int64) ([]EventWithCalendar, error) {
	rows, err := db.Query(`SELECT e.id, e.calendar_id, e.uid, e.etag, e.summary, e.location, e.description,
		e.dtstart, e.dtend, e.is_all_day, e.color, e.ics_data, e.created_at, e.updated_at,
		c.name, c.color, c.timezone
		FROM events e JOIN calendars c ON e.calendar_id = c.id
		WHERE e.dtstart < ? AND (e.dtend > ? OR e.ics_data LIKE '%RRULE:%')
		ORDER BY e.dtstart`, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []EventWithCalendar
	for rows.Next() {
		var ev EventWithCalendar
		if err := rows.Scan(&ev.ID, &ev.CalendarID, &ev.UID, &ev.ETag, &ev.Summary, &ev.Location, &ev.Description,
			&ev.DTStart, &ev.DTEnd, &ev.IsAllDay, &ev.Color, &ev.ICSData, &ev.CreatedAt, &ev.UpdatedAt,
			&ev.CalendarName, &ev.CalendarColor, &ev.CalendarTimezone); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// EventsChangedSince returns all events in the calendar that were created or
// modified after the given sync token. Since sync_token increments per mutation,
// we return all events (clients use etags to determine actual changes).
func EventsChangedSince(db *sql.DB, calendarID, sinceToken int64) ([]Event, error) {
	// Simple approach: return all events in the calendar. The client compares
	// etags to determine which actually changed. This is correct for sync-collection.
	return ListEvents(db, calendarID)
}

// DeletedEventsSince returns UIDs of events deleted after the given sync token.
func DeletedEventsSince(db *sql.DB, calendarID, sinceToken int64) ([]string, error) {
	rows, err := db.Query(`SELECT uid FROM deleted_events WHERE calendar_id = ? AND deleted_at_token > ?`,
		calendarID, sinceToken)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		uids = append(uids, uid)
	}
	return uids, rows.Err()
}

// EventsInRangeForCalendar returns events in a calendar overlapping the given time range.
func EventsInRangeForCalendar(db *sql.DB, calendarID, start, end int64) ([]Event, error) {
	rows, err := db.Query(`SELECT id, calendar_id, uid, etag, summary, location, description,
		dtstart, dtend, is_all_day, color, ics_data, created_at, updated_at
		FROM events WHERE calendar_id = ? AND dtstart < ? AND (dtend > ? OR ics_data LIKE '%RRULE:%')
		ORDER BY dtstart`, calendarID, end, start)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
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

// AddAttendee adds an attendee to an event.
func AddAttendee(db *sql.DB, eventID int64, email, cogUser, displayName, role string) error {
	if role == "" {
		role = "REQ-PARTICIPANT"
	}
	_, err := db.Exec(`INSERT INTO event_attendees (event_id, email, cog_user, display_name, role) VALUES (?, ?, ?, ?, ?)`,
		eventID, email, cogUser, displayName, role)
	return err
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

// DeleteAttendees removes all attendees for an event.
func DeleteAttendees(db *sql.DB, eventID int64) error {
	_, err := db.Exec(`DELETE FROM event_attendees WHERE event_id = ?`, eventID)
	return err
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.CalendarID, &e.UID, &e.ETag, &e.Summary, &e.Location, &e.Description,
			&e.DTStart, &e.DTEnd, &e.IsAllDay, &e.Color, &e.ICSData, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// eventTimesVersion is bumped when the way dtstart/dtend are derived from
// ics_data changes, so RepairEventTimes recomputes existing rows once.
// v1: honour TZID and the calendar's zone instead of reading local times as UTC.
const eventTimesVersion = "1"

// RepairEventTimes recomputes every event's indexed times if they were derived
// by an older parser. It is a no-op once the database is current.
func RepairEventTimes(db *sql.DB) error {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = 'event_times_version'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read event times version: %w", err)
	}
	if v == eventTimesVersion {
		return nil
	}
	if err := RecomputeEventTimes(db, 0); err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO meta (key, value) VALUES ('event_times_version', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, eventTimesVersion)
	return err
}

// RecomputeEventTimes re-derives dtstart, dtend and reminder times from
// ics_data for one calendar, or all calendars when calendarID is 0. The ICS
// itself is untouched, so etags and DAV clients are unaffected.
func RecomputeEventTimes(db *sql.DB, calendarID int64) error {
	cals, err := ListCalendars(db)
	if err != nil {
		return fmt.Errorf("list calendars: %w", err)
	}
	locs := make(map[int64]*time.Location, len(cals))
	for i := range cals {
		locs[cals[i].ID] = cals[i].Zone()
	}

	rows, err := db.Query(`SELECT id, calendar_id, dtstart, dtend, ics_data FROM events WHERE ? = 0 OR calendar_id = ?`, calendarID, calendarID)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	type change struct {
		id, start, end int64
		ics            string
	}
	var changes []change
	for rows.Next() {
		var id, calID, start, end int64
		var ics string
		if err := rows.Scan(&id, &calID, &start, &end, &ics); err != nil {
			rows.Close()
			return fmt.Errorf("scan event: %w", err)
		}
		fields, err := ParseVEventIn(ics, locs[calID])
		if err != nil {
			continue // unparseable ICS was stored as-is; nothing to derive
		}
		newStart, newEnd := eventUnix(fields)
		if newStart != start || newEnd != end {
			changes = append(changes, change{id, newStart, newEnd, ics})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate events: %w", err)
	}
	rows.Close()

	for _, c := range changes {
		if _, err := db.Exec(`UPDATE events SET dtstart = ?, dtend = ? WHERE id = ?`, c.start, c.end, c.id); err != nil {
			return fmt.Errorf("update event %d times: %w", c.id, err)
		}
		if err := EnsureNotifications(db, c.id, c.start, c.ics); err != nil {
			return fmt.Errorf("update event %d reminders: %w", c.id, err)
		}
	}
	return nil
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
