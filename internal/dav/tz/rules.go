package tz

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"github.com/teambition/rrule-go"
)

// lastRuleYear bounds how far FromRules expands a VTIMEZONE's rules. After the
// last onset the zone keeps that onset's offset.
const lastRuleYear = 2100

// minRuleYear is where FromRules starts expanding a yearly rule that begins
// earlier; see onsets.
const minRuleYear = 1900

// maxOnsets bounds the onsets one observance may produce, so a hostile
// VTIMEZONE cannot make the server expand an endless rule.
const maxOnsets = 5000

var rulesCache sync.Map // serialised VTIMEZONE -> *time.Location

// FromRules builds a location from a VTIMEZONE's own STANDARD and DAYLIGHT
// rules, for a TZID that names no zone Cog knows. The location is named by
// the TZID. Results are cached by the component's text.
func FromRules(vtz *contentline.Component) (*time.Location, error) {
	var key bytes.Buffer
	if err := contentline.Write(&key, []*contentline.Component{vtz}); err != nil {
		return nil, err
	}
	if v, ok := rulesCache.Load(key.String()); ok {
		return v.(*time.Location), nil
	}
	tzid := vtz.Prop("TZID")
	if tzid == nil || tzid.Value == "" {
		return nil, errors.New("VTIMEZONE has no TZID")
	}
	var ts []transition
	for _, ob := range vtz.Components {
		if !ob.Is("STANDARD") && !ob.Is("DAYLIGHT") {
			continue
		}
		obTs, err := onsets(ob)
		if err != nil {
			return nil, fmt.Errorf("VTIMEZONE %s: %s: %w", tzid.Value, ob.Name, err)
		}
		ts = append(ts, obTs...)
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("VTIMEZONE %s has no observances", tzid.Value)
	}
	slices.SortStableFunc(ts, func(a, b transition) int { return a.at.Compare(b.at) })
	ts = slices.CompactFunc(ts, func(a, b transition) bool { return a.at.Equal(b.at) })
	loc, err := time.LoadLocationFromTZData(tzid.Value, tzif(ts))
	if err != nil {
		return nil, fmt.Errorf("VTIMEZONE %s: %w", tzid.Value, err)
	}
	rulesCache.Store(key.String(), loc)
	return loc, nil
}

// ParseOffset reads a UTC offset such as -0500, +0530 or +013045.
func ParseOffset(s string) (int, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ":", "")
	if len(s) != 5 && len(s) != 7 || (s[0] != '+' && s[0] != '-') {
		return 0, fmt.Errorf("bad UTC offset %q", s)
	}
	n := func(i int) (int, error) { return strconv.Atoi(s[i : i+2]) }
	h, err := n(1)
	if err != nil {
		return 0, fmt.Errorf("bad UTC offset %q", s)
	}
	m, err := n(3)
	if err != nil {
		return 0, fmt.Errorf("bad UTC offset %q", s)
	}
	sec := 0
	if len(s) == 7 {
		if sec, err = n(5); err != nil {
			return 0, fmt.Errorf("bad UTC offset %q", s)
		}
	}
	off := h*3600 + m*60 + sec
	if s[0] == '-' {
		off = -off
	}
	return off, nil
}

// parseLocal reads a DATE-TIME or DATE written as wall time; a trailing Z
// is tolerated and ignored, since observance times are always local.
func parseLocal(v string) (time.Time, error) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "Z")
	if len(v) == 8 {
		return time.Parse("20060102", v)
	}
	return time.Parse(localLayout, v)
}

