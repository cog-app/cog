package calendar

import (
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
	"git.duckfam.us/jj-software/cog/internal/dav/itip"
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

// Settings shows calendar management: calendars, their feeds, the
// subscriptions and the birthday calendar.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	calendars, err := h.plugin.Calendars(u.ID)
	if err != nil {
		h.plugin.log.Error("calendar settings", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	v, err := h.plugin.settingsView(u.ID, calendars)
	if err != nil {
		h.plugin.log.Error("calendar settings", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Calendar Settings")
	if err := calSettingsPage(v, plugin.CSRFToken(r)).Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render calendar settings", "err", err)
	}
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
			Occurrence  int64  `json:"occurrence,omitempty"`
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
		fc.ExtendedProps.Occurrence = ev.Occurrence
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

// errReadOnly is an edit of an event in a calendar Cog fills.
var errReadOnly = errors.New("this calendar is read-only")

// ownWritable is ownEvent for a change: an event of a read-only calendar
// is refused.
func (h *Pages) ownWritable(r *http.Request) (*sql.DB, string, *Event, *Calendar, int, error) {
	db, userID, ev, cal, status, err := h.ownEvent(r)
	if err == nil && cal.ReadOnly {
		return nil, "", nil, nil, http.StatusForbidden, errReadOnly
	}
	return db, userID, ev, cal, status, err
}

// Attendee is an attendee as the event modal shows one.
type Attendee struct {
	Email       string
	DisplayName string
	Status      string // NEEDS-ACTION, ACCEPTED, DECLINED, TENTATIVE
	Role        string // REQ-PARTICIPANT, OPT-PARTICIPANT, CHAIR
}

// attendeesOf returns a component's attendees that have an email address;
// the form cannot show others, and saving keeps them.
func attendeesOf(e *ical.Component) []Attendee {
	var out []Attendee
	for _, a := range e.Attendees {
		if email := ical.Email(a.Address); email != "" {
			out = append(out, Attendee{Email: email, DisplayName: a.CN, Status: a.PartStat, Role: a.Role})
		}
	}
	return out
}

// myStatus returns the user's PARTSTAT on an event someone else organizes
// ("NEEDS-ACTION" when they have not answered), or "" when it is not theirs
// to answer.
func myStatus(e *ical.Component, me *dav.User) string {
	mine := dav.Addresses(me)
	if e.Organizer == nil || slices.Contains(mine, itip.Normalize(e.Organizer.Address)) {
		return ""
	}
	for _, a := range e.Attendees {
		if slices.Contains(mine, itip.Normalize(a.Address)) {
			if a.PartStat == "" {
				return "NEEDS-ACTION"
			}
			return strings.ToUpper(a.PartStat)
		}
	}
	return ""
}

// errBadOccurrence is a malformed occurrence parameter.
var errBadOccurrence = errors.New("invalid occurrence")

// occurrenceOf returns the instance of a recurring event that the request's
// occurrence parameter (the Unix start of a master occurrence) names, or nil
// when the request is about the event as a whole. An event without a
// recurring master has no occurrences to choose.
func occurrenceOf(r *http.Request, ev *Event, z ical.Zones) (*ical.Instance, error) {
	v := r.FormValue("occurrence")
	if v == "" || v == "0" {
		return nil, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil, errBadOccurrence
	}
	if m := ev.Object.Master(); m == nil || !m.Recurs() {
		return nil, nil
	}
	in, err := ev.Object.InstanceAt(time.Unix(n, 0), z)
	if err != nil {
		return nil, err
	}
	return &in, nil
}

// occurrenceStatus maps an occurrenceOf error to a status code.
func occurrenceStatus(err error) int {
	if errors.Is(err, ical.ErrNoOccurrence) {
		return http.StatusNotFound
	}
	if errors.Is(err, errBadOccurrence) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// EventDetail returns an event detail partial for the modal: the event, or
// with ?occurrence= one instance of a series, with a choice of what an edit
// applies to.
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
	in, err := occurrenceOf(r, ev, ical.ZonesFor(ev.Object, cal.Zone()))
	if err != nil {
		http.Error(w, err.Error(), occurrenceStatus(err))
		return
	}
	view, comp := *ev, ev.Object.Components[0]
	if in != nil {
		comp = in.Event
		view.Summary, view.Location, view.Description = comp.Summary, comp.Location, comp.Description
		view.DTStart, view.DTEnd, view.IsAllDay = in.Start.Unix(), in.End.Unix(), comp.Start.IsDate
		view.Occurrence = in.RecurrenceID.Unix()
	}
	me, err := h.plugin.me(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	compAt := max(slices.Index(ev.Object.Components, comp),
		// An occurrence without an override shows the master's attachments.
		0)
	atts := h.attachmentsOf(ev, compAt, me.Username)
	if err := calEventModal(plugin.CSRFToken(r), &view, cal, calendars, attendeesOf(comp), myStatus(comp, me), atts).Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render event", "id", ev.ID, "err", err)
	}
}

// eventForm is what the event form edits.
type eventForm struct {
	summary, location, description string
	allDay                         bool
	start, end                     time.Time
	rrule                          string
	attendees                      []ical.Attendee // mailto attendees only
	organizer                      *dav.User       // who becomes ORGANIZER when inviting
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

// movedTo returns the form with its times moved to start, keeping their
// length.
func (f *eventForm) movedTo(start time.Time) *eventForm {
	g := *f
	g.start = start
	g.end = start.Add(f.end.Sub(f.start))
	return &g
}

// UpdateEvent handles POST to update an existing event. Only what the form
// shows changes; everything else in the event, including its overrides,
// exceptions, alarms and whatever a client stored that Cog does not model,
// is kept.
//
// For one instance of a series (occurrence), scope says what the edit
// applies to: "this" occurrence (an override), "following" ones (the series
// is split in two at it), or "all" (the master, moved by as much as the
// instance was).
func (h *Pages) UpdateEvent(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, oldCal, status, err := h.ownWritable(r)
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
	if cal.ReadOnly {
		http.Error(w, errReadOnly.Error(), http.StatusForbidden)
		return
	}
	f, err := parseEventForm(cal, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	o := ev.Object
	before := o.Clone()
	z := ical.ZonesFor(o, oldCal.Zone())
	in, err := occurrenceOf(r, ev, z)
	if err != nil {
		http.Error(w, err.Error(), occurrenceStatus(err))
		return
	}
	me, err := h.plugin.me(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f.organizer = me

	now := h.plugin.ctx.Now()
	color := r.FormValue("color")
	saves := []Save{{Cal: cal, From: ev, Object: o}}
	edited := 0 // which save the form's colour and invitations belong to
	scope := r.FormValue("scope")
	switch {
	case in == nil:
		applyForm(o, o.Components[0], f, z, now, true)

	case scope == "this":
		if cal.ID != ev.CalendarID {
			http.Error(w, "Only a whole series can move to another calendar.", http.StatusBadRequest)
			return
		}
		e, err := o.Override(in.RecurrenceID, z)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		applyForm(o, e, f, z, now, false)
		// The series' own colour is not the occurrence's to change.
		color = ev.Color

	case scope == "following" && in.RecurrenceID.After(seriesStart(o, z)):
		rule := o.Master().RRule
		n, err := o.Split(in.RecurrenceID, z, uuid.NewString())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		o.Master().Stamp(now, true)
		delta := f.start.Sub(in.Start)
		g := f.movedTo(in.RecurrenceID.Add(delta))
		if sameRule(rule, f.rrule) {
			// The form shows the whole series' rule; unchanged, the new
			// series keeps what is left of it (a COUNT less the
			// occurrences before the split).
			g.rrule = n.Master().RRule
		}
		applyForm(n, n.Components[0], g, z, now, true)
		n.ShiftExceptions(delta, z)
		saves[0].Cal = oldCal
		saves = append(saves, Save{Cal: cal, Object: n})
		edited = 1

	default: // "all", or "following" from the first occurrence
		m := o.Master()
		delta := f.start.Sub(in.Start)
		start, _, err := m.Span(z)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		applyForm(o, m, f.movedTo(start.Add(delta)), z, now, true)
		o.ShiftExceptions(delta, z)
	}

	// The first save is the event as it was; a split adds a new series.
	olds := []*ical.Object{before, nil}
	var deliveries []*dav.Delivery
	for i, sv := range saves {
		d, err := h.plugin.plan(userID, olds[i], sv.Object)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		deliveries = append(deliveries, d)
	}
	saved, err := SaveEvents(db, now, saves...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := SetEventColor(db, saved[edited].ID, color); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, d := range deliveries {
		d.Send()
	}

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// seriesStart returns when a series' master starts.
func seriesStart(o *ical.Object, z ical.Zones) time.Time {
	if m := o.Master(); m != nil {
		if t, _, err := m.Span(z); err == nil {
			return t
		}
	}
	return time.Time{}
}

// applyForm writes a submitted form into component e of o, with the
// bookkeeping of an organizer's edit (stamp). Vendor copies of a changed
// field are dropped. rule says whether the form's recurrence applies: it
// does not to one occurrence.
func applyForm(o *ical.Object, e *ical.Component, f *eventForm, z ical.Zones, now time.Time, rule bool) {
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

	if rule && !sameRule(e.RRule, f.rrule) {
		e.RRule = f.rrule
		e.Changed("TIME")
		scheduling = true
		if f.rrule == "" && len(e.RDates) == 0 && !e.IsOverride() {
			// No longer recurring: its exceptions describe nothing.
			e.ExDates = nil
			o.Components = []*ical.Component{e}
		}
	}

	if mergeAttendees(e, f.attendees) {
		scheduling = true
	}
	if e.Organizer == nil && len(e.Attendees) > 0 && f.organizer != nil {
		// Inviting someone makes the user the organizer (RFC 5545 requires
		// one with attendees), so the invitation can be scheduled.
		e.Organizer = organizerOf(f.organizer)
	}
	e.Stamp(now, scheduling)
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
func mergeAttendees(e *ical.Component, form []ical.Attendee) bool {
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
	if cal.ReadOnly {
		http.Error(w, errReadOnly.Error(), http.StatusForbidden)
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
	var organizer *ical.Organizer
	if len(f.attendees) > 0 {
		me, err := h.plugin.me(userID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		organizer = organizerOf(me)
	}
	o := &ical.Object{
		UID:   uuid.NewString(),
		Props: []contentline.Property{{Name: "PRODID", Value: ical.ProdID}},
		Components: []*ical.Component{{
			Kind:    ical.VEVENT,
			Summary: f.summary, Description: f.description, Location: f.location,
			Start: ical.DateTimeOf(f.start, f.allDay), End: ical.DateTimeOf(f.end, f.allDay),
			RRule: f.rrule, Organizer: organizer, Attendees: f.attendees, Sequence: &seq,
			DTStamp: stamp, Created: stamp, LastModified: stamp,
		}},
	}
	d, err := h.plugin.plan(userID, nil, o)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
	d.Send()

	http.Redirect(w, r, "/apps/calendar/", http.StatusSeeOther)
}

// DeleteEvent handles DELETE to remove an event (HTMX). For one instance
// of a series, scope says what goes: "this" occurrence (an EXDATE), the
// "following" ones (the series ends before it), or "all" of it.
func (h *Pages) DeleteEvent(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, cal, status, err := h.ownWritable(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	o := ev.Object
	before := o.Clone()
	z := ical.ZonesFor(o, cal.Zone())
	in, err := occurrenceOf(r, ev, z)
	if err != nil {
		http.Error(w, err.Error(), occurrenceStatus(err))
		return
	}
	now := h.plugin.ctx.Now()

	remains := false
	switch scope := r.FormValue("scope"); {
	case in == nil || scope == "all":
	case scope == "following":
		if remains, err = o.Truncate(in.RecurrenceID, z); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		remains = o.Exclude(in.RecurrenceID, z)
	}

	if !remains {
		d, err := h.plugin.plan(userID, before, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := DeleteEventByID(db, ev.ID, now); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h.plugin.collected(plugin.UserFromContext(r.Context()).Username)
		d.Send()
		w.WriteHeader(http.StatusOK)
		return
	}
	o.Components[0].Stamp(now, true)
	d, err := h.plugin.plan(userID, before, o)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := SaveEvent(db, cal, ev, o, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The occurrence's override may have carried attachments.
	h.plugin.collected(plugin.UserFromContext(r.Context()).Username)
	d.Send()
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
	if errors.Is(err, dav.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.failed(w, r, "update the calendar", err)
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
		back(w, r, plugin.Flash{Kind: "error", Message: "Unknown timezone " + tz + "."})
		return
	}
	if err := h.plugin.UpdateCalendar(u.Username, u.ID, id, name, color, tz); err != nil {
		h.failed(w, r, "update the calendar", err)
		return
	}
	back(w, r, plugin.Flash{Kind: "success", Message: "Saved " + name + "."})
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

// RespondToInvite handles POST to accept, decline or tentatively accept an
// invitation in the user's calendar: their PARTSTAT changes on every
// occurrence, and the organizer gets the REPLY.
func (h *Pages) RespondToInvite(w http.ResponseWriter, r *http.Request) {
	db, userID, ev, cal, status, err := h.ownWritable(r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	answer := r.FormValue("status")
	if answer != "ACCEPTED" && answer != "DECLINED" && answer != "TENTATIVE" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	me, err := h.plugin.me(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mine := dav.Addresses(me)
	before := ev.Object.Clone()
	found := false
	for _, e := range ev.Object.Components {
		for i := range e.Attendees {
			if slices.Contains(mine, itip.Normalize(e.Attendees[i].Address)) {
				e.Attendees[i].PartStat = answer
				found = true
			}
		}
	}
	if !found {
		http.Error(w, "not an attendee", http.StatusNotFound)
		return
	}
	now := h.plugin.ctx.Now()
	for _, e := range ev.Object.Components {
		e.Stamp(now, false)
	}
	d, err := h.plugin.plan(userID, before, ev.Object)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := SaveEvent(db, cal, ev, ev.Object, now); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.Send()
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

	// The events in one query, not one per reminder.
	ids := make([]int64, len(notifications))
	for i, n := range notifications {
		ids[i] = n.EventID
	}
	events, err := EventsByID(db, ids)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result := []notifJSON{}
	for _, n := range notifications {
		ev, ok := events[n.EventID]
		if !ok {
			continue // deleted since; its reminders went with it
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
