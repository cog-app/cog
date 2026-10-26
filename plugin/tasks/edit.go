package tasks

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/icalsql"
)

// layoutInput is the value of a datetime-local input.
const layoutInput = "2006-01-02T15:04"

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
		due:         r.FormValue("due_at_date"),
		start:       r.FormValue("start_at_date"),
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
		if _, err := time.Parse(layoutInput, v); err != nil {
			return nil, fmt.Errorf("invalid date %q", v)
		}
	}
	return f, nil
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
	z := icalsql.ZonesFor(o, zone)
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
		if v == shown {
			return nil
		}
		timeChanged = true
		if v == "" {
			*d = ical.DateTime{}
			return nil
		}
		t, err := time.ParseInLocation(layoutInput, v, zone)
		if err != nil {
			return err
		}
		*d = ical.DateTimeOf(t, false)
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

// formTimes is how the edit form shows a task's times.
type formTimes struct {
	Due, Start string // datetime-local values
	Zone       string // the zone they are in
}
