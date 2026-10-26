package calendar

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"github.com/google/uuid"
	rrulelib "github.com/teambition/rrule-go"
)

// Pages holds the web UI handlers for the calendar plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

// userDB returns the request user's database and ID.
func (h *Pages) userDB(r *http.Request) (*sql.DB, string, error) {
	u := plugin.UserFromContext(r.Context())
	if u == nil {
		return nil, "", errors.New("calendar: request has no user")
	}
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		return nil, "", err
	}
	return db, u.ID, nil
}

// Index shows the FullCalendar UI.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	calendars, err := h.plugin.Calendars(u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Calendar")
	calIndexPage(calendars, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Settings shows calendar management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	calendars, err := h.plugin.Calendars(u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Calendar Settings")
	calSettingsPage(calendars, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// EventsJSON returns events as JSON for FullCalendar.
func (h *Pages) EventsJSON(w http.ResponseWriter, r *http.Request) {
	db, userID, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	startStr := r.URL.Query().Get("start")
	endStr := r.URL.Query().Get("end")
	calIDStr := r.URL.Query().Get("calendar_id")

	start, _ := time.Parse(time.RFC3339, startStr)
	end, _ := time.Parse(time.RFC3339, endStr)
	if start.IsZero() {
		start = h.plugin.ctx.Now().AddDate(0, -1, 0)
	}
	if end.IsZero() {
		end = h.plugin.ctx.Now().AddDate(0, 2, 0)
	}

	cals, err := h.plugin.calendarMap(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if calIDStr != "" {
		calID, err := strconv.ParseInt(calIDStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid calendar", http.StatusBadRequest)
			return
		}
		cal, ok := cals[calID]
		if !ok {
			http.Error(w, "calendar not found", http.StatusNotFound)
			return
		}
		cals = map[int64]Calendar{calID: cal}
	}
	events, err := EventsInRange(db, cals, start.Unix(), end.Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if calIDStr == "" {
		// Also include events from calendars shared with this user.
		shared, err := h.plugin.SharedCalendars(userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, sc := range shared {
			ownerDB, err := h.plugin.DB(sc.OwnerUsername)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			sharedEvents, err := EventsInRange(ownerDB, map[int64]Calendar{sc.ID: sc.Calendar}, start.Unix(), end.Unix())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			for _, ev := range sharedEvents {
				ev.CalendarName = sc.Name + " (" + sc.OwnerUsername + ")"
				switch {
				case sc.Permissions&plugin.PermRead != 0:
					events = append(events, ev)
				case sc.Permissions&plugin.PermFreeBusy != 0:
					// Free/Busy only: times, nothing else.
					ev.Object = ev.Object.Busy()
					ev.Summary, ev.Description, ev.Location = "Busy", "", ""
					events = append(events, ev)
				}
			}
		}
	}

	// One entry per instance: recurrences expanded, overrides in place of the
	// occurrences they replace.
	var expanded []EventWithCalendar
	for _, ev := range events {
		ins, err := Instances(ev, start, end)
		if err != nil {
			h.plugin.log.Warn("expand event", "uid", ev.UID, "err", err)
			continue
		}
		expanded = append(expanded, ins...)
	}

	type fcEvent struct {
		ID            int64  `json:"id"`
		Title         string `json:"title"`
		Start         string `json:"start"`
		End           string `json:"end"`
		AllDay        bool   `json:"allDay"`
		Color         string `json:"color,omitempty"`
		ExtendedProps struct {
			Location    string `json:"location"`
			Description string `json:"description"`
			CalendarID  int64  `json:"calendarId"`
		} `json:"extendedProps"`
	}

	result := make([]fcEvent, 0, len(expanded))
	for _, ev := range expanded {
		color := ev.CalendarColor
		if ev.Color != "" {
			color = ev.Color
		}
		fc := fcEvent{
			ID:     ev.ID,
			Title:  ev.Summary,
			AllDay: ev.IsAllDay,
			Color:  color,
		}
		if ev.IsAllDay {
			fc.Start = time.Unix(ev.DTStart, 0).UTC().Format("2006-01-02")
			fc.End = time.Unix(ev.DTEnd, 0).UTC().Format("2006-01-02")
		} else {
			fc.Start = time.Unix(ev.DTStart, 0).UTC().Format(time.RFC3339)
			fc.End = time.Unix(ev.DTEnd, 0).UTC().Format(time.RFC3339)
		}
		fc.ExtendedProps.Location = ev.Location
		fc.ExtendedProps.Description = ev.Description
		fc.ExtendedProps.CalendarID = ev.CalendarID
		result = append(result, fc)
	}

	plugin.WriteJSON(w, http.StatusOK, result)
}

// ownEvent returns the request user's event by path ID, with its object and
// calendar. Events of a calendar the user does not own (or that no longer
// exists) are not found.
func (h *Pages) ownEvent(r *http.Request) (*sql.DB, string, *Event, *Calendar, int, error) {
	db, userID, err := h.userDB(r)
	if err != nil {
		return nil, "", nil, nil, http.StatusInternalServerError, err
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return nil, "", nil, nil, http.StatusBadRequest, errors.New("invalid id")
	}
	ev, err := GetEvent(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil, nil, http.StatusNotFound, errors.New("not found")
	}
	if err != nil {
		return nil, "", nil, nil, http.StatusInternalServerError, err
	}
	cal, err := h.plugin.Calendar(userID, ev.CalendarID)
	if errors.Is(err, dav.ErrNotFound) {
		return nil, "", nil, nil, http.StatusNotFound, errors.New("not found")
	}
	if err != nil {
		return nil, "", nil, nil, http.StatusInternalServerError, err
	}
	return db, userID, ev, cal, 0, nil
}

// Attendee is an attendee as the event modal shows one.
type Attendee struct {
	Email       string
	DisplayName string
	Status      string // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role        string // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
}

// attendeesOf returns the attendees of an event's first component that have
// an email address; the form cannot show others, and saving keeps them.
func attendeesOf(ev *Event) []Attendee {
	var out []Attendee
	for _, a := range ev.Object.Events[0].Attendees {
		if email := ical.Email(a.Address); email != "" {
			out = append(out, Attendee{Email: email, DisplayName: a.CN, Status: a.PartStat, Role: a.Role})
		}
	}
	return out
}

// EventDetail returns an event detail partial for the modal.
func (h *Pages) EventDetail(w http.ResponseWriter, r *http.Request) {
	_, userID, ev, cal, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	calendars, err := h.plugin.Calendars(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	calEventModal(plugin.CSRFToken(r), ev, cal, calendars, attendeesOf(ev)).Render(r.Context(), w)
}

// eventForm is what the event form edits.
type eventForm struct {
	summary, location, description string
	allDay                         bool
	start, end                     time.Time
	rrule                          string
	attendees                      []ical.Attendee // mailto attendees only
}

func parseEventForm(cal *Calendar, r *http.Request) (*eventForm, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	f := &eventForm{
		summary:     r.FormValue("summary"),
		location:    r.FormValue("location"),
		description: r.FormValue("description"),
		allDay:      r.FormValue("all_day") == "on",
		rrule:       buildRRule(r),
		attendees:   parseAttendeeForm(r),
	}
	var err error
	if f.start, f.end, err = formTimes(cal, r, f.allDay); err != nil {
		return nil, err
	}
	if f.rrule != "" {
		if _, err := rrulelib.StrToRRule(f.rrule); err != nil {
			return nil, fmt.Errorf("Invalid recurrence rule: %w", err)
		}
	}
	return f, nil
}

// UpdateEvent handles POST to update an existing event. Only what the form
// shows changes; everything else in the event, including its overrides,
// exceptions, alarms and whatever a client stored that Cog does not model,
// is kept.
func (h *Pages) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, oldCal, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	calID, _ := strconv.ParseInt(r.FormValue("calendar_id"), 10, 64)
	if calID == 0 {
		calID = ev.CalendarID
	}
	cal, err := h.plugin.Calendar(userID, calID)
	if err != nil {
		http.Error(w, "invalid calendar", http.StatusBadRequest)
		return
	}
	f, err := parseEventForm(cal, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := h.plugin.ctx.Now()
	o := ev.Object
	applyForm(o, f, zonesFor(o, oldCal.Zone()), now)

	updated, err := SaveEvent(db, cal, ev, o, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := SetEventColor(db, updated.ID, r.FormValue("color")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(f.attendees) > 0 {
		h.sendInvitations("REQUEST", updated, f.attendees)
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// applyForm writes a submitted form into an event's first component, with
// the bookkeeping RFC 5545 asks of an organizer's edit: DTSTAMP and
// LAST-MODIFIED always, SEQUENCE when a scheduling field changed. Vendor
// copies of a changed field are dropped.
func applyForm(o *ical.Object, f *eventForm, z ical.Zones, now time.Time) {
	e := o.Events[0]
	scheduling := false
	text := func(field *string, v, prop string, sched bool) {
		if *field != v {
			*field = v
			e.Changed(prop)
			scheduling = scheduling || sched
		}
	}
	text(&e.Summary, f.summary, "SUMMARY", false)
	text(&e.Description, f.description, "DESCRIPTION", false)
	text(&e.Location, f.location, "LOCATION", true)

	oldStart, oldEnd, err := e.Span(z)
	if err != nil || !oldStart.Equal(f.start) || !oldEnd.Equal(f.end) || e.Start.IsDate != f.allDay {
		e.Start = ical.DateTimeOf(f.start, f.allDay)
		e.End = ical.DateTimeOf(f.end, f.allDay)
		e.Duration = ""
		e.Changed("TIME")
		scheduling = true
	}

	if !sameRule(e.RRule, f.rrule) {
		e.RRule = f.rrule
		e.Changed("TIME")
		scheduling = true
		if f.rrule == "" && len(e.RDates) == 0 && !e.IsOverride() {
			// No longer recurring: its exceptions describe nothing.
			e.ExDates = nil
			o.Events = o.Events[:1]
		}
	}

	if mergeAttendees(e, f.attendees) {
		scheduling = true
	}

	stamp := ical.DateTimeOf(now.UTC(), false)
	e.DTStamp, e.LastModified = stamp, stamp
	if scheduling {
		seq := 1
		if e.Sequence != nil {
			seq = *e.Sequence + 1
		}
		e.Sequence = &seq
	}
}

// sameRule compares two RRULEs as the form sees them: the same parts in any
// order, and an UNTIL on the same date, since the form only edits the date.
func sameRule(a, b string) bool {
	pa, pb := rruleParams(a), rruleParams(b)
	if len(pa) != len(pb) {
		return false
	}
	for k, v := range pa {
		w, ok := pb[k]
		if !ok {
			return false
		}
		if k == "UNTIL" && len(v) >= 8 && len(w) >= 8 {
			v, w = v[:8], w[:8]
		}
		if !strings.EqualFold(v, w) {
			return false
		}
	}
	return true
}

// mergeAttendees replaces e's mailto attendees with the form's, keeping what
// the form cannot show (their other parameters, and attendees with other
// addresses), and reports whether who attends, or in what role, changed.
func mergeAttendees(e *ical.Event, form []ical.Attendee) bool {
	byEmail := map[string]ical.Attendee{}
	var out []ical.Attendee
	var before, after []string
	for _, a := range e.Attendees {
		if email := ical.Email(a.Address); email != "" {
			key := strings.ToLower(email)
			byEmail[key] = a
			before = append(before, key+" "+roleOf(a))
		} else {
			out = append(out, a)
		}
	}
	for _, a := range form {
		key := strings.ToLower(ical.Email(a.Address))
		if cur, ok := byEmail[key]; ok {
			cur.CN = a.CN
			if a.Role != roleOf(cur) {
				cur.Role = a.Role
			}
			a = cur
		}
		after = append(after, key+" "+roleOf(a))
		out = append(out, a)
	}
	e.Attendees = out
	return !slices.Equal(before, after)
}

// roleOf returns an attendee's role; absent means REQ-PARTICIPANT (RFC 5545
// §3.2.16).
func roleOf(a ical.Attendee) string {
	if a.Role == "" {
		return "REQ-PARTICIPANT"
	}
	return a.Role
}

// CreateEvent handles POST to create a new event.
func (h *Pages) CreateEvent(w http.ResponseWriter, r *http.Request) {
	db, userID, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	calID, err := strconv.ParseInt(r.FormValue("calendar_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid calendar", http.StatusBadRequest)
		return
	}
	cal, err := h.plugin.Calendar(userID, calID)
	if err != nil {
		http.Error(w, "invalid calendar", http.StatusBadRequest)
		return
	}
	f, err := parseEventForm(cal, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := h.plugin.ctx.Now()
	stamp := ical.DateTimeOf(now.UTC(), false)
	seq := 0
	o := &ical.Object{
		UID:   uuid.NewString(),
		Props: []contentline.Property{{Name: "PRODID", Value: ical.ProdID}},
		Events: []*ical.Event{{
			Summary: f.summary, Description: f.description, Location: f.location,
			Start: ical.DateTimeOf(f.start, f.allDay), End: ical.DateTimeOf(f.end, f.allDay),
			RRule: f.rrule, Attendees: f.attendees, Sequence: &seq,
			DTStamp: stamp, Created: stamp, LastModified: stamp,
		}},
	}
	ev, err := SaveEvent(db, cal, nil, o, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := SetEventColor(db, ev.ID, r.FormValue("color")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(f.attendees) > 0 {
		h.sendInvitations("REQUEST", ev, f.attendees)
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// DeleteEvent handles DELETE to remove an event (HTMX).
func (h *Pages) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	db, _, ev, _, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	var attendees []ical.Attendee
	for _, a := range ev.Object.Events[0].Attendees {
		if ical.Email(a.Address) != "" {
			attendees = append(attendees, a)
		}
	}
	if len(attendees) > 0 {
		h.sendInvitations("CANCEL", ev, attendees)
	}
	if err := DeleteEventByID(db, ev.ID, h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// CreateCalendar handles POST to create a new calendar.
func (h *Pages) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	tz := r.FormValue("timezone")
	if tz != "" && LoadTimezone(tz) == nil {
		http.Error(w, "unknown timezone "+tz, http.StatusBadRequest)
		return
	}
	if name != "" {
		if _, err := h.plugin.CreateCalendar(u.ID, r.FormValue("uid"), name, r.FormValue("color"), "", tz); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/apps/calendar/settings", http.StatusSeeOther)
}

// UpdateCalendarHandler handles POST to rename, recolor or rezone a calendar.
func (h *Pages) UpdateCalendarHandler(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	cal, err := h.plugin.Calendar(u.ID, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = cal.Name
	}
	color := r.FormValue("color")
	if color == "" {
		color = cal.Color
	}
	tz := r.FormValue("timezone")
	if tz != "" && LoadTimezone(tz) == nil {
		http.Error(w, "unknown timezone "+tz, http.StatusBadRequest)
		return
	}
	if err := h.plugin.UpdateCalendar(u.Username, u.ID, id, name, color, tz); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/calendar/settings", http.StatusSeeOther)
}

// formTimes parses an event form's start and end. Date-times are read in the
// form's tz field (the browser's zone), falling back to the calendar's zone,
// and keep that zone so the event records it as TZID.
func formTimes(cal *Calendar, r *http.Request, isAllDay bool) (time.Time, time.Time, error) {
	if isAllDay {
		start, err := time.Parse("2006-01-02", r.FormValue("date_start"))
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start date")
		}
		end := start.AddDate(0, 0, 1)
		if v := r.FormValue("date_end"); v != "" {
			if end, err = time.Parse("2006-01-02", v); err != nil {
				return time.Time{}, time.Time{}, fmt.Errorf("invalid end date")
			}
		}
		return start, end, nil
	}

	loc := time.UTC
	if tz := r.FormValue("tz"); tz != "" {
		if loc = LoadTimezone(tz); loc == nil {
			return time.Time{}, time.Time{}, fmt.Errorf("unknown timezone %q", tz)
		}
	} else {
		loc = cal.Zone()
	}
	start, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("datetime_start"), loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start time")
	}
	end := start.Add(time.Hour)
	if v := r.FormValue("datetime_end"); v != "" {
		if end, err = time.ParseInLocation("2006-01-02T15:04", v, loc); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end time")
		}
	}
	return start, end, nil
}

func buildRRule(r *http.Request) string {
	freq := r.FormValue("rrule_freq")
	if freq == "CUSTOM" {
		v := strings.TrimSpace(r.FormValue("rrule"))
		return strings.TrimPrefix(v, "RRULE:")
	}
	if freq == "" || freq == "NONE" {
		return ""
	}
	parts := []string{"FREQ=" + freq}
	if interval := r.FormValue("rrule_interval"); interval != "" && interval != "1" {
		parts = append(parts, "INTERVAL="+interval)
	}
	if freq == "WEEKLY" {
		if days := r.Form["rrule_byday"]; len(days) > 0 {
			parts = append(parts, "BYDAY="+strings.Join(days, ","))
		}
	}
	if until := r.FormValue("rrule_until"); until != "" {
		t, err := time.Parse("2006-01-02", until)
		if err == nil {
			parts = append(parts, "UNTIL="+t.Format("20060102T150405Z"))
		}
	}
	return strings.Join(parts, ";")
}

// RRulePreview returns an HTMX partial with the human-readable RRULE or an error.
func (h *Pages) RRulePreview(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("rrule"))
	raw = strings.TrimPrefix(raw, "RRULE:")
	if raw == "" {
		return
	}
	var msg string
	if _, err := rrulelib.StrToRRule(raw); err != nil {
		msg = fmt.Sprintf(`<span style="color:var(--color-danger,red)">Invalid RRULE: %s</span>`, template.HTMLEscapeString(err.Error()))
	} else {
		msg = fmt.Sprintf(`<span>%s</span>`, template.HTMLEscapeString(FormatRRule(raw)))
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		h.plugin.log.Debug("write rrule preview", "err", err)
	}
}

func eventRRule(ev *Event) string { return FormatRRule(ev.RRule) }

func eventRRuleRaw(ev *Event) string { return ev.RRule }

// rruleParams parses an RRULE string into key=value pairs.
func rruleParams(rrule string) map[string]string {
	m := make(map[string]string)
	for part := range strings.SplitSeq(rrule, ";") {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			m[strings.ToUpper(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
		}
	}
	return m
}

// eventRRuleIsSimple returns true if the RRULE can be represented by the GUI dropdowns
// (FREQ + optional INTERVAL + optional BYDAY for WEEKLY + optional UNTIL).
func eventRRuleIsSimple(ev *Event) bool {
	raw := eventRRuleRaw(ev)
	if raw == "" {
		return true
	}
	p := rruleParams(raw)
	freq := p["FREQ"]
	if freq != "DAILY" && freq != "WEEKLY" && freq != "MONTHLY" && freq != "YEARLY" {
		return false
	}
	for k := range p {
		switch k {
		case "FREQ", "INTERVAL", "UNTIL":
			// always ok
		case "BYDAY":
			if freq != "WEEKLY" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func eventRRuleFreq(ev *Event) string {
	raw := eventRRuleRaw(ev)
	if raw == "" {
		return "NONE"
	}
	if !eventRRuleIsSimple(ev) {
		return "CUSTOM"
	}
	return rruleParams(raw)["FREQ"]
}

func eventRRuleInterval(ev *Event) string {
	p := rruleParams(eventRRuleRaw(ev))
	if v, ok := p["INTERVAL"]; ok {
		return v
	}
	return "1"
}

func eventRRuleByDay(ev *Event) map[string]bool {
	p := rruleParams(eventRRuleRaw(ev))
	m := make(map[string]bool)
	if days, ok := p["BYDAY"]; ok {
		for d := range strings.SplitSeq(days, ",") {
			m[strings.TrimSpace(d)] = true
		}
	}
	return m
}

func eventRRuleUntil(ev *Event) string {
	p := rruleParams(eventRRuleRaw(ev))
	if u, ok := p["UNTIL"]; ok {
		// Try to parse RRULE UNTIL format and return as date input value
		for _, layout := range []string{"20060102T150405Z", "20060102"} {
			if t, err := time.Parse(layout, u); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	return ""
}

func hasByDay(days map[string]bool, day string) bool {
	return days[day]
}

// parseAttendeeForm extracts attendees from form data: parallel arrays
// attendee_email[], attendee_name[], attendee_role[]. The form has been
// parsed.
func parseAttendeeForm(r *http.Request) []ical.Attendee {
	emails := r.Form["attendee_email"]
	names := r.Form["attendee_name"]
	roles := r.Form["attendee_role"]
	var result []ical.Attendee
	for i, email := range emails {
		email = strings.TrimSpace(email)
		if email == "" {
			continue
		}
		name := ""
		if i < len(names) {
			name = strings.TrimSpace(names[i])
		}
		role := "REQ-PARTICIPANT"
		if i < len(roles) && roles[i] != "" {
			role = roles[i]
		}
		result = append(result, ical.Attendee{
			Address:  "mailto:" + email,
			CN:       name,
			PartStat: "NEEDS-ACTION",
			Role:     role,
			RSVP:     "TRUE",
		})
	}
	return result
}

// inviteICS writes an event as an iTIP message (RFC 5546): the stored event
// with METHOD, and for a cancellation, STATUS:CANCELLED.
func inviteICS(ev *Event, method string) ([]byte, error) {
	o := *ev.Object
	o.Props = append([]contentline.Property{{Name: "METHOD", Value: method}}, o.Props...)
	if method == "CANCEL" {
		o.Events = make([]*ical.Event, len(ev.Object.Events))
		for i, e := range ev.Object.Events {
			c := *e
			c.Status = "CANCELLED"
			o.Events[i] = &c
		}
	}
	var b bytes.Buffer
	if err := contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// sendInvitations sends email invitations/cancellations to attendees.
func (h *Pages) sendInvitations(method string, ev *Event, attendees []ical.Attendee) {
	emailSender := h.plugin.ctx.EmailSender()
	if emailSender == nil || len(attendees) == 0 {
		return
	}
	data, err := inviteICS(ev, method)
	if err != nil {
		h.plugin.log.Error("build invitation", "method", method, "uid", ev.UID, "err", err)
		return
	}

	var subject string
	switch method {
	case "CANCEL":
		subject = fmt.Sprintf("Event Cancelled: %s", ev.Summary)
	default:
		subject = fmt.Sprintf("Invitation: %s", ev.Summary)
	}

	body := fmt.Sprintf("Event: %s\nWhen: %s\n", ev.Summary, formatEventTime(ev.DTStart, ev.DTEnd, ev.IsAllDay))
	if ev.Location != "" {
		body += fmt.Sprintf("Where: %s\n", ev.Location)
	}
	if ev.Description != "" {
		body += fmt.Sprintf("\n%s\n", ev.Description)
	}
	if method == "CANCEL" {
		body += "\nThis event has been cancelled.\n"
	}

	for _, att := range attendees {
		to := ical.Email(att.Address)
		err := emailSender.SendWithAttachments(to, subject, body, "", []plugin.EmailAttachment{{
			Filename:    "invite.ics",
			ContentType: "text/calendar; method=" + method,
			Data:        data,
		}})
		if err != nil {
			h.plugin.log.Error("send invitation", "method", method, "uid", ev.UID, "to", to, "err", err)
		}
	}
}

// RespondToInvite handles POST to accept/decline an event invitation.
func (h *Pages) RespondToInvite(w http.ResponseWriter, r *http.Request) {
	db, _, ev, cal, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	email := r.FormValue("email")
	answer := r.FormValue("status")
	if answer != "ACCEPTED" && answer != "DECLINED" && answer != "TENTATIVE" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	found := false
	for _, e := range ev.Object.Events {
		for i := range e.Attendees {
			if strings.EqualFold(ical.Email(e.Attendees[i].Address), email) {
				e.Attendees[i].PartStat = answer
				found = true
			}
		}
	}
	if !found {
		http.Error(w, "not an attendee", http.StatusNotFound)
		return
	}
	if _, err := SaveEvent(db, cal, ev, ev.Object, h.plugin.ctx.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// DeleteCalendarHandler handles DELETE to remove a calendar (HTMX).
func (h *Pages) DeleteCalendarHandler(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.plugin.DeleteCalendar(u.Username, u.ID, id); err != nil {
		if errors.Is(err, dav.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Notifications returns due (unfired) reminder notifications as JSON.
// GET /apps/calendar/notifications
func (h *Pages) Notifications(w http.ResponseWriter, r *http.Request) {
	db, _, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	notifications, err := DueNotifications(db, h.plugin.ctx.Now().Unix())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type notifJSON struct {
		ID      int64  `json:"id"`
		EventID int64  `json:"event_id"`
		Summary string `json:"summary"`
		Start   string `json:"start"`
	}

	result := []notifJSON{}
	for _, n := range notifications {
		ev, err := GetEvent(db, n.EventID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result = append(result, notifJSON{
			ID:      n.ID,
			EventID: n.EventID,
			Summary: ev.Summary,
			Start:   time.Unix(ev.DTStart, 0).UTC().Format(time.RFC3339),
		})
		if err := MarkNotificationFired(db, n.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	plugin.WriteJSON(w, http.StatusOK, result)
}
