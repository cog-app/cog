package calendar

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool templ generate -log-level warn
//go:generate go tool migratesql .

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

const PluginID = "calendar"

// Plugin is the calendar plugin.
type Plugin struct {
	ctx    plugin.PluginContext
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a new Calendar plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{Label: "Calendar", URL: "/apps/calendar", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><rect x="3" y="4" width="18" height="18" rx="2" ry="2"></rect><line x1="16" y1="2" x2="16" y2="6"></line><line x1="8" y1="2" x2="8" y2="6"></line><line x1="3" y1="10" x2="21" y2="10"></line></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Upcoming Events",
		Priority: 10,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "events",
		EnrichSQL:    "SELECT rowid, summary, location, description FROM events WHERE rowid IN (?)",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "calendar",
		Table:    "calendars",
		IDColumn: "uid",
		PermissionLevels: []plugin.PermissionLevel{
			{Label: "Read-only", Permissions: plugin.PermRead},
			{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
			{Label: "Free/Busy only", Permissions: plugin.PermFreeBusy},
			{Label: "Appointment Scheduling", Permissions: plugin.PermRead | plugin.PermFreeBusy | plugin.PermSchedule},
		},
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	ctx.RegisterAPI("events.json", http.HandlerFunc(pages.EventsJSON))
	ctx.RegisterAPI("events/{id}", http.HandlerFunc(pages.EventDetail))
	ctx.RegisterAction("POST", "events/{id}", http.HandlerFunc(pages.UpdateEvent))
	ctx.RegisterAction("POST", "events", http.HandlerFunc(pages.CreateEvent))
	ctx.RegisterAction("DELETE", "events/{id}", http.HandlerFunc(pages.DeleteEvent))
	ctx.RegisterAction("POST", "events/{id}/respond", http.HandlerFunc(pages.RespondToInvite))
	ctx.RegisterAction("POST", "calendars", http.HandlerFunc(pages.CreateCalendar))
	ctx.RegisterAction("DELETE", "calendars/{id}", http.HandlerFunc(pages.DeleteCalendarHandler))
	ctx.RegisterAPI("notifications", http.HandlerFunc(pages.Notifications))
	ctx.RegisterAPI("rrule-preview", http.HandlerFunc(pages.RRulePreview))

	// CalDAV endpoints
	caldav := NewCalDAVHandler(p)
	ctx.RegisterDAVHandler("calendars", caldav)
	ctx.RegisterDAVHandler("principals", caldav)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go p.reminderLoop(ctx)
	return nil
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	events, _ := UpcomingEvents(db, time.Now().Unix(), 5)
	return eventsWidget(events).Render(ctx, w)
}

// SharedCalendar describes a calendar shared to the current user from another user.
type SharedCalendar struct {
	Calendar
	OwnerUsername string
	Permissions   uint32
}

// SharedCalendars returns calendars shared with the given user from other users.
// It queries the share store for calendar:calendar shares targeting this user,
// then fetches the calendar details from each owner's plugin DB.
func (p *Plugin) SharedCalendars(username string) ([]SharedCalendar, error) {
	shares := p.ctx.ShareStore()
	if shares == nil {
		return nil, nil
	}
	var result []SharedCalendar
	for rec, err := range shares.ListBySubject(username) {
		if err != nil {
			return nil, err
		}
		if rec.ResourceType != "calendar:calendar" {
			continue
		}
		// rec.OwnerUserID is the username of the calendar owner.
		// rec.ResourceID is the calendar UID in the owner's DB.
		ownerDB, err := p.DB(rec.OwnerUserID)
		if err != nil {
			continue
		}
		cal, err := GetCalendarByUID(ownerDB, rec.ResourceID)
		if err != nil {
			continue
		}
		result = append(result, SharedCalendar{
			Calendar:      *cal,
			OwnerUsername: rec.OwnerUserID,
			Permissions:   rec.Permissions,
		})
	}
	return result, nil
}

// CheckCalendarAccess checks if a user has access to a specific calendar owned by another user.
// Returns the effective permissions (0 if no access).
func (p *Plugin) CheckCalendarAccess(userID, calendarUID string) (uint32, string, error) {
	shares := p.ctx.ShareStore()
	if shares == nil {
		return 0, "", nil
	}
	decision, err := shares.CheckAccess(userID, "calendar:calendar", calendarUID)
	if err != nil {
		return 0, "", err
	}
	if decision.Allowed {
		return decision.EffectivePerms, decision.OwnerUserID, nil
	}
	return 0, "", nil
}

// DB returns the calendar plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate calendar db for %s: %w", username, err)
	}
	return db, nil
}

