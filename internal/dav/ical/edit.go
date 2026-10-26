package ical

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrNotRecurring is returned for an occurrence edit of an object without a
// recurring master.
var ErrNotRecurring = errors.New("ical: not a recurring event")

// ErrNoOccurrence is returned when an object has no instance at a time.
var ErrNoOccurrence = errors.New("ical: no such occurrence")

// At writes t the way the component's DTSTART (or a task's DUE, without
// one) is written: a DATE, a UTC time, a floating time, or wall time in the
// same zone.
func (e *Component) At(t time.Time, z Zones) DateTime { return like(e.anchor(), t, z) }

// like writes t the way d is written.
func like(d DateTime, t time.Time, z Zones) DateTime {
	switch {
	case d.IsDate:
		return DateTime{Local: t.UTC().Format(layoutDate), IsDate: true}
	case d.TZID == "UTC":
		return DateTime{Local: t.UTC().Format(layoutDateTime), TZID: "UTC"}
	}
	return DateTime{Local: t.In(z.Location(d.TZID)).Format(layoutDateTime), TZID: d.TZID}
}

// shifted returns d moved by delta, written in its own zone and type.
func shifted(d DateTime, delta time.Duration, z Zones) DateTime {
	t, err := d.Time(z)
	if err != nil || d.IsZero() {
		return d
	}
	return like(d, t.Add(delta), z)
}

// placeAt sets c's times to the master's occurrence at rid: its start, and
// its DTEND or DUE, each written as the master writes it.
func (c *Component) placeAt(m *Component, rid time.Time, z Zones) error {
	end, err := m.endFor(rid, z)
	if err != nil {
		return err
	}
	if !m.Start.IsZero() {
		c.Start = like(m.Start, rid, z)
	}
	if !m.End.IsZero() {
		c.End = like(m.End, end, z)
	}
	if !m.Due.IsZero() {
		if m.Start.IsZero() {
			c.Due = like(m.Due, rid, z)
		} else {
			c.Due = like(m.Due, end, z)
		}
	}
	return nil
}

// Clone returns a copy of the component that shares nothing it could
// modify.
func (e *Component) Clone() *Component {
	c := *e
	c.RDates = slices.Clone(e.RDates)
	c.ExDates = slices.Clone(e.ExDates)
	c.Attendees = slices.Clone(e.Attendees)
	c.Categories = slices.Clone(e.Categories)
	c.Relations = slices.Clone(e.Relations)
	c.Alarms = make([]Alarm, len(e.Alarms))
	for i, a := range e.Alarms {
		a.Leftovers = a.Leftovers.Clone()
		c.Alarms[i] = a
	}
	c.Leftovers = e.Leftovers.Clone()
	if e.Organizer != nil {
		o := *e.Organizer
		c.Organizer = &o
	}
	c.Sequence = cloneInt(e.Sequence)
	c.Priority = cloneInt(e.Priority)
	c.PercentComplete = cloneInt(e.PercentComplete)
	c.SortOrder = cloneInt(e.SortOrder)
	return &c
}

func cloneInt(n *int) *int {
	if n == nil {
		return nil
	}
	v := *n
	return &v
}

// InstanceAt returns the instance that is the master's occurrence at rid:
// the override that replaces it, or the occurrence itself.
func (o *Object) InstanceAt(rid time.Time, z Zones) (Instance, error) {
	var found *Instance
	_, err := o.walk(z, rid.Add(time.Second), func(in Instance) {
		if found == nil && in.RecurrenceID.Equal(rid) {
			found = &in
		}
	})
	if err != nil {
		return Instance{}, err
	}
	if found == nil {
		return Instance{}, ErrNoOccurrence
	}
	return *found, nil
}

func (o *Object) recurringMaster() (*Component, error) {
	m := o.Master()
	if m == nil || !m.Recurs() {
		return nil, ErrNotRecurring
	}
	return m, nil
}

// overrideAt returns the override of the occurrence at rid, or nil.
func (o *Object) overrideAt(rid time.Time, z Zones) *Component {
	for _, e := range o.Components {
		if t, err := e.RecurrenceID.Time(z); e.IsOverride() && err == nil && t.Equal(rid) {
			return e
		}
	}
	return nil
}

// Override returns the component that describes the occurrence at rid,
// creating it, as a copy of the master without its recurrence, when the
// occurrence has none yet.
func (o *Object) Override(rid time.Time, z Zones) (*Component, error) {
	if e := o.overrideAt(rid, z); e != nil {
		return e, nil
	}
	m, err := o.recurringMaster()
	if err != nil {
		return nil, err
	}
	if _, err := o.InstanceAt(rid, z); err != nil {
		return nil, err
	}
	c := m.Clone()
	c.RRule, c.RDates, c.ExDates, c.ThisAndFuture = "", nil, nil, false
	c.RecurrenceID = m.At(rid, z)
	if err := c.placeAt(m, rid, z); err != nil {
		return nil, err
	}
	o.Components = append(o.Components, c)
	return c, nil
}

// Exclude removes the occurrence at rid: its override, and an EXDATE on the
// master. It reports whether the object still has components.
func (o *Object) Exclude(rid time.Time, z Zones) bool {
	o.Components = slices.DeleteFunc(o.Components, func(e *Component) bool {
		t, err := e.RecurrenceID.Time(z)
		return e.IsOverride() && err == nil && t.Equal(rid)
	})
	if m := o.Master(); m != nil && m.Recurs() {
		m.ExDates = append(m.ExDates, DateValue{DateTime: m.At(rid, z)})
	}
	return len(o.Components) > 0
}

// before reports whether d is earlier than t; unreadable values are kept.
func before(d DateTime, t time.Time, z Zones) bool {
	v, err := d.Time(z)
	return err != nil || v.Before(t)
}

