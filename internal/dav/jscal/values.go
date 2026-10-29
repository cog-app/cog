package jscal

import (
	"errors"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
)

const (
	layoutLocal   = "2006-01-02T15:04:05"
	layoutUTC     = "2006-01-02T15:04:05Z"
	layoutICal    = "20060102T150405"
	layoutICalDay = "20060102"
)

var errBadTime = errors.New("bad date-time")

// localOf writes a value's wall time as a LocalDateTime: a DATE is its
// midnight.
func localOf(d ical.DateTime) string {
	if d.IsDate {
		t, err := time.Parse(layoutICalDay, d.Local)
		if err != nil {
			return ""
		}
		return t.Format(layoutLocal)
	}
	t, err := time.Parse(layoutICal, d.Local)
	if err != nil {
		return ""
	}
	return t.Format(layoutLocal)
}

// parseLocal reads a LocalDateTime as iCalendar wall time, or a DATE when
// date is set and the time is midnight.
func parseLocal(s string, date bool) (string, bool, error) {
	t, err := time.Parse(layoutLocal, strings.TrimSpace(s))
	if err != nil {
		return "", false, errBadTime
	}
	if date && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format(layoutICalDay), true, nil
	}
	return t.Format(layoutICal), false, nil
}

// zoneOut writes a TZID as a JSCalendar time zone: an IANA name as itself,
// UTC as Etc/UTC, floating as none, and a zone of the client's own (or one
// Cog cannot read) as a custom zone, "/" and its TZID.
func zoneOut(tzid string) string {
	switch {
	case tzid == "":
		return ""
	case tzid == "UTC":
		return "Etc/UTC"
	case tz.Load(tzid) != nil:
		return tzid
	}
	return "/" + tzid
}

// zoneIn reads a JSCalendar time zone as a TZID.
func zoneIn(s string) string {
	switch {
	case s == "Etc/UTC":
		return "UTC"
	case strings.HasPrefix(s, "/"):
		return s[1:]
	}
	return s
}

// dateTimeOf reads a LocalDateTime in a JSCalendar time zone.
func dateTimeOf(local, zone string, date bool) (ical.DateTime, error) {
	v, isDate, err := parseLocal(local, date)
	if err != nil {
		return ical.DateTime{}, err
	}
	if isDate {
		return ical.DateTime{Local: v, IsDate: true}, nil
	}
	return ical.DateTime{Local: v, TZID: zoneIn(zone)}, nil
}

// utcOut writes a value as a UTCDateTime.
func utcOut(d ical.DateTime, z ical.Zones) string {
	if d.IsZero() {
		return ""
	}
	t, err := d.Time(z)
	if err != nil {
		return ""
	}
	return t.UTC().Format(layoutUTC)
}

// utcIn reads a UTCDateTime, fractions of a second dropped.
func utcIn(s string) (ical.DateTime, error) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil || !strings.HasSuffix(s, "Z") {
		return ical.DateTime{}, errBadTime
	}
	return ical.DateTime{Local: t.UTC().Format(layoutICal), TZID: "UTC"}, nil
}

// durationOut writes an iCalendar duration as a JSCalendar Duration, which
// has no sign; false for a negative one.
func durationOut(s string) (string, bool) {
	d, err := ical.ParseDuration(s)
	if err != nil || d.Negative {
		return "", false
	}
	return d.String(), true
}

// signedOut writes an iCalendar duration as a SignedDuration.
func signedOut(s string) (string, bool) {
	d, err := ical.ParseDuration(s)
	if err != nil {
		return "", false
	}
	return d.String(), true
}

// between returns the duration from start to end: whole days between two
// dates, the elapsed time between two times. False when it cannot be said
// (a date and a time, or an end before the start).
func between(start, end ical.DateTime, z ical.Zones) (string, bool) {
	if start.IsDate != end.IsDate {
		return "", false
	}
	a, err1 := start.Time(z)
	b, err2 := end.Time(z)
	if err1 != nil || err2 != nil || b.Before(a) {
		return "", false
	}
	if start.IsDate {
		return ical.Duration{Days: int(b.Sub(a).Hours() / 24)}.String(), true
	}
	return ical.Duration{Clock: b.Sub(a)}.String(), true
}

// plus returns start moved by a duration, written as start is: days are
// calendar days in its zone, the rest elapsed time.
func plus(start ical.DateTime, dur string, z ical.Zones) (ical.DateTime, error) {
	d, err := ical.ParseDuration(dur)
	if err != nil {
		return ical.DateTime{}, err
	}
	if start.IsDate {
		if d.Clock != 0 {
			return ical.DateTime{}, errBadTime
		}
		t, err := time.Parse(layoutICalDay, start.Local)
		if err != nil {
			return ical.DateTime{}, err
		}
		return ical.DateTime{Local: d.Add(t).Format(layoutICalDay), IsDate: true}, nil
	}
	t, err := start.Time(z)
	if err != nil {
		return ical.DateTime{}, err
	}
	return ical.DateTime{Local: d.Add(t).In(z.Location(start.TZID)).Format(layoutICal), TZID: start.TZID}, nil
}

// keyOf writes a recurrence id as a key of recurrenceOverrides: its wall
// time in the master's zone.
func keyOf(d, master ical.DateTime, z ical.Zones) string {
	if d.IsDate || master.IsDate || d.TZID == master.TZID {
		return localOf(d)
	}
	t, err := d.Time(z)
	if err != nil {
		return localOf(d)
	}
	return t.In(z.Location(master.TZID)).Format(layoutLocal)
}

// atKey reads a key of recurrenceOverrides as a value written as the
// master's start is.
func atKey(key string, master ical.DateTime) (ical.DateTime, error) {
	v, isDate, err := parseLocal(key, master.IsDate)
	if err != nil {
		return ical.DateTime{}, err
	}
	if master.IsDate && !isDate {
		return ical.DateTime{}, errBadTime
	}
	if isDate {
		return ical.DateTime{Local: v, IsDate: true}, nil
	}
	return ical.DateTime{Local: v, TZID: master.TZID}, nil
}
