// Package tz resolves the time zone names clients put in iCalendar TZID
// parameters and CalDAV calendar-timezone properties to IANA locations.
package tz

import (
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// cache memoizes Load; time.LoadLocation reads zoneinfo on every call.
var cache sync.Map // string -> *time.Location (nil when unresolvable)

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

// Load resolves an IANA zone name, returning nil for "" or an unknown name.
// Unlike time.LoadLocation it rejects "Local", which would mean the server's
// zone.
func Load(name string) *time.Location {
	if name == "" || name == "Local" {
		return nil
	}
	if v, ok := cache.Load(name); ok {
		return v.(*time.Location)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	cache.Store(name, loc)
	return loc
}

// Resolve maps a TZID parameter to a location. It handles IANA names,
// VTIMEZONE X-LIC-LOCATION aliases (Thunderbird), path-prefixed IDs such as
// "/mozilla.org/20050126_1/America/Denver", and common Windows names.
func Resolve(tzid string, aliases map[string]string) *time.Location {
	tzid = strings.Trim(tzid, `"`)
	if alias, ok := aliases[tzid]; ok {
		if loc := Load(alias); loc != nil {
			return loc
		}
	}
	if loc := Load(tzid); loc != nil {
		return loc
	}
	if iana, ok := windowsZones[tzid]; ok {
		return Load(iana)
	}
	// Try each suffix after a slash: "/vendor/version/America/Denver" ->
	// "version/America/Denver" -> "America/Denver".
	for i := 0; i < len(tzid); i++ {
		if tzid[i] == '/' {
			if loc := Load(tzid[i+1:]); loc != nil {
				return loc
			}
		}
	}
	return nil
}

// FromVTimezone returns the location of the first VTIMEZONE in comps (a
// parsed VCALENDAR, as sent in CALDAV:calendar-timezone) that resolves, or
// nil.
func FromVTimezone(comps []*contentline.Component) *time.Location {
	for _, cal := range comps {
		for _, vtz := range cal.Components {
			if !vtz.Is("VTIMEZONE") {
				continue
			}
			id := vtz.Prop("TZID")
			if id == nil {
				continue
			}
			var aliases map[string]string
			if loc := vtz.Prop("X-LIC-LOCATION"); loc != nil {
				aliases = map[string]string{id.Value: loc.Value}
			}
			if loc := Resolve(id.Value, aliases); loc != nil {
				return loc
			}
		}
	}
	return nil
}
