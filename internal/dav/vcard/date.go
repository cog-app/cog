package vcard

import (
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// Date is a calendar date read from BDAY or ANNIVERSARY. Year is 0 when the
// card does not say it.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// appleNoYear is the year Apple Contacts writes for a date without one,
// together with X-APPLE-OMIT-YEAR=1604 (3.0 has no form for a yearless date).
const appleNoYear = 1604

// ParseDate reads a birthday or anniversary as it is written in any vCard
// version, with the property's parameters:
//
//   - 4.0 (RFC 6350 date-and-or-time): 19950415, --0415 (no year), and a
//     date-time such as 19961022T140000 or 20090808T1430-0500;
//   - 3.0 (RFC 2426, ISO 8601 extended): 1995-04-15, --04-15, and a
//     date-time such as 1953-10-15T23:10:00Z;
//   - 2.1: either form.
//
// Apple's yearless dates (1604-04-15 with X-APPLE-OMIT-YEAR) have no year.
// A time of day is ignored: a birthday is a day. Reduced forms that name no
// day (--04, ---15, 1995-04), free text and impossible dates are not dates.
func ParseDate(value string, params []contentline.Param) (Date, bool) {
	v := strings.TrimSpace(value)
	if i := strings.IndexByte(v, 'T'); i >= 0 {
		v = v[:i]
	}
	var d Date
	var ok bool
	switch {
	case strings.HasPrefix(v, "--"):
		d, ok = monthDay(strings.TrimPrefix(v, "--"))
	case len(v) == 8 && digits(v):
		d.Year, _ = strconv.Atoi(v[:4])
		var md Date
		md, ok = monthDay(v[4:])
		d.Month, d.Day = md.Month, md.Day
	case len(v) == 10 && v[4] == '-' && v[7] == '-' && digits(v[:4]):
		d.Year, _ = strconv.Atoi(v[:4])
		var md Date
		md, ok = monthDay(v[5:])
		d.Month, d.Day = md.Month, md.Day
	}
	if !ok {
		return Date{}, false
	}
	omit := 0
	for _, p := range params {
		if strings.EqualFold(p.Name, "X-APPLE-OMIT-YEAR") && len(p.Values) > 0 {
			omit, _ = strconv.Atoi(p.Values[0])
		}
	}
	if d.Year == appleNoYear || (omit != 0 && d.Year == omit) {
		d.Year = 0
	}
	if !d.valid() {
		return Date{}, false
	}
	return d, true
}

// monthDay reads MMDD or MM-DD.
func monthDay(s string) (Date, bool) {
	s = strings.Replace(s, "-", "", 1)
	if len(s) != 4 || !digits(s) {
		return Date{}, false
	}
	m, _ := strconv.Atoi(s[:2])
	day, _ := strconv.Atoi(s[2:])
	return Date{Month: time.Month(m), Day: day}, true
}

func digits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// valid reports whether the date exists: February 29 only in a leap year,
// or in no year at all.
func (d Date) valid() bool {
	if d.Month < time.January || d.Month > time.December || d.Day < 1 {
		return false
	}
	year := d.Year
	if year == 0 {
		year = 2000 // a leap year: --0229 is a date
	}
	return d.Day <= time.Date(year, d.Month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