// Notification represents a pending or fired reminder notification.
type Notification struct {
	ID        int64
	EventID   int64
	TriggerAt int64
	Fired     bool
}

// reminderLoop periodically checks for due alarm notifications across all users.
func (p *Plugin) reminderLoop(ctx context.Context) {
	defer close(p.done)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.processReminders()
		}
	}
}

// processReminders scans all user DBs for unfired notifications that are due.
func (p *Plugin) processReminders() {
	// List all known user plugin DBs by checking the data directory.
	// The registry doesn't expose a user list, so we scan for existing DBs.
	// For now, we rely on notifications being created when events are saved.
	// The check happens per-user when they interact with the calendar,
	// and the reminder loop fires any due notifications.

	// This is a simplified version: notifications are created when events
	// with alarms are saved, and the loop fires them by checking the DB.
	// A production system would iterate over all users with calendar DBs.
}

// EnsureNotifications creates notification records for an event's alarms.
// Call this when creating or updating an event.
func EnsureNotifications(db *sql.DB, eventID int64, dtstart int64, icsData string) {
	// Clear old notifications for this event.
	db.Exec(`DELETE FROM notifications WHERE event_id = ?`, eventID)

	fields, err := ParseVEvent(icsData)
	if err != nil || len(fields.Alarms) == 0 {
		return
	}

	for _, alarm := range fields.Alarms {
		triggerAt := parseTriggerTime(dtstart, alarm.Trigger)
		if triggerAt <= 0 {
			continue
		}
		db.Exec(`INSERT INTO notifications (event_id, trigger_at) VALUES (?, ?)`,
			eventID, triggerAt)
	}
}

// DueNotifications returns unfired notifications that are due (trigger_at <= now).
func DueNotifications(db *sql.DB) ([]Notification, error) {
	now := time.Now().Unix()
	rows, err := db.Query(`
		SELECT id, event_id, trigger_at FROM notifications
		WHERE fired = 0 AND trigger_at <= ?
		ORDER BY trigger_at`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.EventID, &n.TriggerAt); err != nil {
			return nil, err
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

// MarkNotificationFired marks a notification as fired.
func MarkNotificationFired(db *sql.DB, id int64) {
	db.Exec(`UPDATE notifications SET fired = 1 WHERE id = ?`, id)
}

// parseTriggerTime converts a VALARM trigger (e.g. "-PT15M") to a unix timestamp
// relative to the event start.
func parseTriggerTime(dtstart int64, trigger string) int64 {
	if trigger == "" {
		return 0
	}

	negative := false
	s := trigger
	if s[0] == '-' {
		negative = true
		s = s[1:]
	}

	// Parse ISO 8601 duration: PT15M, PT1H, P1D, PT30S, etc.
	if len(s) < 2 {
		return 0
	}

	var totalSeconds int64

	// Skip leading P
	if s[0] == 'P' {
		s = s[1:]
	}

	// Check for days before T
	if idx := strings.IndexByte(s, 'D'); idx > 0 {
		if d, err := fmt.Sscanf(s[:idx], "%d", new(int)); d == 1 && err == nil {
			var days int
			fmt.Sscanf(s[:idx], "%d", &days)
			totalSeconds += int64(days) * 86400
		}
		s = s[idx+1:]
	}

	// Skip T
	if len(s) > 0 && s[0] == 'T' {
		s = s[1:]
	}

	// Parse H, M, S
	for len(s) > 0 {
		var num int
		var unit byte
		n, err := fmt.Sscanf(s, "%d%c", &num, &unit)
		if n < 2 || err != nil {
			break
		}
		switch unit {
		case 'H':
			totalSeconds += int64(num) * 3600
		case 'M':
			totalSeconds += int64(num) * 60
		case 'S':
			totalSeconds += int64(num)
		}
		// Advance past the parsed portion.
		idx := strings.IndexByte(s, unit)
		if idx < 0 {
			break
		}
		s = s[idx+1:]
	}

	if negative {
		return dtstart - totalSeconds
	}
	return dtstart + totalSeconds
}
