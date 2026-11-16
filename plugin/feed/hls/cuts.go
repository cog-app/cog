package hls

import (
	"math"
	"slices"
)

// Cut is a span of item time, in seconds, a skip presentation leaves out.
type Cut struct{ Start, End float64 }

// CutMargin is how much of a silence is kept at each end of a cut.
const CutMargin = 0.1

// MinPiece is the shortest piece of media worth keeping or encoding.
const MinPiece = 0.1

// EffectiveCuts turns silence ranges into the cuts a presentation applies:
// ranges shorter than minLen dropped, ranges closer than minLen merged,
// each shrunk by CutMargin at both ends, clamped to [0, duration], sorted.
//
// An end within CutMargin of 0 or of duration is snapped to it and not
// shrunk, so no sliver of media is left at the item's edge. A duration of
// +Inf leaves the end unclamped. silences is not modified.
func EffectiveCuts(silences []Cut, minLen, duration float64) []Cut {
	sorted := slices.Clone(silences)
	slices.SortFunc(sorted, func(a, b Cut) int {
		if a.Start != b.Start {
			return cmpFloat(a.Start, b.Start)
		}
		return cmpFloat(a.End, b.End)
	})

	var merged []Cut
	for _, s := range sorted {
		if s.End-s.Start < minLen {
			continue
		}
		if n := len(merged); n > 0 && s.Start-merged[n-1].End < minLen {
			merged[n-1].End = math.Max(merged[n-1].End, s.End)
			continue
		}
		merged = append(merged, s)
	}

	var cuts []Cut
	for _, c := range merged {
		if c.Start <= CutMargin {
			c.Start = 0
		} else {
			c.Start += CutMargin
		}
		if c.End >= duration-CutMargin {
			c.End = duration
		} else {
			c.End -= CutMargin
		}
		if c.End-c.Start > 0 {
			cuts = append(cuts, c)
		}
	}
	return cuts
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// PresentationDuration is duration less the cuts.
func PresentationDuration(cuts []Cut, duration float64) float64 {
	for _, c := range cuts {
		duration -= c.End - c.Start
	}
	return duration
}

// ItemTime maps a time in a presentation with cuts to item time.
//
// The mapping has no duration to clamp to, so a time past the end of the
// presentation maps past the end of the item; callers clamp.
func ItemTime(cuts []Cut, presentation float64) float64 {
	t := presentation
	for _, c := range cuts {
		if t < c.Start {
			break
		}
		t += c.End - c.Start
	}
	return t
}

// PresentationTime maps item time to presentation time; an item time
// inside a cut maps to where the cut is in the presentation.
func PresentationTime(cuts []Cut, item float64) float64 {
	removed := 0.0
	for _, c := range cuts {
		switch {
		case item >= c.End:
			removed += c.End - c.Start
		case item > c.Start:
			return c.Start - removed
		default:
			return item - removed
		}
	}
	return item - removed
}