// onsets expands one observance into the transitions it starts.
func onsets(ob *contentline.Component) ([]transition, error) {
	prop := func(name string) (string, error) {
		p := ob.Prop(name)
		if p == nil {
			return "", fmt.Errorf("missing %s", name)
		}
		return p.Value, nil
	}
	fromS, err := prop("TZOFFSETFROM")
	if err != nil {
		return nil, err
	}
	toS, err := prop("TZOFFSETTO")
	if err != nil {
		return nil, err
	}
	startS, err := prop("DTSTART")
	if err != nil {
		return nil, err
	}
	from, err := ParseOffset(fromS)
	if err != nil {
		return nil, err
	}
	to, err := ParseOffset(toS)
	if err != nil {
		return nil, err
	}
	start, err := parseLocal(startS)
	if err != nil {
		return nil, fmt.Errorf("DTSTART: %w", err)
	}
	name := ""
	if p := ob.Prop("TZNAME"); p != nil {
		name = p.Value
	}
	dst := ob.Is("DAYLIGHT")
	at := func(local time.Time) transition {
		return transition{at: local.Add(-time.Duration(from) * time.Second), from: from, to: to, dst: dst, name: name}
	}

	locals := []time.Time{start}
	for p := range ob.PropsNamed("RDATE") {
		for v := range strings.SplitSeq(p.Value, ",") {
			v, _, _ = strings.Cut(v, "/") // a PERIOD starts at its first part
			t, err := parseLocal(v)
			if err != nil {
				return nil, fmt.Errorf("RDATE: %w", err)
			}
			locals = append(locals, t)
		}
	}
	if p := ob.Prop("RRULE"); p != nil {
		rule, until, err := splitUntil(p.Value, from)
		if err != nil {
			return nil, err
		}
		opt, err := rrule.StrToROptionInLocation(rule, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("RRULE: %w", err)
		}
		opt.Dtstart = start
		// rrule-go stops about 292 years after DTSTART (its offsets are
		// time.Durations), and Outlook starts observances in 1601. Start a
		// yearly rule without COUNT at a later year in the same phase; the
		// onsets it skips are before any date Cog stores.
		if opt.Freq == rrule.YEARLY && opt.Count == 0 && start.Year() < minRuleYear {
			step := max(opt.Interval, 1)
			years := (minRuleYear - start.Year() + step - 1) / step * step
			opt.Dtstart = start.AddDate(years, 0, 0)
		}
		r, err := rrule.NewRRule(*opt)
		if err != nil {
			return nil, fmt.Errorf("RRULE: %w", err)
		}
		end := time.Date(lastRuleYear, 12, 31, 23, 59, 59, 0, time.UTC)
		next := r.Iterator()
		for range maxOnsets {
			t, ok := next()
			if !ok || t.After(end) {
				break
			}
			if !until.IsZero() && at(t).at.After(until) {
				break
			}
			locals = append(locals, t)
		}
	}
	out := make([]transition, 0, len(locals))
	for _, l := range locals {
		out = append(out, at(l))
	}
	return out, nil
}

// splitUntil removes UNTIL from an observance's RRULE and returns it as an
// instant. RFC 5545 requires it in UTC; a local UNTIL is read in the offset
// the observance starts from. rrule-go would compare it against wall times,
// which are off by that offset.
func splitUntil(rule string, from int) (string, time.Time, error) {
	var parts []string
	var until time.Time
	for part := range strings.SplitSeq(rule, ";") {
		k, v, _ := strings.Cut(part, "=")
		if !strings.EqualFold(k, "UNTIL") {
			parts = append(parts, part)
			continue
		}
		t, err := parseLocal(v)
		if err != nil {
			return "", time.Time{}, fmt.Errorf("RRULE UNTIL: %w", err)
		}
		if !strings.HasSuffix(v, "Z") {
			t = t.Add(-time.Duration(from) * time.Second)
		}
		until = t
	}
	return strings.Join(parts, ";"), until, nil
}

// tzif encodes transitions as TZif version 2 data (RFC 8536) for
// time.LoadLocationFromTZData.
func tzif(ts []transition) []byte {
	type ttinfo struct {
		off  int
		dst  bool
		name string
	}
	// Type 0 is used by no transition, which makes Go use it for instants
	// before the first one: the offset the first observance changes from.
	types := []ttinfo{{off: ts[0].from, name: FormatOffset(ts[0].from)}}
	index := map[ttinfo]int{}
	idx := make([]byte, len(ts))
	for i, t := range ts {
		ti := ttinfo{t.to, t.dst, t.name}
		if ti.name == "" {
			ti.name = FormatOffset(t.to)
		}
		n, ok := index[ti]
		if !ok {
			n = len(types)
			types = append(types, ti)
			index[ti] = n
		}
		idx[i] = byte(n)
	}
	var chars []byte
	abbr := map[string]int{}
	for _, ti := range types {
		if _, ok := abbr[ti.name]; !ok {
			abbr[ti.name] = len(chars)
			chars = append(append(chars, ti.name...), 0)
		}
	}

	var b bytes.Buffer
	be := func(v any) { binary.Write(&b, binary.BigEndian, v) }
	header := func(timecnt, typecnt, charcnt int) {
		b.WriteString("TZif2")
		b.Write(make([]byte, 15))
		for _, n := range []int{0, 0, 0, timecnt, typecnt, charcnt} {
			be(uint32(n))
		}
	}
	// Version 1 block: a single type and no transitions; readers of version
	// 2 skip it.
	header(0, 1, 1)
	be(int32(0))
	b.Write([]byte{0, 0})
	b.WriteByte(0)

	header(len(ts), len(types), len(chars))
	for _, t := range ts {
		be(t.at.Unix())
	}
	b.Write(idx)
	for _, ti := range types {
		be(int32(ti.off))
		dst := byte(0)
		if ti.dst {
			dst = 1
		}
		b.Write([]byte{dst, byte(abbr[ti.name])})
	}
	b.Write(chars)
	b.WriteString("\n\n")
	return b.Bytes()
}
