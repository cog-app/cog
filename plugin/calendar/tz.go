package calendar

import (
	"strings"
	"sync"
	"time"
)

// tzCache memoizes resolveName; time.LoadLocation reads zoneinfo on every call.
var tzCache sync.Map // string -> *time.Location (nil when unresolvable)

// windowsZones maps the Windows zone names Outlook and Exchange put in TZID to
// IANA names. Only the common ones; anything else falls back to the
// calendar's zone.
var windowsZones = map[string]string{
	"UTC":                          "UTC",
	"Coordinated Universal Time":   "UTC",
	"Hawaiian Standard Time":       "Pacific/Honolulu",
	"Alaskan Standard Time":        "America/Anchorage",
	"Pacific Standard Time":        "America/Los_Angeles",
	"US Mountain Standard Time":    "America/Phoenix",
	"Mountain Standard Time":       "America/Denver",
	"Central Standard Time":        "America/Chicago",
	"Eastern Standard Time":        "America/New_York",
	"Atlantic Standard Time":       "America/Halifax",
	"GMT Standard Time":            "Europe/London",
	"W. Europe Standard Time":      "Europe/Berlin",
	"Romance Standard Time":        "Europe/Paris",
	"Central Europe Standard Time": "Europe/Budapest",
	"E. Europe Standard Time":      "Europe/Chisinau",
	"Tokyo Standard Time":          "Asia/Tokyo",
	"China Standard Time":          "Asia/Shanghai",
	"India Standard Time":          "Asia/Kolkata",
	"AUS Eastern Standard Time":    "Australia/Sydney",
}

// LoadTimezone resolves an IANA zone name, returning nil for "" or an unknown
// name. Unlike time.LoadLocation it rejects "Local", which would mean the
// server's zone.
func LoadTimezone(name string) *time.Location {
	if name == "" || name == "Local" {
		return nil
	}
	if v, ok := tzCache.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	tzCache.Store(name, loc)
	return loc
}

// resolveTZID maps a TZID parameter to a location. It handles IANA names,
// VTIMEZONE X-LIC-LOCATION aliases (Thunderbird), path-prefixed IDs such as
// "/mozilla.org/20050126_1/America/Denver", and common Windows names.
func resolveTZID(tzid string, aliases map[string]string) *time.Location {
	tzid = strings.Trim(tzid, `"`)
	if alias, ok := aliases[tzid]; ok {
		if loc := LoadTimezone(alias); loc != nil {
			return loc
		}
	}
	if loc := LoadTimezone(tzid); loc != nil {
		return loc
	}
	if iana, ok := windowsZones[tzid]; ok {
		return LoadTimezone(iana)
	}
	// Try each suffix after a slash: "/vendor/version/America/Denver" ->
	// "version/America/Denver" -> "America/Denver".
	for i := 0; i < len(tzid); i++ {
		if tzid[i] == '/' {
			if loc := LoadTimezone(tzid[i+1:]); loc != nil {
				return loc
			}
		}
	}
	return nil
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
