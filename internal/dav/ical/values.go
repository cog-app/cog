// Package ical maps iCalendar components (RFC 5545) between the generic
// contentline tree and Cog's models: what Cog understands becomes typed
// fields, and everything else is kept as leftovers and written back in its
// original order. It holds no SQL; plugins store the models.
package ical

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// UnescapeText decodes a TEXT value (RFC 5545 §3.3.11). A backslash before
// anything else is kept, as lenient readers do.
func UnescapeText(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		switch s[i+1] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ',', ';':
			b.WriteByte(s[i+1])
		default:
			b.WriteByte(c)
			continue
		}
		i++
	}
	return b.String()
}

// EscapeText encodes a TEXT value.
func EscapeText(s string) string {
	if !strings.ContainsAny(s, "\\;,\n\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\', ';', ',':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			b.WriteString(`\n`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// splitText splits a multi-valued TEXT property (CATEGORIES) on unescaped
// commas and unescapes each value.
func splitText(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case ',':
			out = append(out, UnescapeText(s[start:i]))
			start = i + 1
		}
	}
	return append(out, UnescapeText(s[start:]))
}

const (
	layoutDateTime = "20060102T150405"
	layoutDate     = "20060102"
)

// DateTime is a DATE or DATE-TIME value as written: wall time and the zone it
// is in, never only an instant, because recurrence and DST depend on the
// wall time.
type DateTime struct {
	Local  string `json:"v"`            // 20261025T090000, or 20261025 for a DATE
	TZID   string `json:"tz,omitempty"` // "" floating, "UTC", an IANA name, or a client's own TZID
	IsDate bool   `json:"d,omitempty"`
}

// IsZero reports whether the value is absent.
func (d DateTime) IsZero() bool { return d.Local == "" }

// Zones turns stored TZIDs into locations.
type Zones struct {
	// Floating is the zone of floating times and of TZIDs Cog cannot read:
	// the collection's zone.
	Floating *time.Location
	// Custom holds the locations of clients' own VTIMEZONEs, by TZID.
	Custom map[string]*time.Location
	// Load resolves an IANA name; nil for one it does not know.
	Load func(string) *time.Location
}

// Location returns the location a TZID names.
func (z Zones) Location(tzid string) *time.Location {
	switch tzid {
	case "":
		return z.floating()
	case "UTC":
		return time.UTC
	}
	if loc, ok := z.Custom[tzid]; ok {
		return loc
	}
	if z.Load != nil {
		if loc := z.Load(tzid); loc != nil {
			return loc
		}
	}
	return z.floating()
}

func (z Zones) floating() *time.Location {
	if z.Floating == nil {
		return time.UTC
	}
	return z.Floating
}

// Time returns the value in its zone. A DATE is midnight UTC on that date,
// which is how Cog stores and shows all-day values.
func (d DateTime) Time(z Zones) (time.Time, error) {
	if d.IsDate {
		return time.Parse(layoutDate, d.Local)
	}
	return time.ParseInLocation(layoutDateTime, d.Local, z.Location(d.TZID))
}

// Year returns the value's year, or 0 when it is unreadable.
func (d DateTime) Year() int {
	if len(d.Local) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(d.Local[:4])
	return y
}

// DateTimeOf returns the value of t as written in loc: UTC times with TZID
// "UTC", others with the location's name, or floating when tzid is "".
func DateTimeOf(t time.Time, allDay bool) DateTime {
	if allDay {
		return DateTime{Local: t.Format(layoutDate), IsDate: true}
	}
	if t.Location() == time.UTC {
		return DateTime{Local: t.Format(layoutDateTime), TZID: "UTC"}
	}
	return DateTime{Local: t.Format(layoutDateTime), TZID: t.Location().String()}
}

// value writes the value text, with the Z of a UTC time.
func (d DateTime) value() string {
	if d.TZID == "UTC" {
		return d.Local + "Z"
	}
	return d.Local
}

// params returns VALUE and TZID as the value needs them.
func (d DateTime) params() []contentline.Param {
	switch {
	case d.IsDate:
		return []contentline.Param{{Name: "VALUE", Values: []string{"DATE"}}}
	case d.TZID != "" && d.TZID != "UTC":
		return []contentline.Param{{Name: "TZID", Values: []string{d.TZID}}}
	}
	return nil
}

