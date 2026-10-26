package ical

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/teambition/rrule-go"
)

// maxInstances bounds how many occurrences one walk produces. A finite rule
// with more than this is treated as unbounded, and expansion stops there.
const maxInstances = 100000

// ErrNoStart is returned for a component without a readable DTSTART.
var ErrNoStart = errors.New("ical: no DTSTART")

// Instance is one occurrence of an object's events.
type Instance struct {
	// Event describes the occurrence: the master, or the override that
	// replaces it.
	Event *Event
	Start time.Time
	End   time.Time
	// RecurrenceID is the master occurrence this is; zero for an event that
	// does not recur.
	RecurrenceID time.Time
}

// Span returns a component's own start and end. Without DTEND or DURATION, an
// all-day event lasts one day and a timed one is an instant (RFC 5545
// §3.6.1).
func (e *Event) Span(z Zones) (time.Time, time.Time, error) {
	if e.Start.IsZero() {
		return time.Time{}, time.Time{}, ErrNoStart
	}
	start, err := e.Start.Time(z)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err := e.endFor(start, z)
	return start, end, err
}

// endFor returns the end of an occurrence starting at start: DURATION is
// nominal, so it is added in the occurrence's zone; DTEND gives an exact
// length.
func (e *Event) endFor(start time.Time, z Zones) (time.Time, error) {
	switch {
	case e.Duration != "":
		d, err := ParseDuration(e.Duration)
		if err != nil {
			return start, err
		}
		return d.Add(start), nil
	case !e.End.IsZero():
		first, err := e.Start.Time(z)
		if err != nil {
			return start, err
		}
		end, err := e.End.Time(z)
		if err != nil {
			return start, err
		}
		if end.Before(first) {
			return start, nil
		}
		if e.Start.IsDate {
			return start.AddDate(0, 0, int(end.Sub(first).Hours()/24)), nil
		}
		return start.Add(end.Sub(first)), nil
	case e.Start.IsDate:
		return start.AddDate(0, 0, 1), nil
	}
	return start, nil
}

// Recurs reports whether the component has a recurrence rule or dates.
func (e *Event) Recurs() bool { return e.RRule != "" || len(e.RDates) > 0 }

// exclusions holds a master's EXDATEs: instants, and whole days for DATE
// values on a DATE-TIME event, which some clients send.
type exclusions struct {
	at   map[int64]bool
	days map[string]bool
}

func (x exclusions) has(t time.Time) bool {
	return x.at[t.Unix()] || x.days[t.Format(layoutDate)]
}

func excluded(m *Event, z Zones) (exclusions, error) {
	x := exclusions{at: map[int64]bool{}, days: map[string]bool{}}
	for _, d := range m.ExDates {
		if d.IsDate && !m.Start.IsDate {
			x.days[d.Local] = true
			continue
		}
		t, err := d.Time(z)
		if err != nil {
			return x, err
		}
		x.at[t.Unix()] = true
	}
	return x, nil
}

// recurrence builds the master's occurrence set, DTSTART included.
func recurrence(m *Event, z Zones) (*rrule.Set, *rrule.ROption, error) {
	start, err := m.Start.Time(z)
	if err != nil {
		return nil, nil, err
	}
	set := &rrule.Set{}
	set.DTStart(start)
	var opt *rrule.ROption
	if m.RRule != "" {
		if opt, err = rrule.StrToROptionInLocation(m.RRule, start.Location()); err != nil {
			return nil, nil, fmt.Errorf("RRULE: %w", err)
		}
		opt.Dtstart = start
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, nil, fmt.Errorf("RRULE: %w", err)
		}
		set.RRule(r)
	}
	// DTSTART is always the first occurrence (RFC 5545 §3.8.5.3), even when
	// the rule would not produce it.
	set.RDate(start)
	for _, d := range m.RDates {
		t, err := d.Time(z)
		if err != nil {
			return nil, nil, err
		}
		set.RDate(t)
	}
	return set, opt, nil
}

