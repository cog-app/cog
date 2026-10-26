package calendar

import (
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

// LoadTimezone resolves an IANA zone name, returning nil for "" or an unknown
// name.
func LoadTimezone(name string) *time.Location { return tz.Load(name) }

// resolveTZID maps a TZID parameter to a location; see tz.Resolve.
func resolveTZID(tzid string, aliases map[string]string) *time.Location {
	return tz.Resolve(tzid, aliases)
}

// vtimezoneAliases collects TZID -> X-LIC-LOCATION pairs from the VTIMEZONE
// components of unfolded iCalendar lines.
func vtimezoneAliases(lines []string) map[string]string {
	var aliases map[string]string
	var inTZ bool
	var tzid string
	for _, line := range lines {
		switch {
		case line == "BEGIN:VTIMEZONE":
			inTZ, tzid = true, ""
		case line == "END:VTIMEZONE":
			inTZ = false
		case inTZ && strings.HasPrefix(line, "TZID:"):
			tzid = strings.TrimPrefix(line, "TZID:")
		case inTZ && tzid != "" && strings.HasPrefix(line, "X-LIC-LOCATION:"):
			if aliases == nil {
				aliases = make(map[string]string)
			}
			aliases[tzid] = strings.TrimPrefix(line, "X-LIC-LOCATION:")
		}
	}
	return aliases
}

// paramValue returns the value of a property parameter, e.g. TZID in
// "DTSTART;TZID=America/Denver". Names compare case-insensitively.
func paramValue(propFull, name string) string {
	for param := range strings.SplitSeq(propFull, ";") {
		k, v, ok := strings.Cut(param, "=")
		if ok && strings.EqualFold(k, name) {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// formatICSTime writes a DTSTART/DTEND-style property for t: UTC times as
// "NAME:...Z", other zones as "NAME;TZID=Zone:..." in local wall time so
// recurrences keep their local time across DST.
func formatICSTime(name string, t time.Time) string {
	if t.Location() == time.UTC || t.Location().String() == "Local" {
		return name + ":" + t.UTC().Format("20060102T150405Z")
	}
	return name + ";TZID=" + t.Location().String() + ":" + t.Format("20060102T150405")
}

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
// DTSTART names, or nil for UTC times, which the form shows in the browser's
// zone instead.
func eventEditZone(ev *Event, cal *Calendar) *time.Location {
	floating := time.UTC
	if cal != nil {
		floating = cal.Zone()
	}
	fields, err := ParseVEventIn(ev.ICSData, floating)
	if err != nil || fields.DTStart.IsZero() || fields.DTStart.Location() == time.UTC {
		return nil
	}
	return fields.DTStart.Location()
}
