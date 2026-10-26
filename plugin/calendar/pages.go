package calendar

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
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
					// Free/Busy only: strip sensitive details.
					ev.Summary = "Busy"
					ev.Description = ""
					ev.Location = ""
					events = append(events, ev)
				}
			}
		}
	}

	// Expand recurring events
	var expanded []EventWithCalendar
	for _, ev := range events {
		expanded = append(expanded, expandRRule(ev, start, end)...)
	}
	events = expanded

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

	result := make([]fcEvent, 0)
	for _, ev := range events {
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

// ownEvent returns the request user's event by path ID, with its calendar.
// Events of a calendar the user does not own (or that no longer exists) are
// not found.
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

// EventDetail returns an event detail partial for the modal.
func (h *Pages) EventDetail(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, cal, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	calendars, err := h.plugin.Calendars(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attendees, err := ListAttendees(db, ev.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	calEventModal(plugin.CSRFToken(r), ev, cal, calendars, attendees).Render(r.Context(), w)
}

// UpdateEvent handles POST to update an existing event.
func (h *Pages) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, _, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	summary := r.FormValue("summary")
	location := r.FormValue("location")
	description := r.FormValue("description")
	isAllDay := r.FormValue("all_day") == "on"
	calID, _ := strconv.ParseInt(r.FormValue("calendar_id"), 10, 64)
	if calID == 0 {
		calID = ev.CalendarID
	}
	cal, err := h.plugin.Calendar(userID, calID)
	if err != nil {
		http.Error(w, "invalid calendar", http.StatusBadRequest)
		return
	}

	dtstart, dtend, err := formTimes(cal, r, isAllDay)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	rrule := buildRRule(r)
	if rrule != "" {
		if _, err := rrulelib.StrToRRule(rrule); err != nil {
			http.Error(w, "Invalid recurrence rule: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// The form doesn't edit reminders; carry them over from the stored ICS.
	var alarms []VAlarm
	if old, err := ParseVEvent(ev.ICSData); err == nil {
		alarms = old.Alarms
	}

	attendeeFields := parseAttendeeForm(r)
	icsData := GenerateICSFull(ev.UID, summary, description, location, dtstart, dtend, isAllDay, rrule, alarms, attendeeFields, h.plugin.ctx.Now())
	updated, err := SaveEvent(db, cal, ev, icsData, h.plugin.ctx.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := SetEventColor(db, updated.ID, r.FormValue("color")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := ReplaceAttendees(db, updated.ID, attendeeFields); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(attendeeFields) > 0 {
		h.sendInvitations("REQUEST", updated, summary, description, location, dtstart, dtend, isAllDay, attendeeFields)
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
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
	summary := r.FormValue("summary")
	location := r.FormValue("location")
	description := r.FormValue("description")
	isAllDay := r.FormValue("all_day") == "on"

	dtstart, dtend, err := formTimes(cal, r, isAllDay)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	uid := uuid.NewString()
	rrule := buildRRule(r)
	if rrule != "" {
		if _, err := rrulelib.StrToRRule(rrule); err != nil {
			http.Error(w, "Invalid recurrence rule: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	attendeeFields := parseAttendeeForm(r)
	icsData := GenerateICSFull(uid, summary, description, location, dtstart, dtend, isAllDay, rrule, nil, attendeeFields, h.plugin.ctx.Now())
	ev, err := SaveEvent(db, cal, nil, icsData, h.plugin.ctx.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := SetEventColor(db, ev.ID, r.FormValue("color")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(attendeeFields) > 0 {
		if err := ReplaceAttendees(db, ev.ID, attendeeFields); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.sendInvitations("REQUEST", ev, summary, description, location, dtstart, dtend, isAllDay, attendeeFields)
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

	// Send cancellation emails before deleting
	attendees, err := ListAttendees(db, ev.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(attendees) > 0 {
		dtstart := time.Unix(ev.DTStart, 0).UTC()
		dtend := time.Unix(ev.DTEnd, 0).UTC()
		var afs []AttendeeField
		for _, a := range attendees {
			afs = append(afs, AttendeeField{
				Email:    a.Email,
				CN:       a.DisplayName,
				PartStat: a.Status,
				Role:     a.Role,
			})
		}
		h.sendInvitations("CANCEL", ev, ev.Summary, ev.Description, ev.Location, dtstart, dtend, ev.IsAllDay, afs)
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
// and keep that zone so the ICS records it as TZID.
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
	r.ParseForm()
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
		w.Write([]byte(""))
		return
	}
	_, err := rrulelib.StrToRRule(raw)
	if err != nil {
		fmt.Fprintf(w, `<span style="color:var(--color-danger,red)">Invalid RRULE: %s</span>`, template.HTMLEscapeString(err.Error()))
		return
	}
	desc := FormatRRule(raw)
	fmt.Fprintf(w, `<span>%s</span>`, template.HTMLEscapeString(desc))
}

func eventRRule(ev *Event) string {
	fields, err := ParseVEvent(ev.ICSData)
	if err != nil || fields.RRule == "" {
		return ""
	}
	return FormatRRule(fields.RRule)
}

func eventRRuleRaw(ev *Event) string {
	fields, err := ParseVEvent(ev.ICSData)
	if err != nil {
		return ""
	}
	return fields.RRule
}

// rruleParams parses an RRULE string into key=value pairs.
func rruleParams(rrule string) map[string]string {
	m := make(map[string]string)
	for part := range strings.SplitSeq(rrule, ";") {
		if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
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

// parseAttendeeForm extracts attendee fields from form data.
// Expects parallel arrays: attendee_email[], attendee_name[], attendee_role[]
func parseAttendeeForm(r *http.Request) []AttendeeField {
	if err := r.ParseForm(); err != nil {
		return nil
	}
	emails := r.Form["attendee_email"]
	names := r.Form["attendee_name"]
	roles := r.Form["attendee_role"]
	var result []AttendeeField
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
		result = append(result, AttendeeField{
			Email:    email,
			CN:       name,
			PartStat: "NEEDS-ACTION",
			Role:     role,
		})
	}
	return result
}

// sendInvitations sends email invitations/cancellations to attendees.
func (h *Pages) sendInvitations(method string, ev *Event, summary, description, location string, dtstart, dtend time.Time, isAllDay bool, attendees []AttendeeField) {
	emailSender := h.plugin.ctx.EmailSender()
	if emailSender == nil || len(attendees) == 0 {
		return
	}
	icsData := GenerateICSInvite(method, ev.UID, summary, description, location, dtstart, dtend, isAllDay, attendees, h.plugin.ctx.Now())

	var subject string
	switch method {
	case "CANCEL":
		subject = fmt.Sprintf("Event Cancelled: %s", summary)
	default:
		subject = fmt.Sprintf("Invitation: %s", summary)
	}

	var timeStr string
	if isAllDay {
		timeStr = dtstart.Format("Mon, Jan 2 2006")
	} else {
		timeStr = fmt.Sprintf("%s - %s", dtstart.Format("Mon, Jan 2 2006 15:04"), dtend.Format("15:04"))
	}

	body := fmt.Sprintf("Event: %s\nWhen: %s\n", summary, timeStr)
	if location != "" {
		body += fmt.Sprintf("Where: %s\n", location)
	}
	if description != "" {
		body += fmt.Sprintf("\n%s\n", description)
	}
	if method == "CANCEL" {
		body += "\nThis event has been cancelled.\n"
	}

	for _, att := range attendees {
		err := emailSender.SendWithAttachments(
			att.Email,
			subject,
			body,
			"",
			[]plugin.EmailAttachment{{
				Filename:    "invite.ics",
				ContentType: "text/calendar; method=" + method,
				Data:        []byte(icsData),
			}},
		)
		if err != nil {
			h.plugin.log.Error("send invitation", "method", method, "uid", ev.UID, "to", att.Email, "err", err)
		}
	}
}

// RespondToInvite handles POST to accept/decline an event invitation.
func (h *Pages) RespondToInvite(w http.ResponseWriter, r *http.Request) {
	db, _, ev, _, status, err := h.ownEvent(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	responseEmail := r.FormValue("email")
	answer := r.FormValue("status")
	if answer != "ACCEPTED" && answer != "DECLINED" && answer != "TENTATIVE" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	if err := UpdateAttendeeStatus(db, ev.ID, responseEmail, answer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func expandRRule(ev EventWithCalendar, rangeStart, rangeEnd time.Time) []EventWithCalendar {
	fields, err := ParseVEventIn(ev.ICSData, ev.Zone())
	if err != nil || fields.RRule == "" {
		return []EventWithCalendar{ev}
	}

	rule, err := rrulelib.StrToRRule(fields.RRule)
	if err != nil {
		return []EventWithCalendar{ev}
	}
	// DTSTART keeps its zone, so occurrences land on the same local time
	// across DST changes.
	rule.DTStart(fields.DTStart)
	var set rrulelib.Set
	set.RRule(rule)
	for _, ex := range fields.ExDates {
		set.ExDate(ex)
	}

	duration := time.Unix(ev.DTEnd, 0).Sub(time.Unix(ev.DTStart, 0))
	occurrences := set.Between(rangeStart.Add(-duration), rangeEnd, true)

	var result []EventWithCalendar
	for _, occ := range occurrences {
		occEnd := occ.Add(duration)
		if occEnd.Before(rangeStart) {
			continue
		}
		e := ev
		e.DTStart = occ.Unix()
		e.DTEnd = occEnd.Unix()
		result = append(result, e)
	}
	return result
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
