package calendar

import (
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// LoadTimezone resolves an IANA zone name, returning nil for "" or an unknown
// name.
func LoadTimezone(name string) *time.Location { return tz.Load(name) }

// commonTimezones seeds the timezone picker; any IANA name is accepted.
var commonTimezones = []string{
	"UTC",
	"Pacific/Honolulu", "America/Anchorage", "America/Los_Angeles", "America/Phoenix",
	"America/Denver", "America/Chicago", "America/New_York", "America/Halifax",
	"America/Sao_Paulo", "Europe/London", "Europe/Berlin", "Europe/Paris",
	"Europe/Helsinki", "Asia/Kolkata", "Asia/Shanghai", "Asia/Tokyo",
	"Australia/Sydney", "Pacific/Auckland",
}

// eventEditZone returns the zone an event's times are edited in: the zone its
// DTSTART names, the calendar's for a floating time, or nil for UTC times,
// which the form shows in the browser's zone instead.
func eventEditZone(ev *Event, cal *Calendar) *time.Location {
	if ev.Object == nil || len(ev.Object.Events) == 0 {
		return nil
	}
	start := ev.Object.Events[0].Start
	if start.IsZero() || start.IsDate || start.TZID == "UTC" {
		return nil
	}
	floating := time.UTC
	if cal != nil {
		floating = cal.Zone()
	}
	return zonesFor(ev.Object, floating).Location(start.TZID)
}