// walk yields the object's instances in master order, then its overrides,
// stopping master occurrences at or after until (zero: no limit). It reports
// false when it stopped at maxInstances rather than at the end.
func (o *Object) walk(z Zones, until time.Time, yield func(Instance)) (bool, error) {
	m := o.Master()
	var x exclusions
	overrides := map[int64]bool{}
	var future []*Event // RANGE=THISANDFUTURE, by recurrence ID
	for _, e := range o.Events {
		if !e.IsOverride() {
			continue
		}
		rid, err := e.RecurrenceID.Time(z)
		if err != nil {
			continue
		}
		overrides[rid.Unix()] = true
		if e.ThisAndFuture {
			future = append(future, e)
		}
	}
	slices.SortFunc(future, func(a, b *Event) int {
		ta, _ := a.RecurrenceID.Time(z)
		tb, _ := b.RecurrenceID.Time(z)
		return ta.Compare(tb)
	})

	complete := true
	if m != nil && !m.Start.IsZero() {
		var err error
		if x, err = excluded(m, z); err != nil {
			return false, err
		}
		// A rule Cog cannot read leaves the event as its first occurrence;
		// the rule itself is kept and written back.
		set, _, rerr := recurrence(m, z)
		if !m.Recurs() || rerr != nil {
			start, end, err := m.Span(z)
			if err != nil {
				return false, err
			}
			yield(Instance{Event: m, Start: start, End: end})
		} else {
			next := set.Iterator()
			n := 0
			for {
				occ, ok := next()
				if !ok || (!until.IsZero() && !occ.Before(until)) {
					break
				}
				if n++; n > maxInstances {
					complete = false
					break
				}
				if x.has(occ) || overrides[occ.Unix()] {
					continue
				}
				in, err := applyFuture(m, future, occ, z)
				if err != nil {
					return false, err
				}
				yield(in)
			}
		}
	}
	for _, e := range o.Events {
		if !e.IsOverride() {
			continue
		}
		rid, err := e.RecurrenceID.Time(z)
		if err != nil {
			continue
		}
		// An EXDATE wins over an override of the same occurrence.
		if x.has(rid) {
			continue
		}
		start, end := rid, rid
		if !e.Start.IsZero() {
			if start, end, err = e.Span(z); err != nil {
				return false, err
			}
		} else if m != nil {
			if end, err = m.endFor(rid, z); err != nil {
				return false, err
			}
		}
		yield(Instance{Event: e, Start: start, End: end, RecurrenceID: rid})
	}
	return complete, nil
}

// applyFuture returns the occurrence at occ, as changed by the latest
// THISANDFUTURE override before it: its properties, and its shift in time.
func applyFuture(m *Event, future []*Event, occ time.Time, z Zones) (Instance, error) {
	var by *Event
	var rid time.Time
	for _, f := range future {
		t, err := f.RecurrenceID.Time(z)
		if err != nil || !t.Before(occ) {
			break
		}
		by, rid = f, t
	}
	if by == nil || by.Start.IsZero() {
		end, err := m.endFor(occ, z)
		return Instance{Event: m, Start: occ, End: end, RecurrenceID: occ}, err
	}
	fs, err := by.Start.Time(z)
	if err != nil {
		return Instance{}, err
	}
	start := occ.Add(fs.Sub(rid))
	end, err := by.endFor(start, z)
	return Instance{Event: by, Start: start, End: end, RecurrenceID: occ}, err
}

// Instances returns the instances that overlap [from, to), in start order.
// An instant event overlaps when it is inside the range.
func (o *Object) Instances(z Zones, from, to time.Time) ([]Instance, error) {
	var out []Instance
	_, err := o.walk(z, to, func(in Instance) {
		if in.Start.Before(to) && (in.End.After(from) || (in.End.Equal(in.Start) && !in.Start.Before(from))) {
			out = append(out, in)
		}
	})
	slices.SortStableFunc(out, func(a, b Instance) int { return a.Start.Compare(b.Start) })
	return out, err
}

// Bounds returns when the object's first instance starts and its last ends.
// unbounded is set when the recurrence has no end, or more instances than
// Cog expands.
func (o *Object) Bounds(z Zones) (start, end time.Time, unbounded bool, err error) {
	if m := o.Master(); m != nil && m.RRule != "" {
		if _, opt, err := recurrence(m, z); err == nil && opt.Count == 0 && opt.Until.IsZero() {
			unbounded = true
		}
	}
	if unbounded {
		// Still find the earliest start, from DTSTART and the overrides.
		for _, e := range o.Events {
			s, _, err := e.Span(z)
			if err == nil && (start.IsZero() || s.Before(start)) {
				start = s
			}
		}
		return start, time.Time{}, true, nil
	}
	first := true
	complete, err := o.walk(z, time.Time{}, func(in Instance) {
		if first || in.Start.Before(start) {
			start = in.Start
		}
		if first || in.End.After(end) {
			end = in.End
		}
		first = false
	})
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return start, end, !complete, nil
}
