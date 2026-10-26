package itip

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
)

// Free/busy types (RFC 5545 §3.2.9).
const (
	Busy          = "BUSY"
	BusyTentative = "BUSY-TENTATIVE"
)

// Period is a span of busy time.
type Period struct {
	Start, End time.Time
	Type       string // Busy or BusyTentative
}

// BusyTime returns the busy periods of an object's instances overlapping
// [from, to). Transparent and cancelled instances are free, and so are
// those the calendar user (self) declined; tentative ones, and those they
// have not answered, are tentatively busy. An all-day instance is busy for
// its days in the floating zone, which is the calendar's.
func BusyTime(o *ical.Object, z ical.Zones, self func(string) bool, from, to time.Time) ([]Period, error) {
	ins, err := o.Instances(z, from, to)
	if err != nil {
		return nil, err
	}
	var out []Period
	for _, in := range ins {
		e := in.Event
		if strings.EqualFold(e.Transp, "TRANSPARENT") || strings.EqualFold(e.Status, "CANCELLED") {
			continue
		}
		typ := Busy
		if strings.EqualFold(e.Status, "TENTATIVE") {
			typ = BusyTentative
		}
		for _, a := range e.Attendees {
			if !self(Normalize(a.Address)) {
				continue
			}
			switch strings.ToUpper(a.PartStat) {
			case "DECLINED":
				typ = ""
			case "TENTATIVE", "NEEDS-ACTION", "":
				typ = BusyTentative
			}
			break
		}
		if typ == "" {
			continue
		}
		start, end := in.Start, in.End
		if e.Start.IsDate {
			start, end = inZone(start, z.Location("")), inZone(end, z.Location(""))
		}
		if !end.After(start) {
			continue // an instant blocks no time
		}
		out = append(out, Period{start, end, typ})
	}
	return out, nil
}

// inZone reads a UTC-midnight date as midnight in loc.
func inZone(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// Merge clips periods to [from, to) and joins those of one type that overlap
// or touch, in start order.
func Merge(ps []Period, from, to time.Time) []Period {
	var clipped []Period
	for _, p := range ps {
		if p.Start.Before(from) {
			p.Start = from
		}
		if p.End.After(to) {
			p.End = to
		}
		if p.End.After(p.Start) {
			clipped = append(clipped, p)
		}
	}
	slices.SortFunc(clipped, func(a, b Period) int {
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return a.Start.Compare(b.Start)
	})
	var out []Period
	for _, p := range clipped {
		if n := len(out); n > 0 && out[n-1].Type == p.Type && !p.Start.After(out[n-1].End) {
			if p.End.After(out[n-1].End) {
				out[n-1].End = p.End
			}
			continue
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Period) int { return cmp.Compare(a.Start.Unix(), b.Start.Unix()) })
	return out
}

const utcLayout = "20060102T150405Z"

// FreeBusy writes a VFREEBUSY for [from, to) with the busy periods given.
// props come first: ORGANIZER, ATTENDEE and UID for a reply.
func FreeBusy(ps []Period, from, to, now time.Time, props ...contentline.Property) *contentline.Component {
	fb := &contentline.Component{Name: "VFREEBUSY"}
	fb.Props = append(fb.Props, props...)
	fb.Props = append(fb.Props,
		contentline.Property{Name: "DTSTAMP", Value: now.UTC().Format(utcLayout)},
		contentline.Property{Name: "DTSTART", Value: from.UTC().Format(utcLayout)},
		contentline.Property{Name: "DTEND", Value: to.UTC().Format(utcLayout)},
	)
	for _, p := range ps {
		var params []contentline.Param
		if p.Type != Busy {
			params = []contentline.Param{{Name: "FBTYPE", Values: []string{p.Type}}}
		}
		fb.Props = append(fb.Props, contentline.Property{
			Name: "FREEBUSY", Params: params, Value: p.Start.UTC().Format(utcLayout) + "/" + p.End.UTC().Format(utcLayout),
		})
	}
	return fb
}

// Calendar wraps components in a VCALENDAR, with METHOD when it is set.
func Calendar(method Method, comps ...*contentline.Component) *contentline.Component {
	cal := &contentline.Component{Name: "VCALENDAR", Props: []contentline.Property{
		{Name: "VERSION", Value: "2.0"}, {Name: "PRODID", Value: ical.ProdID},
	}}
	if method != "" {
		cal.Props = append(cal.Props, contentline.Property{Name: "METHOD", Value: string(method)})
	}
	cal.Components = comps
	return cal
}