// sameKind reports whether two values can share one multi-valued property.
func (d DateTime) sameKind(o DateTime) bool { return d.TZID == o.TZID && d.IsDate == o.IsDate }

var errBadDateTime = errors.New("bad DATE or DATE-TIME value")

// parseValue reads one value of a property with the VALUE and TZID it was
// written with; tzid maps a TZID parameter to the name Cog stores.
func parseValue(v, valueParam, tzidParam string, tzid func(string) string) (DateTime, error) {
	v = strings.TrimSpace(v)
	switch {
	case strings.EqualFold(valueParam, "DATE") || (valueParam == "" && len(v) == 8):
		if _, err := time.Parse(layoutDate, v); err != nil {
			return DateTime{}, errBadDateTime
		}
		return DateTime{Local: v, IsDate: true}, nil
	case strings.HasSuffix(v, "Z"):
		v = strings.TrimSuffix(v, "Z")
		if _, err := time.Parse(layoutDateTime, v); err != nil {
			return DateTime{}, errBadDateTime
		}
		return DateTime{Local: v, TZID: "UTC"}, nil
	}
	if _, err := time.Parse(layoutDateTime, v); err != nil {
		return DateTime{}, errBadDateTime
	}
	d := DateTime{Local: v}
	if tzidParam != "" {
		d.TZID = tzid(tzidParam)
	}
	return d, nil
}

// Duration is an RFC 5545 duration. Weeks and days are nominal: a day is a
// calendar day in the value's zone, not 24 hours.
type Duration struct {
	Negative bool
	Days     int // weeks included
	Clock    time.Duration
}

// ParseDuration reads a dur-value such as -PT15M, P1D or P1W.
func ParseDuration(s string) (Duration, error) {
	var d Duration
	orig := s
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "-"):
		d.Negative = true
		s = s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") || len(s) < 3 {
		return d, fmt.Errorf("bad duration %q", orig)
	}
	s = s[1:]
	inTime := false
	for s != "" {
		if s[0] == 'T' {
			inTime = true
			s = s[1:]
			continue
		}
		i := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == 0 || i == len(s) {
			return d, fmt.Errorf("bad duration %q", orig)
		}
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return d, fmt.Errorf("bad duration %q", orig)
		}
		switch unit := s[i]; {
		case unit == 'W' && !inTime:
			d.Days += 7 * n
		case unit == 'D' && !inTime:
			d.Days += n
		case unit == 'H' && inTime:
			d.Clock += time.Duration(n) * time.Hour
		case unit == 'M' && inTime:
			d.Clock += time.Duration(n) * time.Minute
		case unit == 'S' && inTime:
			d.Clock += time.Duration(n) * time.Second
		default:
			return d, fmt.Errorf("bad duration %q", orig)
		}
		s = s[i+1:]
	}
	return d, nil
}

// Add returns t moved by the duration.
func (d Duration) Add(t time.Time) time.Time {
	if d.Negative {
		return t.AddDate(0, 0, -d.Days).Add(-d.Clock)
	}
	return t.AddDate(0, 0, d.Days).Add(d.Clock)
}

// String writes the duration in its shortest form.
func (d Duration) String() string {
	var b strings.Builder
	if d.Negative {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	if d.Days%7 == 0 && d.Days > 0 && d.Clock == 0 {
		fmt.Fprintf(&b, "%dW", d.Days/7)
		return b.String()
	}
	if d.Days > 0 {
		fmt.Fprintf(&b, "%dD", d.Days)
	}
	if d.Clock > 0 || d.Days == 0 {
		b.WriteByte('T')
		c := d.Clock
		h, m, s := c/time.Hour, c%time.Hour/time.Minute, c%time.Minute/time.Second
		if h > 0 {
			fmt.Fprintf(&b, "%dH", h)
		}
		if m > 0 {
			fmt.Fprintf(&b, "%dM", m)
		}
		if s > 0 || (h == 0 && m == 0) {
			fmt.Fprintf(&b, "%dS", s)
		}
	}
	return b.String()
}
