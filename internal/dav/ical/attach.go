package ical

import (
	"errors"
	"slices"
	"strings"
)

// Managed attachments (RFC 8607): helpers over an object's ATTACH
// properties for the operations a client asks for by POST.

// ErrNoInstance is returned for a recurrence ID that names no component and
// no occurrence of the series.
var ErrNoInstance = errors.New("ical: no such instance")

// ManagedIDs returns the MANAGED-IDs the object's components name.
func (o *Object) ManagedIDs() []string {
	var out []string
	for _, e := range o.Components {
		for _, a := range e.Attachments {
			if a.ManagedID != "" && !slices.Contains(out, a.ManagedID) {
				out = append(out, a.ManagedID)
			}
		}
	}
	return out
}

// Targets returns the components that rids name, as RFC 8607's rid query
// parameter lists them: "M" for the master, otherwise a RECURRENCE-ID value
// as stored (in its own zone, not converted to UTC). An occurrence without an
// override gets one, a copy of the master (Override). No rids means every
// component.
func (o *Object) Targets(rids []string, z Zones) ([]*Component, error) {
	if len(rids) == 0 {
		return slices.Clone(o.Components), nil
	}
	var out []*Component
	add := func(e *Component) error {
		if slices.Contains(out, e) {
			return ErrNoInstance // the same instance twice
		}
		out = append(out, e)
		return nil
	}
	for _, rid := range rids {
		if strings.EqualFold(rid, "M") {
			m := o.Master()
			if m == nil {
				return nil, ErrNoInstance
			}
			if err := add(m); err != nil {
				return nil, err
			}
			continue
		}
		var found *Component
		for _, e := range o.Components {
			if e.IsOverride() && e.RecurrenceID.value() == rid {
				found = e
			}
		}
		if found == nil {
			m := o.Master()
			if m == nil || !m.Recurs() {
				return nil, ErrNoInstance
			}
			d := DateTime{Local: strings.TrimSuffix(rid, "Z"), TZID: m.Start.TZID, IsDate: len(rid) == 8}
			if strings.HasSuffix(rid, "Z") {
				d.TZID = "UTC"
			}
			t, err := d.Time(z)
			if err != nil {
				return nil, ErrNoInstance
			}
			if found, err = o.Override(t, z); err != nil {
				return nil, ErrNoInstance
			}
		}
		if err := add(found); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Holding returns the components with an attachment of this MANAGED-ID.
func (o *Object) Holding(managedID string) []*Component {
	var out []*Component
	for _, e := range o.Components {
		if slices.ContainsFunc(e.Attachments, func(a Attachment) bool { return a.ManagedID == managedID }) {
			out = append(out, e)
		}
	}
	return out
}

// RemoveAttachment drops the attachments with this MANAGED-ID from e and
// reports whether it had any.
func (e *Component) RemoveAttachment(managedID string) bool {
	n := len(e.Attachments)
	e.Attachments = slices.DeleteFunc(e.Attachments, func(a Attachment) bool { return a.ManagedID == managedID })
	return len(e.Attachments) != n
}
