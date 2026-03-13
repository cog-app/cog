package calendar

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
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

// Index shows the FullCalendar UI.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	EnsureDefaultCalendar(db)

	plugin.SetTitle(w, "Calendar")
	calendars, _ := ListCalendars(db)
	calIndexPage(calendars, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Settings shows calendar management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Calendar Settings")
	calendars, _ := ListCalendars(db)
	calSettingsPage(calendars, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// EventsJSON returns events as JSON for FullCalendar.
func (h *Pages) EventsJSON(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
		start = time.Now().AddDate(0, -1, 0)
	}
	if end.IsZero() {
		end = time.Now().AddDate(0, 2, 0)
	}

	u := plugin.UserFromContext(r.Context())

	var events []EventWithCalendar
	if calIDStr != "" {
		calID, _ := strconv.ParseInt(calIDStr, 10, 64)
		raw, _ := EventsInRangeForCalendar(db, calID, start.Unix(), end.Unix())
		cal, err := GetCalendar(db, calID)
		if err == nil {
			for _, ev := range raw {
				events = append(events, EventWithCalendar{
					Event:         ev,
					CalendarName:  cal.Name,
					CalendarColor: cal.Color,
				})
			}
		}
	} else {
		events, _ = EventsInRange(db, start.Unix(), end.Unix())

		// Also include events from calendars shared with this user.
		shared, _ := h.plugin.SharedCalendars(u.Username)
		for _, sc := range shared {
			ownerDB, err := h.plugin.DB(sc.OwnerUsername)
			if err != nil {
				continue
			}
			sharedEvents, _ := EventsInRange(ownerDB, start.Unix(), end.Unix())
			for _, ev := range sharedEvents {
				if ev.CalendarID != sc.ID {
					continue
				}
				if sc.Permissions&plugin.PermRead != 0 {
					// Full read access: include all details.
					ev.CalendarName = sc.Name + " (" + sc.OwnerUsername + ")"
					events = append(events, ev)
				} else if sc.Permissions&plugin.PermFreeBusy != 0 {
					// Free/Busy only: strip sensitive details.
					ev.Summary = "Busy"
					ev.Description = ""
					ev.Location = ""
					ev.CalendarName = sc.Name + " (" + sc.OwnerUsername + ")"
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// EventDetail returns an event detail partial for the modal.
func (h *Pages) EventDetail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ev, err := GetEvent(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	cal, _ := GetCalendar(db, ev.CalendarID)
	calendars, _ := ListCalendars(db)
	attendees, _ := ListAttendees(db, ev.ID)

	calEventModal(plugin.CSRFToken(r), ev, cal, calendars, attendees).Render(r.Context(), w)
}

// UpdateEvent handles POST to update an existing event.
func (h *Pages) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ev, err := GetEvent(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
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

	var dtstart, dtend time.Time
	if isAllDay {
		dtstart, _ = time.Parse("2006-01-02", r.FormValue("date_start"))
		dtend, _ = time.Parse("2006-01-02", r.FormValue("date_end"))
		if dtend.IsZero() {
			dtend = dtstart.AddDate(0, 0, 1)
		}
	} else {
		dtstart, _ = time.Parse("2006-01-02T15:04", r.FormValue("datetime_start"))
		dtend, _ = time.Parse("2006-01-02T15:04", r.FormValue("datetime_end"))
		if dtend.IsZero() {
			dtend = dtstart.Add(time.Hour)
		}
	}

	eventColor := r.FormValue("color")
	rrule := buildRRule(r)
	if rrule != "" {
		if _, err := rrulelib.StrToRRule(rrule); err != nil {
			http.Error(w, "Invalid recurrence rule: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Parse attendees from form
	attendeeFields := parseAttendeeForm(r)
	icsData := GenerateICSFull(ev.UID, summary, description, location, dtstart, dtend, isAllDay, rrule, nil, attendeeFields)
	fields := VEventFields{
		UID: ev.UID, Summary: summary, Description: description, Location: location,
		DTStart: dtstart, DTEnd: dtend, IsAllDay: isAllDay, Color: eventColor, RRule: rrule,
		Attendees: attendeeFields,
	}

	updated, err := UpsertEvent(db, calID, ev.UID, icsData, fields)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create alarm notifications.
	if updated != nil {
		EnsureNotifications(db, updated.ID, updated.DTStart, icsData)
	}

	// Replace attendees and send updated invitations
	if updated != nil {
		DeleteAttendees(db, updated.ID)
		for _, af := range attendeeFields {
			AddAttendee(db, updated.ID, af.Email, "", af.CN, af.Role)
		}
		if len(attendeeFields) > 0 {
			h.sendInvitations("REQUEST", updated, summary, description, location, dtstart, dtend, isAllDay, attendeeFields)
		}
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// CreateEvent handles POST to create a new event.
func (h *Pages) CreateEvent(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	calID, _ := strconv.ParseInt(r.FormValue("calendar_id"), 10, 64)
	summary := r.FormValue("summary")
	location := r.FormValue("location")
	description := r.FormValue("description")
	isAllDay := r.FormValue("all_day") == "on"

	var dtstart, dtend time.Time
	if isAllDay {
		dtstart, _ = time.Parse("2006-01-02", r.FormValue("date_start"))
		dtend, _ = time.Parse("2006-01-02", r.FormValue("date_end"))
		if dtend.IsZero() {
			dtend = dtstart.AddDate(0, 0, 1)
		}
	} else {
		dtstart, _ = time.Parse("2006-01-02T15:04", r.FormValue("datetime_start"))
		dtend, _ = time.Parse("2006-01-02T15:04", r.FormValue("datetime_end"))
		if dtend.IsZero() {
			dtend = dtstart.Add(time.Hour)
		}
	}

	eventColor := r.FormValue("color")
	uid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), summary)
	rrule := buildRRule(r)
	if rrule != "" {
		if _, err := rrulelib.StrToRRule(rrule); err != nil {
			http.Error(w, "Invalid recurrence rule: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Parse attendees from form
	attendeeFields := parseAttendeeForm(r)
	icsData := GenerateICSFull(uid, summary, description, location, dtstart, dtend, isAllDay, rrule, nil, attendeeFields)
	fields := VEventFields{
		UID: uid, Summary: summary, Description: description, Location: location,
		DTStart: dtstart, DTEnd: dtend, IsAllDay: isAllDay, Color: eventColor, RRule: rrule,
		Attendees: attendeeFields,
	}

	ev, err := UpsertEvent(db, calID, uid, icsData, fields)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create alarm notifications.
	if ev != nil {
		EnsureNotifications(db, ev.ID, ev.DTStart, icsData)
	}

	// Save attendees to DB and send invitations
	if ev != nil && len(attendeeFields) > 0 {
		for _, af := range attendeeFields {
			AddAttendee(db, ev.ID, af.Email, "", af.CN, af.Role)
		}
		h.sendInvitations("REQUEST", ev, summary, description, location, dtstart, dtend, isAllDay, attendeeFields)
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// DeleteEvent handles DELETE to remove an event (HTMX).
func (h *Pages) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// Send cancellation emails before deleting
	ev, err := GetEvent(db, id)
	if err == nil {
		attendees, _ := ListAttendees(db, ev.ID)
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
	}

	DeleteEventByID(db, id)
	w.WriteHeader(http.StatusOK)
}

// CreateCalendar handles POST to create a new calendar.
func (h *Pages) CreateCalendar(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	uid := r.FormValue("uid")
	name := r.FormValue("name")
	color := r.FormValue("color")
	if uid == "" {
		uid = name
	}
	if name != "" {
		CreateCalendar(db, uid, name, color, "")
	}
	http.Redirect(w, r, "/apps/calendar/settings", http.StatusSeeOther)
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
	icsData := GenerateICSInvite(method, ev.UID, summary, description, location, dtstart, dtend, isAllDay, attendees)

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
		_ = emailSender.SendWithAttachments(
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
	}
}

// RespondToInvite handles POST to accept/decline an event invitation.
func (h *Pages) RespondToInvite(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	responseEmail := r.FormValue("email")
	status := r.FormValue("status")
	if status != "ACCEPTED" && status != "DECLINED" && status != "TENTATIVE" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	UpdateAttendeeStatus(db, id, responseEmail, status)
	w.WriteHeader(http.StatusOK)
}

func expandRRule(ev EventWithCalendar, rangeStart, rangeEnd time.Time) []EventWithCalendar {
	fields, err := ParseVEvent(ev.ICSData)
	if err != nil || fields.RRule == "" {
		return []EventWithCalendar{ev}
	}

	rule, err := rrulelib.StrToRRule(fields.RRule)
	if err != nil {
		return []EventWithCalendar{ev}
	}
	rule.DTStart(fields.DTStart)

	duration := time.Unix(ev.DTEnd, 0).Sub(time.Unix(ev.DTStart, 0))
	occurrences := rule.Between(rangeStart.Add(-duration), rangeEnd, true)

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

	if len(result) == 0 {
		return []EventWithCalendar{ev}
	}
	return result
}

// DeleteCalendarHandler handles DELETE to remove a calendar (HTMX).
func (h *Pages) DeleteCalendarHandler(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteCalendar(db, id)
	w.WriteHeader(http.StatusOK)
}

// Notifications returns due (unfired) reminder notifications as JSON.
// GET /apps/calendar/notifications
func (h *Pages) Notifications(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	notifications, err := DueNotifications(db)
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

	var result []notifJSON
	for _, n := range notifications {
		ev, err := GetEvent(db, n.EventID)
		if err != nil {
			continue
		}
		start := time.Unix(ev.DTStart, 0).UTC()
		result = append(result, notifJSON{
			ID:      n.ID,
			EventID: n.EventID,
			Summary: ev.Summary,
			Start:   start.Format(time.RFC3339),
		})
		MarkNotificationFired(db, n.ID)
	}

	if result == nil {
		result = []notifJSON{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
