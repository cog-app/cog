package calendar

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/dav"
)

// userID returns the ID of a user by username. Collections are keyed by user
// ID; plugin databases by username.
func (p *Plugin) userID(username string) (string, error) {
	users := p.ctx.UserLookup()
	if users == nil {
		return "", errors.New("calendar: no user lookup service")
	}
	u, err := users.GetByUsername(username)
	if err != nil {
		return "", fmt.Errorf("look up user %s: %w", username, err)
	}
	return u.ID, nil
}

func (p *Plugin) username(userID string) (string, error) {
	users := p.ctx.UserLookup()
	if users == nil {
		return "", errors.New("calendar: no user lookup service")
	}
	u, err := users.GetByID(userID)
	if err != nil {
		return "", fmt.Errorf("look up user %s: %w", userID, err)
	}
	return u.Username, nil
}

// Calendars returns a user's collections that hold events, creating the
// default one when the user has no calendar collections at all.
func (p *Plugin) Calendars(userID string) ([]Calendar, error) {
	cols, err := p.dav.Collections.EnsureDefault(userID, dav.HomeCalendars)
	if err != nil {
		return nil, err
	}
	var cals []Calendar
	for i := range cols {
		if cols[i].Has(dav.VEVENT) {
			cals = append(cals, calendarFrom(&cols[i]))
		}
	}
	return cals, nil
}

// calendarMap returns a user's calendars by ID.
func (p *Plugin) calendarMap(userID string) (map[int64]Calendar, error) {
	cals, err := p.Calendars(userID)
	if err != nil {
		return nil, err
	}
	m := make(map[int64]Calendar, len(cals))
	for _, c := range cals {
		m[c.ID] = c
	}
	return m, nil
}

// Calendar returns one of a user's own calendars, or dav.ErrNotFound.
func (p *Plugin) Calendar(userID string, id int64) (*Calendar, error) {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return nil, err
	}
	if c.OwnerID != userID || !c.Has(dav.VEVENT) {
		return nil, dav.ErrNotFound
	}
	cal := calendarFrom(c)
	return &cal, nil
}

// CalendarByName returns a user's calendar by its path name.
func (p *Plugin) CalendarByName(userID, name string) (*Calendar, error) {
	c, err := p.dav.Collections.Get(userID, dav.HomeCalendars, name)
	if err != nil {
		return nil, err
	}
	if !c.Has(dav.VEVENT) {
		return nil, dav.ErrNotFound
	}
	cal := calendarFrom(c)
	return &cal, nil
}

// CreateCalendar creates an events calendar. An empty name is made from the
// display name, unique among the user's calendars.
func (p *Plugin) CreateCalendar(userID, name, displayName, color, description, timezone string) (*Calendar, error) {
	create := p.dav.Collections.Create
	if name == "" {
		name = dav.Slug(displayName, "calendar")
		create = p.dav.Collections.CreateNamed
	}
	if color == "" {
		color = "#0082c9"
	}
	c := &dav.Collection{
		OwnerID: userID, Home: dav.HomeCalendars, Name: name, Components: []dav.Component{dav.VEVENT},
		DisplayName: displayName, Description: description, Color: color, Timezone: timezone,
	}
	if err := create(c); err != nil {
		return nil, err
	}
	cal := calendarFrom(c)
	return &cal, nil
}

// UpdateCalendar renames, recolors or rezones one of a user's calendars. A
// new zone recomputes the indexed times of floating events.
func (p *Plugin) UpdateCalendar(username, userID string, id int64, displayName, color, timezone string) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID {
		return dav.ErrNotFound
	}
	rezoned := c.Timezone != timezone
	c.DisplayName, c.Color, c.Timezone = displayName, color, timezone
	if err := p.dav.Collections.Update(c); err != nil {
		return err
	}
	if !rezoned {
		return nil
	}
	cal := calendarFrom(c)
	db, err := p.DB(username)
	if err != nil {
		return err
	}
	return RecomputeEventTimes(db, cal.ID, cal.Zone(), p.ctx.Now())
}

// DeleteCalendar deletes one of a user's calendars, with everything in it:
// a collection holding tasks as well loses those too.
func (p *Plugin) DeleteCalendar(username, userID string, id int64) error {
	c, err := p.dav.Collections.ByID(dav.CollectionID(id))
	if err != nil {
		return err
	}
	if c.OwnerID != userID {
		return dav.ErrNotFound
	}
	return p.dav.DeleteCollection(username, c)
}

// SharedCalendar describes a calendar shared to the current user from another user.
type SharedCalendar struct {
	Calendar
	OwnerUsername string
	Permissions   uint32
}

// SharedCalendars returns the event calendars shared with a user.
func (p *Plugin) SharedCalendars(userID string) ([]SharedCalendar, error) {
	shared, err := p.dav.Collections.Shared(p.ctx.ShareStore(), p.username, userID, dav.VEVENT)
	if err != nil {
		return nil, err
	}
	out := make([]SharedCalendar, len(shared))
	for i, sc := range shared {
		out[i] = SharedCalendar{Calendar: calendarFrom(&sc.Collection), OwnerUsername: sc.OwnerUsername, Permissions: sc.Permissions}
	}
	return out, nil
}

// writable returns the calendars events may be created in or moved to.
func writable(cals []Calendar) []Calendar {
	var out []Calendar
	for _, c := range cals {
		if !c.ReadOnly {
			out = append(out, c)
		}
	}
	return out
}

func readOnly(cal *Calendar) bool { return cal != nil && cal.ReadOnly }

// readOnlyReason says why an event cannot be edited.
func readOnlyReason(cal *Calendar) string {
	switch cal.Kind {
	case dav.KindSubscription:
		return "This event comes from a subscribed calendar and cannot be edited here."
	case dav.KindBirthdays:
		return "This event comes from a contact's birthday or anniversary: edit the contact to change it."
	}
	return "This calendar is read-only."
}
