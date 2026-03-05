package calendar

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	plugpkg "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

type navProvider interface {
	NavItems() []plugin.NavItem
}

// Pages holds the web UI handlers for the calendar plugin.
type Pages struct {
	plugin *Plugin
	secret []byte
	nav    navProvider
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin, secret []byte, nav navProvider) *Pages {
	return &Pages{plugin: p, secret: secret, nav: nav}
}

func (h *Pages) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, h.secret)
	}
	var isAdmin bool
	if u != nil {
		isAdmin = u.IsAdmin
	}

	var nav []ui.NavItem
	for _, item := range h.nav.NavItems() {
		active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
		nav = append(nav, ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		})
	}

	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         nav,
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
	}
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := auth.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

// Index shows the FullCalendar UI.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	EnsureDefaultCalendar(db)

	pd := h.pageData(r, "Calendar")
	calendars, _ := ListCalendars(db)
	calIndexPage(pd, calendars).Render(r.Context(), w)
}

// Settings shows calendar management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pd := h.pageData(r, "Calendar Settings")
	calendars, _ := ListCalendars(db)
	calSettingsPage(pd, calendars).Render(r.Context(), w)
}

// EventsJSON returns events as JSON for FullCalendar.
func (h *Pages) EventsJSON(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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

	u := auth.UserFromContext(r.Context())

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
				if sc.Permissions&plugpkg.PermRead != 0 {
					// Full read access: include all details.
					ev.CalendarName = sc.Name + " (" + sc.OwnerUsername + ")"
					events = append(events, ev)
				} else if sc.Permissions&plugpkg.PermFreeBusy != 0 {
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
	db, err := h.userDB(r)
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

	pd := h.pageData(r, "Event")
	calEventModal(pd, ev, cal, calendars, attendees).Render(r.Context(), w)
}

// UpdateEvent handles POST to update an existing event.
func (h *Pages) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	db, err := h.userDB(r)
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
	db, err := h.userDB(r)
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
	db, err := h.userDB(r)
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
	freq := r.FormValue("recurrence")
	if freq == "" {
		return ""
	}
	rrule := "FREQ=" + freq
	if interval := r.FormValue("recurrence_interval"); interval != "" && interval != "1" {
		rrule += ";INTERVAL=" + interval
	}
	if freq == "WEEKLY" {
		if err := r.ParseForm(); err == nil {
			days := r.Form["recurrence_byday"]
			if len(days) > 0 {
				rrule += ";BYDAY=" + strings.Join(days, ",")
			}
		}
	}
	if until := r.FormValue("recurrence_until"); until != "" {
		if t, err := time.Parse("2006-01-02", until); err == nil {
			rrule += ";UNTIL=" + t.Format("20060102") + "T235959Z"
		}
	}
	return rrule
}

func eventRRule(ev *Event) string {
	fields, err := ParseVEvent(ev.ICSData)
	if err != nil || fields.RRule == "" {
		return ""
	}
	return FormatRRule(fields.RRule)
}

func eventRRuleFreq(ev *Event) string {
	fields, _ := ParseVEvent(ev.ICSData)
	if fields.RRule == "" {
		return ""
	}
	for part := range strings.SplitSeq(fields.RRule, ";") {
		k, v, _ := strings.Cut(part, "=")
		if strings.ToUpper(k) == "FREQ" {
			return v
		}
	}
	return ""
}

func eventRRuleInterval(ev *Event) string {
	fields, _ := ParseVEvent(ev.ICSData)
	if fields.RRule == "" {
		return "1"
	}
	for part := range strings.SplitSeq(fields.RRule, ";") {
		k, v, _ := strings.Cut(part, "=")
		if strings.ToUpper(k) == "INTERVAL" {
			return v
		}
	}
	return "1"
}

