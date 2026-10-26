package tz

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
)

// lastScanYear bounds the transitions VTimezone reads from Go's zone data.
// The final regular rule is written without an end, so it covers later years;
// a fixed bound keeps the output independent of the date it is generated on.
const lastScanYear = 2037

// transition is one change of a zone's offset.
type transition struct {
	at       time.Time // the instant, in UTC
	from, to int       // offsets in seconds east of UTC
	dst      bool      // whether the new offset is daylight time
	name     string    // abbreviation after the change
}

// local returns the wall time of the change in the offset it leaves, which
// is how an observance's DTSTART is written (RFC 5545 §3.6.5).
func (t transition) local() time.Time { return t.at.Add(time.Duration(t.from) * time.Second) }

// transitions returns the changes of loc's offset between the start of year
// from and the end of year to.
func transitions(loc *time.Location, from, to int) []transition {
	var out []transition
	cur := time.Date(from, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(to+1, 1, 1, 0, 0, 0, 0, time.UTC)
	_, off := cur.In(loc).Zone()
	dst := cur.In(loc).IsDST()
	for cur.Before(end) {
		next := cur.AddDate(0, 0, 1)
		n2, o2 := next.In(loc).Zone()
		d2 := next.In(loc).IsDST()
		if o2 != off || d2 != dst {
			// Find the first second with the new offset.
			lo, hi := cur.Unix(), next.Unix()
			for hi-lo > 1 {
				mid := lo + (hi-lo)/2
				if _, o := time.Unix(mid, 0).In(loc).Zone(); o == off && time.Unix(mid, 0).In(loc).IsDST() == dst {
					lo = mid
				} else {
					hi = mid
				}
			}
			out = append(out, transition{at: time.Unix(hi, 0).UTC(), from: off, to: o2, dst: d2, name: n2})
			off, dst = o2, d2
		}
		cur = next
	}
	return out
}

// ruleKey identifies transitions that one yearly RRULE can describe.
type ruleKey struct {
	from, to int
	dst      bool
	name     string
	month    time.Month
	weekday  time.Weekday
	nth      int // 1-4, or -1 for the last in the month
	clock    time.Duration
}

func keyOf(t transition) ruleKey {
	l := t.local()
	lastDay := time.Date(l.Year(), l.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	nth := (l.Day()-1)/7 + 1
	if l.Day()+7 > lastDay {
		nth = -1
	}
	clock := time.Duration(l.Hour())*time.Hour + time.Duration(l.Minute())*time.Minute + time.Duration(l.Second())*time.Second
	return ruleKey{t.from, t.to, t.dst, t.name, l.Month(), l.Weekday(), nth, clock}
}

var weekdayCodes = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// FormatOffset writes a UTC offset as TZOFFSETFROM and TZOFFSETTO do:
// +hhmm, or +hhmmss when there are seconds.
func FormatOffset(sec int) string {
	sign := '+'
	if sec < 0 {
		sign, sec = '-', -sec
	}
	if sec%60 != 0 {
		return fmt.Sprintf("%c%02d%02d%02d", sign, sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%c%02d%02d", sign, sec/3600, sec/60%60)
}

const localLayout = "20060102T150405"

// observance is one STANDARD or DAYLIGHT block being built.
type observance struct {
	first transition
	rrule string
	rdate []time.Time // further onsets, local
}

func (o *observance) component() *contentline.Component {
	name := "STANDARD"
	if o.first.dst {
		name = "DAYLIGHT"
	}
	c := &contentline.Component{Name: name}
	add := func(n, v string) { c.Props = append(c.Props, contentline.Property{Name: n, Value: v}) }
	add("TZOFFSETFROM", FormatOffset(o.first.from))
	add("TZOFFSETTO", FormatOffset(o.first.to))
	if o.first.name != "" && o.first.name[0] != '+' && o.first.name[0] != '-' {
		add("TZNAME", o.first.name)
	}
	add("DTSTART", o.first.local().Format(localLayout))
	if o.rrule != "" {
		add("RRULE", o.rrule)
	}
	for _, r := range o.rdate {
		add("RDATE", r.Format(localLayout))
	}
	return c
}

type vtzKey struct {
	name string
	from int
}

var vtzCache sync.Map // vtzKey -> *contentline.Component

// VTimezone returns a VTIMEZONE describing loc from the start of year from,
// built from Go's zone data. Regular yearly changes become RRULEs, the last
// of them open-ended; irregular ones become RDATEs. The result depends only
// on loc, from and the zone data, never on the current date. Callers must not
// modify it.
func VTimezone(loc *time.Location, from int) *contentline.Component {
	key := vtzKey{loc.String(), from}
	if v, ok := vtzCache.Load(key); ok {
		return v.(*contentline.Component)
	}
	to := max(lastScanYear, from+2)
	ts := transitions(loc, from, to)
	vtz := &contentline.Component{Name: "VTIMEZONE", Props: []contentline.Property{{Name: "TZID", Value: loc.String()}}}

	if len(ts) == 0 {
		at := time.Date(from, 1, 1, 0, 0, 0, 0, time.UTC)
		name, off := at.In(loc).Zone()
		o := observance{first: transition{at: at.Add(-time.Duration(off) * time.Second), from: off, to: off, name: name}}
		vtz.Components = append(vtz.Components, o.component())
		vtzCache.Store(key, vtz)
		return vtz
	}

	// A run is the same rule in consecutive years.
	type run struct{ ts []transition }
	byKey := map[ruleKey][]run{}
	var keys []ruleKey
	for _, t := range ts {
		k := keyOf(t)
		runs := byKey[k]
		if n := len(runs); n > 0 {
			last := runs[n-1].ts
			if last[len(last)-1].local().Year()+1 == t.local().Year() {
				runs[n-1].ts = append(last, t)
				byKey[k] = runs
				continue
			}
		}
		if len(runs) == 0 {
			keys = append(keys, k)
		}
		byKey[k] = append(runs, run{ts: []transition{t}})
	}

	type singleKey struct {
		from, to int
		dst      bool
		name     string
	}
	singles := map[singleKey]*observance{}
	var obs []*observance
	for _, k := range keys {
		for _, r := range byKey[k] {
			if len(r.ts) < 2 {
				sk := singleKey{k.from, k.to, k.dst, k.name}
				if o, ok := singles[sk]; ok {
					o.rdate = append(o.rdate, r.ts[0].local())
					continue
				}
				o := &observance{first: r.ts[0]}
				singles[sk] = o
				obs = append(obs, o)
				continue
			}
			nth := fmt.Sprint(k.nth)
			rule := fmt.Sprintf("FREQ=YEARLY;BYMONTH=%d;BYDAY=%s%s", k.month, nth, weekdayCodes[k.weekday])
			last := r.ts[len(r.ts)-1]
			if last.local().Year() < to {
				rule += ";UNTIL=" + last.at.Format("20060102T150405Z")
			}
			obs = append(obs, &observance{first: r.ts[0], rrule: rule})
		}
	}
	for _, o := range obs {
		slices.SortFunc(o.rdate, func(a, b time.Time) int { return a.Compare(b) })
	}
	slices.SortFunc(obs, func(a, b *observance) int {
		return cmp.Or(a.first.at.Compare(b.first.at), cmp.Compare(a.rrule, b.rrule))
	})
	for _, o := range obs {
		vtz.Components = append(vtz.Components, o.component())
	}
	vtzCache.Store(key, vtz)
	return vtz
}
