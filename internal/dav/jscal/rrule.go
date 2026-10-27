package jscal

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
)

// RRULE parts and the RecurrenceRule properties they map to (RFC 8984
// section 4.3.3). Lists of numbers keep their type; BYMONTH is a list of
// strings, since RFC 7529 adds leap months ("5L").
var intLists = []struct{ part, prop string }{
	{"BYMONTHDAY", "byMonthDay"},
	{"BYYEARDAY", "byYearDay"},
	{"BYWEEKNO", "byWeekNo"},
	{"BYHOUR", "byHour"},
	{"BYMINUTE", "byMinute"},
	{"BYSECOND", "bySecond"},
	{"BYSETPOS", "bySetPosition"},
}

// ruleOut writes an RRULE as a RecurrenceRule, or reports false when it has
// a part JSCalendar does not know or a value it cannot read. until is
// written in the master's zone.
func ruleOut(rule string, start ical.DateTime, z ical.Zones) (jsobj.Object, bool) {
	out := jsobj.Object{"@type": "RecurrenceRule"}
	seen := map[string]bool{}
	for part := range strings.SplitSeq(rule, ";") {
		name, value, ok := strings.Cut(part, "=")
		name = strings.ToUpper(name)
		if !ok || value == "" || seen[name] {
			return nil, false
		}
		seen[name] = true
		switch name {
		case "FREQ":
			out["frequency"] = strings.ToLower(value)
		case "INTERVAL", "COUNT":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return nil, false
			}
			key := "interval"
			if name == "COUNT" {
				key = "count"
			}
			out[key] = json.Number(strconv.Itoa(n))
		case "UNTIL":
			u, ok := untilOut(value, start, z)
			if !ok {
				return nil, false
			}
			out["until"] = u
		case "WKST":
			out["firstDayOfWeek"] = strings.ToLower(value)
		case "RSCALE":
			out["rscale"] = strings.ToLower(value)
		case "SKIP":
			out["skip"] = strings.ToLower(value)
		case "BYDAY":
			var days []any
			for v := range strings.SplitSeq(value, ",") {
				if len(v) < 2 {
					return nil, false
				}
				nd := jsobj.Object{"@type": "NDay", "day": strings.ToLower(v[len(v)-2:])}
				if n := v[:len(v)-2]; n != "" {
					k, err := strconv.Atoi(n)
					if err != nil || k == 0 {
						return nil, false
					}
					nd["nthOfPeriod"] = json.Number(strconv.Itoa(k))
				}
				days = append(days, nd)
			}
			out["byDay"] = days
		case "BYMONTH":
			var months []any
			for v := range strings.SplitSeq(value, ",") {
				months = append(months, v)
			}
			out["byMonth"] = months
		default:
			found := false
			for _, l := range intLists {
				if l.part != name {
					continue
				}
				var nums []any
				for v := range strings.SplitSeq(value, ",") {
					n, err := strconv.Atoi(v)
					if err != nil {
						return nil, false
					}
					nums = append(nums, json.Number(strconv.Itoa(n)))
				}
				out[l.prop] = nums
				found = true
			}
			if !found {
				return nil, false
			}
		}
	}
	if _, ok := out["frequency"]; !ok {
		return nil, false
	}
	return out, true
}

// untilOut writes UNTIL as a LocalDateTime in the master's zone.
func untilOut(v string, start ical.DateTime, z ical.Zones) (string, bool) {
	switch {
	case len(v) == 8:
		t, err := time.Parse(layoutICalDay, v)
		if err != nil {
			return "", false
		}
		return t.Format(layoutLocal), true
	case strings.HasSuffix(v, "Z"):
		t, err := time.Parse(layoutICal, strings.TrimSuffix(v, "Z"))
		if err != nil {
			return "", false
		}
		if start.TZID != "" && start.TZID != "UTC" && !start.IsDate {
			t = t.In(z.Location(start.TZID))
		}
		return t.Format(layoutLocal), true
	}
	t, err := time.Parse(layoutICal, v)
	if err != nil {
		return "", false
	}
	return t.Format(layoutLocal), true
}

// untilIn writes until as RFC 5545 asks for the master's DTSTART: a DATE
// for a date, UTC for a time in a zone, floating for a floating one.
func untilIn(s string, start ical.DateTime, z ical.Zones) (string, bool) {
	t, err := time.Parse(layoutLocal, s)
	if err != nil {
		return "", false
	}
	switch {
	case start.IsDate:
		return t.Format(layoutICalDay), true
	case start.TZID == "":
		return t.Format(layoutICal), true
	case start.TZID == "UTC":
		return t.Format(layoutICal) + "Z", true
	}
	local, err := time.ParseInLocation(layoutICal, t.Format(layoutICal), z.Location(start.TZID))
	if err != nil {
		return "", false
	}
	return local.UTC().Format(layoutICal) + "Z", true
}

// ruleIn writes a RecurrenceRule as an RRULE, parts in RFC 5545's usual
// order.
func ruleIn(r jsobj.Reader, start ical.DateTime, z ical.Zones) (string, bool) {
	var parts []string
	add := func(name, v string) {
		if v != "" {
			parts = append(parts, name+"="+v)
		}
	}
	freq := r.String("frequency")
	if freq == "" {
		r.Fail("frequency")
		return "", false
	}
	add("FREQ", strings.ToUpper(freq))
	if u := r.String("until"); u != "" {
		v, ok := untilIn(u, start, z)
		if !ok {
			r.Fail("until")
			return "", false
		}
		add("UNTIL", v)
	}
	if n := r.Int("count"); n != nil {
		add("COUNT", strconv.Itoa(*n))
	}
	if n := r.Int("interval"); n != nil {
		add("INTERVAL", strconv.Itoa(*n))
	}
	ints := func(prop string) string {
		v, ok := r.Obj[prop].([]any)
		if !ok {
			if r.Has(prop) {
				r.Fail(prop)
			}
			return ""
		}
		var out []string
		for _, x := range v {
			n, ok := x.(json.Number)
			if !ok {
				r.Fail(prop)
				return ""
			}
			out = append(out, n.String())
		}
		return strings.Join(out, ",")
	}
	add("BYSECOND", ints("bySecond"))
	add("BYMINUTE", ints("byMinute"))
	add("BYHOUR", ints("byHour"))
	if days, ok := r.Obj["byDay"].([]any); ok {
		var out []string
		for _, d := range days {
			nd, ok := d.(jsobj.Object)
			if !ok {
				r.Fail("byDay")
				break
			}
			ndr := jsobj.Reader{Obj: nd, Path: r.Path + "/byDay", Bad: r.Bad}
			v := strings.ToUpper(ndr.String("day"))
			if n := ndr.Int("nthOfPeriod"); n != nil {
				v = strconv.Itoa(*n) + v
			}
			out = append(out, v)
		}
		add("BYDAY", strings.Join(out, ","))
	}
	add("BYMONTHDAY", ints("byMonthDay"))
	add("BYYEARDAY", ints("byYearDay"))
	add("BYWEEKNO", ints("byWeekNo"))
	add("BYMONTH", strings.Join(r.Strings("byMonth"), ","))
	add("BYSETPOS", ints("bySetPosition"))
	add("WKST", strings.ToUpper(r.String("firstDayOfWeek")))
	if rs := r.String("rscale"); rs != "" {
		parts = append([]string{"RSCALE=" + strings.ToUpper(rs)}, parts...)
	}
	add("SKIP", strings.ToUpper(r.String("skip")))
	return strings.Join(parts, ";"), true
}