// Truncate ends the series before rid: an UNTIL just before it, and no
// overrides, exceptions or extra dates from rid on. It reports whether any
// occurrence remains; none does when rid is the first.
func (o *Object) Truncate(rid time.Time, z Zones) (bool, error) {
	m, err := o.recurringMaster()
	if err != nil {
		return false, err
	}
	start, err := m.anchor().Time(z)
	if err != nil {
		return false, err
	}
	if !rid.After(start) {
		return false, nil
	}
	if m.RRule != "" {
		m.RRule = withRuleEnd(m.RRule, "UNTIL="+m.untilBefore(rid, z))
	}
	m.RDates = slices.DeleteFunc(m.RDates, func(d DateValue) bool { return !before(d.DateTime, rid, z) })
	m.ExDates = slices.DeleteFunc(m.ExDates, func(d DateValue) bool { return !before(d.DateTime, rid, z) })
	o.Components = slices.DeleteFunc(o.Components, func(e *Component) bool {
		return e.IsOverride() && !before(e.RecurrenceID, rid, z)
	})
	return true, nil
}

// untilBefore returns an UNTIL value for the last moment before rid, in the
// type RFC 5545 requires for the master's DTSTART: a DATE for a DATE, floating
// for floating, otherwise UTC.
func (e *Component) untilBefore(rid time.Time, z Zones) string {
	switch a := e.anchor(); {
	case a.IsDate:
		return rid.UTC().AddDate(0, 0, -1).Format(layoutDate)
	case a.TZID == "":
		return rid.Add(-time.Second).In(z.Location("")).Format(layoutDateTime)
	}
	return rid.Add(-time.Second).UTC().Format(layoutDateTime) + "Z"
}

// withRuleEnd replaces a rule's COUNT and UNTIL with end ("" for none).
func withRuleEnd(rule, end string) string {
	var parts []string
	for part := range strings.SplitSeq(rule, ";") {
		k, _, _ := strings.Cut(part, "=")
		if !strings.EqualFold(k, "COUNT") && !strings.EqualFold(k, "UNTIL") {
			parts = append(parts, part)
		}
	}
	if end != "" {
		parts = append(parts, end)
	}
	return strings.Join(parts, ";")
}

// Split ends the series before rid and returns the rest as a new object
// with uid: the same rule from rid on (with what is left of a COUNT), and
// the overrides, exceptions and extra dates from rid on. This is how "this
// and following" is edited; clients handle it better than
// RANGE=THISANDFUTURE.
func (o *Object) Split(rid time.Time, z Zones, uid string) (*Object, error) {
	m, err := o.recurringMaster()
	if err != nil {
		return nil, err
	}
	if _, err := o.InstanceAt(rid, z); err != nil {
		return nil, err
	}
	nm := m.Clone()
	if err := nm.placeAt(m, rid, z); err != nil {
		return nil, err
	}
	if nm.RRule != "" {
		set, opt, err := recurrence(m, z)
		if err != nil {
			return nil, err
		}
		if opt.Count > 0 {
			done := 0
			next := set.Iterator()
			for t, ok := next(); ok && t.Before(rid); t, ok = next() {
				done++
			}
			nm.RRule = withRuleEnd(nm.RRule, "COUNT="+strconv.Itoa(max(opt.Count-done, 1)))
		}
	}
	nm.RDates = slices.DeleteFunc(nm.RDates, func(d DateValue) bool { return before(d.DateTime, rid, z) })
	nm.ExDates = slices.DeleteFunc(nm.ExDates, func(d DateValue) bool { return before(d.DateTime, rid, z) })
	n := &Object{UID: uid, Props: slices.Clone(o.Props), Others: slices.Clone(o.Others), Zones: o.Zones, Components: []*Component{nm}}
	for _, e := range o.Components {
		if e.IsOverride() && !before(e.RecurrenceID, rid, z) {
			n.Components = append(n.Components, e.Clone())
		}
	}
	if _, err := o.Truncate(rid, z); err != nil {
		return nil, err
	}
	return n, nil
}

// ShiftExceptions moves the master's extra and excluded dates, and the
// recurrence IDs of its overrides, by delta, so they stay attached to their
// occurrences when the whole series moves.
func (o *Object) ShiftExceptions(delta time.Duration, z Zones) {
	if delta == 0 {
		return
	}
	for _, e := range o.Components {
		if e.IsOverride() {
			e.RecurrenceID = shifted(e.RecurrenceID, delta, z)
			continue
		}
		for i := range e.RDates {
			e.RDates[i].DateTime = shifted(e.RDates[i].DateTime, delta, z)
		}
		for i := range e.ExDates {
			e.ExDates[i].DateTime = shifted(e.ExDates[i].DateTime, delta, z)
		}
	}
}

// Advance moves a recurring series on to its next occurrence, which is how
// Cog completes one occurrence of a recurring task, as tasks.org does: the
// first occurrence goes, and with it the overrides, exceptions and extra
// dates before the next one; a COUNT counts one less, and DTSTART and DUE
// move to the next occurrence. It reports false, changing nothing, when no
// occurrence follows the first.
func (o *Object) Advance(z Zones) (bool, error) {
	m, err := o.recurringMaster()
	if err != nil {
		return false, err
	}
	set, _, err := recurrence(m, z)
	if err != nil {
		return false, err
	}
	x, err := excluded(m, z)
	if err != nil {
		return false, err
	}
	seen := false
	next := set.Iterator()
	for t, ok := next(); ok; t, ok = next() {
		if x.has(t) {
			continue
		}
		if !seen {
			seen = true
			continue
		}
		n, err := o.Split(t, z, o.UID)
		if err != nil {
			return false, err
		}
		*o = *n
		return true, nil
	}
	return false, nil
}