func eventRRuleUntil(ev *Event) string {
	fields, _ := ParseVEvent(ev.ICSData)
	if fields.RRule == "" {
		return ""
	}
	for part := range strings.SplitSeq(fields.RRule, ";") {
		k, v, _ := strings.Cut(part, "=")
		if strings.ToUpper(k) == "UNTIL" {
			if t, err := time.Parse("20060102T150405Z", v); err == nil {
				return t.Format("2006-01-02")
			}
			if t, err := time.Parse("20060102", v); err == nil {
				return t.Format("2006-01-02")
			}
		}
	}
	return ""
}

func eventRRuleByDay(ev *Event) []string {
	fields, _ := ParseVEvent(ev.ICSData)
	if fields.RRule == "" {
		return nil
	}
	for part := range strings.SplitSeq(fields.RRule, ";") {
		k, v, _ := strings.Cut(part, "=")
		if strings.ToUpper(k) == "BYDAY" {
			return strings.Split(v, ",")
		}
	}
	return nil
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
	if h.plugin.email == nil || len(attendees) == 0 {
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
		_ = h.plugin.email.Send(
			[]string{att.Email},
			subject,
			body,
			email.Attachment{
				Filename:    "invite.ics",
				ContentType: "text/calendar; method=" + method,
				Data:        []byte(icsData),
			},
		)
	}
}

// RespondToInvite handles POST to accept/decline an event invitation.
func (h *Pages) RespondToInvite(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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

	params := make(map[string]string)
	for part := range strings.SplitSeq(fields.RRule, ";") {
		k, v, _ := strings.Cut(part, "=")
		params[strings.ToUpper(k)] = v
	}

	freq := params["FREQ"]
	interval := 1
	if v, ok := params["INTERVAL"]; ok {
		fmt.Sscanf(v, "%d", &interval)
	}

	var until time.Time
	if v, ok := params["UNTIL"]; ok {
		until, _ = time.Parse("20060102T150405Z", v)
		if until.IsZero() {
			until, _ = time.Parse("20060102", v)
		}
	}
	if until.IsZero() {
		until = rangeEnd
	}

	count := 0
	maxCount := 365
	if v, ok := params["COUNT"]; ok {
		fmt.Sscanf(v, "%d", &maxCount)
	}

	duration := time.Unix(ev.DTEnd, 0).Sub(time.Unix(ev.DTStart, 0))
	var result []EventWithCalendar
	cur := fields.DTStart

	// BYDAY for weekly
	var byDays []time.Weekday
	if freq == "WEEKLY" {
		if bd, ok := params["BYDAY"]; ok {
			dayMap := map[string]time.Weekday{
				"MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
				"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday, "SU": time.Sunday,
			}
			for d := range strings.SplitSeq(bd, ",") {
				if wd, ok := dayMap[strings.TrimSpace(d)]; ok {
					byDays = append(byDays, wd)
				}
			}
		}
	}

	for count < maxCount && !cur.After(until) && !cur.After(rangeEnd) {
		occEnd := cur.Add(duration)
		if !occEnd.Before(rangeStart) && !cur.After(rangeEnd) {
			if len(byDays) > 0 {
				matched := slices.Contains(byDays, cur.Weekday())
				if !matched {
					cur = cur.AddDate(0, 0, 1)
					continue
				}
			}
			occ := ev
			occ.DTStart = cur.Unix()
			occ.DTEnd = occEnd.Unix()
			result = append(result, occ)
		}
		count++
		switch freq {
		case "DAILY":
			cur = cur.AddDate(0, 0, interval)
		case "WEEKLY":
			if len(byDays) > 0 {
				cur = cur.AddDate(0, 0, 1)
			} else {
				cur = cur.AddDate(0, 0, 7*interval)
			}
		case "MONTHLY":
			cur = cur.AddDate(0, interval, 0)
		case "YEARLY":
			cur = cur.AddDate(interval, 0, 0)
		default:
			return []EventWithCalendar{ev}
		}
	}

	if len(result) == 0 {
		return []EventWithCalendar{ev}
	}
	return result
}

// DeleteCalendarHandler handles DELETE to remove a calendar (HTMX).
func (h *Pages) DeleteCalendarHandler(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	db, err := h.userDB(r)
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
