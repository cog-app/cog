package calendar

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
)

// What the calendar pages show, and how: helpers for pages.templ.

// deleteURL deletes the event, or with the scope chosen in the dialog, part
// of a series.
func deleteURL(ev *Event) string {
	if ev.Occurrence != 0 {
		return fmt.Sprintf("/apps/calendar/events/%d?occurrence=%d", ev.ID, ev.Occurrence)
	}
	return fmt.Sprintf("/apps/calendar/events/%d", ev.ID)
}

// displayZone is the zone an event's times are shown in on the server: the
// zone its form edits it in, or UTC (the browser shows its own zone).
func displayZone(ev *Event, cal *Calendar) *time.Location {
	if z := eventEditZone(ev, cal); z != nil {
		return z
	}
	return time.UTC
}

// zoneName is a zone's IANA name, "" for none.
func zoneName(z *time.Location) string {
	if z == nil {
		return ""
	}
	return z.String()
}

// formatEventTime says when an event is, in loc and relative to now:
// "Today · 10:00 AM – 11:30 AM", "Wed, Mar 12, 2025 · 10:00 AM – 11:30 AM",
// "Thu, Mar 13 – Mon, Mar 17" for days. The page re-renders it in the
// browser's locale and zone; this is what it shows before that.
func formatEventTime(dtstart, dtend int64, isAllDay bool, loc *time.Location, now time.Time) string {
	if dtstart == 0 {
		return ""
	}
	start, end := time.Unix(dtstart, 0).In(loc), time.Unix(dtend, 0).In(loc)
	if isAllDay {
		// Dates are stored at midnight UTC; the end is exclusive.
		start, end = start.UTC(), end.UTC().AddDate(0, 0, -1)
	}
	now = now.In(start.Location())
	day := func(t time.Time) string {
		y1, m1, d1 := t.Date()
		for i, name := range []string{"Today", "Tomorrow"} {
			y2, m2, d2 := now.AddDate(0, 0, i).Date()
			if y1 == y2 && m1 == m2 && d1 == d2 {
				return name
			}
		}
		if t.Year() == now.Year() {
			return t.Format("Mon, Jan 2")
		}
		return t.Format("Mon, Jan 2, 2006")
	}
	sameDay := start.Format("20060102") == end.Format("20060102")
	switch {
	case isAllDay && sameDay:
		return day(start)
	case isAllDay:
		return day(start) + " – " + day(end)
	case sameDay:
		return day(start) + " · " + start.Format("3:04 PM") + " – " + end.Format("3:04 PM")
	}
	return day(start) + ", " + start.Format("3:04 PM") + " – " + day(end) + ", " + end.Format("3:04 PM")
}

func evSummary(ev *Event) string {
	if ev == nil {
		return ""
	}
	return ev.Summary
}

func evLocation(ev *Event) string {
	if ev == nil {
		return ""
	}
	return ev.Location
}

func evDescription(ev *Event) string {
	if ev == nil {
		return ""
	}
	return ev.Description
}

func evColor(ev *Event) string {
	if ev == nil {
		return ""
	}
	return strings.ToLower(ev.Color)
}

// timedValue fills a datetime-local input with an event's start (or end):
// its wall time in the zone it is edited in, or, without one, the Unix
// time for the page to show in the browser's zone.
func timedValue(ev *Event, cal *Calendar, start bool) templ.Attributes {
	if ev == nil || ev.IsAllDay {
		return templ.Attributes{}
	}
	t := ev.DTEnd
	if start {
		t = ev.DTStart
	}
	if z := eventEditZone(ev, cal); z != nil {
		return templ.Attributes{"value": time.Unix(t, 0).In(z).Format("2006-01-02T15:04")}
	}
	return templ.Attributes{"data-unix": strconv.FormatInt(t, 10)}
}

// dateValue fills a date input with an all-day event's first or (exclusive)
// end date.
func dateValue(ev *Event, start bool) string {
	if ev == nil || !ev.IsAllDay {
		return ""
	}
	t := ev.DTEnd
	if start {
		t = ev.DTStart
	}
	return time.Unix(t, 0).UTC().Format("2006-01-02")
}

// customRRule is the rule the custom field shows: the event's, when the
// simple choices cannot show it.
func customRRule(ev *Event) string {
	if eventRRuleFreq(ev) == "CUSTOM" {
		return eventRRuleRaw(ev)
	}
	return ""
}

type choice struct{ Value, Label string }

var freqOptions = []choice{
	{"NONE", "Does not repeat"},
	{"DAILY", "Daily"},
	{"WEEKLY", "Weekly"},
	{"MONTHLY", "Monthly"},
	{"YEARLY", "Yearly"},
	{"CUSTOM", "Custom rule…"},
}

// freqUnit is what "Every N …" counts.
func freqUnit(freq string) string {
	switch freq {
	case "DAILY":
		return "days"
	case "MONTHLY":
		return "months"
	case "YEARLY":
		return "years"
	}
	return "weeks"
}

type weekday struct{ Code, Short, Name string }

var weekdays = []weekday{
	{"MO", "M", "Monday"}, {"TU", "T", "Tuesday"}, {"WE", "W", "Wednesday"}, {"TH", "T", "Thursday"},
	{"FR", "F", "Friday"}, {"SA", "S", "Saturday"}, {"SU", "S", "Sunday"},
}

type swatch struct{ Hex, Name string }

// swatchColors are the colours an event can take with one click.
var swatchColors = []swatch{
	{"#d50000", "Tomato"}, {"#f4511e", "Tangerine"}, {"#f6bf26", "Banana"}, {"#33b679", "Sage"},
	{"#039be5", "Peacock"}, {"#3f51b5", "Blueberry"}, {"#8e24aa", "Grape"}, {"#616161", "Graphite"},
}

func isSwatch(c string) bool {
	for _, s := range swatchColors {
		if s.Hex == c {
			return true
		}
	}
	return false
}

// customColor is the colour picker's value: the event's own colour when it
// is not a swatch.
func customColor(own string) string {
	if own != "" && !isSwatch(own) {
		return own
	}
	return "#0082c9"
}

// formCalColor is the colour of the calendar a form starts on: the event's,
// or the first one a new event can go in.
func formCalColor(ev *Event, cal *Calendar, calendars []Calendar) string {
	if ev != nil && cal != nil && cal.Color != "" {
		return cal.Color
	}
	if w := writable(calendars); len(w) > 0 && w[0].Color != "" {
		return w[0].Color
	}
	return "#0082c9"
}

// calendarMeta is the line under a calendar's name in settings.
func calendarMeta(cal Calendar) string {
	zone := cal.Timezone
	if zone == "" {
		zone = "UTC"
	}
	return cal.UID + " · " + zone
}

func sidebarItemClass(active bool) string {
	if active {
		return "sidebar-item active"
	}
	return "sidebar-item"
}
