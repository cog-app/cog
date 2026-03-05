package calendar

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
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
	}

	type fcEvent struct {
		ID    int64  `json:"id"`
		Title string `json:"title"`
		Start string `json:"start"`
		End   string `json:"end"`
		AllDay bool  `json:"allDay"`
		Color string `json:"color"`
		ExtendedProps struct {
			Location    string `json:"location"`
			Description string `json:"description"`
			CalendarID  int64  `json:"calendarId"`
		} `json:"extendedProps"`
	}

	var result []fcEvent
	for _, ev := range events {
		fc := fcEvent{
			ID:    ev.ID,
			Title: ev.Summary,
			AllDay: ev.IsAllDay,
			Color: ev.CalendarColor,
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

	pd := h.pageData(r, "Event")
	calEventModal(pd, ev, cal, calendars).Render(r.Context(), w)
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

	icsData := GenerateICS(ev.UID, summary, description, location, dtstart, dtend, isAllDay)
	fields := VEventFields{
		UID: ev.UID, Summary: summary, Description: description, Location: location,
		DTStart: dtstart, DTEnd: dtend, IsAllDay: isAllDay,
	}

	UpsertEvent(db, calID, ev.UID, icsData, fields)
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

	uid := fmt.Sprintf("%d-%s", time.Now().UnixNano(), summary)
	icsData := GenerateICS(uid, summary, description, location, dtstart, dtend, isAllDay)
	fields := VEventFields{
		UID: uid, Summary: summary, Description: description, Location: location,
		DTStart: dtstart, DTEnd: dtend, IsAllDay: isAllDay,
	}

	UpsertEvent(db, calID, uid, icsData, fields)
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
