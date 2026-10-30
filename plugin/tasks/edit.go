package tasks

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// layoutInput is the value of a datetime-local input, and layoutDate of a
// date input: a time the form shows or submits is one or the other.
const (
	layoutInput = "2006-01-02T15:04"
	layoutDate  = "2006-01-02"
)

// inputValue writes a stored time as the edit form shows it: wall time in
// the list's zone, or a DATE at midnight.
func inputValue(d ical.DateTime, z ical.Zones, zone *time.Location) string {
	if d.IsZero() {
		return ""
	}
	t, err := d.Time(z)
	if err != nil {
		return ""
	}
	if d.IsDate {
		return t.Format(layoutInput)
	}
	return t.In(zone).Format(layoutInput)
}

// taskForm is the task edit form as submitted.
type taskForm struct {
	summary, description string
	priority, percent    int
	status               string
	due, start           string // datetime-local values
	parentUID            string
}

func parseTaskForm(r *http.Request) (*taskForm, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	f := &taskForm{
		summary:     r.FormValue("summary"),
		description: r.FormValue("description"),
		status:      r.FormValue("status"),
		due:         formWhen(r, "due"),
		start:       formWhen(r, "start"),
	}
	if f.status == "" {
		f.status = "NEEDS-ACTION"
	}
	var err error
	if f.priority, err = formInt(r, "priority", 0, 9); err != nil {
		return nil, err
	}
	if f.percent, err = formInt(r, "percent_complete", 0, 100); err != nil {
		return nil, err
	}
	for _, v := range []string{f.due, f.start} {
		if v == "" {
			continue
		}
		if _, err := parseWhen(v, time.UTC); err != nil {
			return nil, fmt.Errorf("invalid date %q", v)
		}
	}
	return f, nil
}

// formWhen reads one of the form's times: a date input (name_date) and an
// optional time input (name_time), which make a date or a date-time, or a
// single datetime-local value (name_at_date).
func formWhen(r *http.Request, name string) string {
	if _, ok := r.Form[name+"_date"]; !ok {
		return r.FormValue(name + "_at_date")
	}
	d, t := r.FormValue(name+"_date"), r.FormValue(name+"_time")
	if d == "" || t == "" {
		return d
	}
	return d + "T" + t
}

// parseWhen parses a form time in loc; a date alone is a whole day.
func parseWhen(v string, loc *time.Location) (ical.DateTime, error) {
	if len(v) == len(layoutDate) {
		t, err := time.Parse(layoutDate, v)
		if err != nil {
			return ical.DateTime{}, err
		}
		return ical.DateTimeOf(t, true), nil
	}
	t, err := time.ParseInLocation(layoutInput, v, loc)
	if err != nil {
		return ical.DateTime{}, err
	}
	return ical.DateTimeOf(t, false), nil
}

func formInt(r *http.Request, name string, lo, hi int) (int, error) {
	v := r.FormValue(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be a number from %d to %d", name, lo, hi)
	}
	return n, nil
}

// applyForm writes a submitted form into a task's master component. Only
// what the form shows changes, and only when it differs from what the form
// was shown with: overrides, alarms, attendees, relations other than the
// parent, the recurrence and everything Cog does not model stay. Changed
// fields drop their dependent leftovers; DTSTAMP and LAST-MODIFIED always
// move, and SEQUENCE when the times or the status changed (RFC 5545
// §3.8.7.4). Completing a recurring task moves it on to its next occurrence
// (Complete).
func applyForm(o *ical.Object, f *taskForm, zone *time.Location, now time.Time) error {
	e := o.Components[0]
	z := ical.ZonesFor(o, zone)
	scheduling := false

	shownPercent := 0
	if e.PercentComplete != nil {
		shownPercent = *e.PercentComplete
	}
	shownDue, shownStart := inputValue(e.Due, z, zone), inputValue(e.Start, z, zone)

	if e.Summary != f.summary {
		e.Summary = f.summary
		e.Changed("SUMMARY")
	}
	if e.Description != f.description {
		e.Description = f.description
		e.Changed("DESCRIPTION")
	}
	if shown := intOr(e.Priority); shown != f.priority {
		p := f.priority
		e.Priority = &p
	}

	before, _, beforeErr := e.Span(z)
	timeChanged := false
	setTime := func(d *ical.DateTime, shown, v string) error {
		if v == shown || (d.IsDate && v != "" && v+"T00:00" == shown) {
			return nil
		}
		timeChanged = true
		if v == "" {
			*d = ical.DateTime{}
			return nil
		}
		t, err := parseWhen(v, zone)
		if err != nil {
			return err
		}
		*d = t
		return nil
	}
	if err := setTime(&e.Start, shownStart, f.start); err != nil {
		return err
	}
	dueWas := e.Due
	if err := setTime(&e.Due, shownDue, f.due); err != nil {
		return err
	}
	if timeChanged {
		if e.Due != dueWas || e.Start.IsZero() {
			// DUE and DURATION exclude each other, and DURATION needs a
			// DTSTART.
			e.Duration = ""
		}
		e.Changed("TIME")
		scheduling = true
		if after, _, err := e.Span(z); err == nil && beforeErr == nil && e.Recurs() {
			// The occurrences moved with the series; keep its exceptions
			// and overrides on them.
			o.ShiftExceptions(after.Sub(before), z)
		}
	}

	shownStatus := e.Status
	if shownStatus == "" {
		shownStatus = "NEEDS-ACTION"
	}
	if f.status != shownStatus {
		scheduling = true
		if f.status == "COMPLETED" {
			if err := Complete(o, z, now); err != nil {
				return err
			}
			e = o.Components[0]
		} else {
			setStatus(e, f.status, now)
		}
	}
	if f.percent != shownPercent {
		p := f.percent
		e.PercentComplete = &p
	}
	e.SetParent(f.parentUID)
	e.Stamp(now, scheduling)
	return nil
}

func intOr(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}

// formTimes is how the edit form shows a task's times: date and time input
// values, the time empty for a date.
type formTimes struct {
	DueDate, DueTime     string
	StartDate, StartTime string
	Zone                 string // the list's zone, "" when it has none
}

// formTimesOf returns the edit form's times for a task in list.
func formTimesOf(o *ical.Object, list *TaskList) formTimes {
	z := ical.ZonesFor(o, list.Zone())
	m := o.Components[0]
	var f formTimes
	f.DueDate, f.DueTime = splitInput(m.Due, inputValue(m.Due, z, list.Zone()))
	f.StartDate, f.StartTime = splitInput(m.Start, inputValue(m.Start, z, list.Zone()))
	if list.Timezone != "" {
		f.Zone = list.Zone().String()
	}
	return f
}

// splitInput splits a datetime-local value into a date and a time, with no
// time for a date.
func splitInput(d ical.DateTime, v string) (string, string) {
	date, clock, _ := strings.Cut(v, "T")
	if d.IsDate {
		clock = ""
	}
	return date, clock
}
