package hls

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
)

// planTolerance is how close two times must be to count as the same, and
// the shortest media a plan keeps at all.
const planTolerance = 1e-3

// Piece is a span of one segment a cut presentation keeps.
//
// A piece shorter than MinPiece is joined to its neighbour in the same kept
// span; the joined piece is an edge that may cross a segment boundary, and
// Segment is the first segment it touches.
type Piece struct {
	Start, End float64 // item time
	Segment    int     // index into the track's segments
	Edge       bool    // a strict part of its segment: played from an edge file
}

// unionCuts sorts cuts and merges those that overlap or touch. A cut
// shorter than 1 ms is no cut.
func unionCuts(cuts []Cut) []Cut {
	sorted := slices.Clone(cuts)
	slices.SortFunc(sorted, func(a, b Cut) int { return cmpFloat(a.Start, b.Start) })
	var out []Cut
	for _, c := range sorted {
		if c.End-c.Start < planTolerance {
			continue
		}
		if n := len(out); n > 0 && c.Start <= out[n-1].End+planTolerance {
			out[n-1].End = math.Max(out[n-1].End, c.End)
			continue
		}
		out = append(out, c)
	}
	return out
}

// PlanCuts keeps every part of every segment outside the cuts: a whole
// segment as it is, a part of one as an edge piece.
//
// Parts shorter than 1 ms are not kept. An edge piece shorter than MinPiece
// is joined to the next piece of its kept span, or the previous one at the
// span's end, which then becomes an edge too; a short whole segment stays
// as it is, since it needs no encoding. A kept span shorter than MinPiece
// at the track's start or end is dropped: it is media past a cut that
// reaches the item's edge, in a rendition a frame longer than the one the
// cuts were made from. Any other short kept span stays a piece of its own.
// Cuts may be unsorted or overlap. A plan whose cuts cover the whole track
// has no pieces.
func PlanCuts(t Track, cuts []Cut) []Piece {
	segs := TrackSegments(t)
	if len(segs) == 0 {
		return nil
	}
	last := segs[len(segs)-1]
	start, end := segs[0].Start, last.Start+last.Duration

	var spans []Cut // the complement of the cuts within the track
	at := start
	for _, c := range unionCuts(cuts) {
		if c.Start > at {
			spans = append(spans, Cut{at, math.Min(c.Start, end)})
		}
		at = math.Max(at, c.End)
	}
	if at < end {
		spans = append(spans, Cut{at, end})
	}

	var pieces []Piece
	for _, span := range spans {
		atStart, atEnd := span.Start <= start+planTolerance, span.End >= end-planTolerance
		if span.End-span.Start < MinPiece && atStart != atEnd {
			continue
		}
		var ps []Piece
		for i, s := range segs {
			sEnd := s.Start + s.Duration
			a, b := math.Max(span.Start, s.Start), math.Min(span.End, sEnd)
			if b-a < planTolerance {
				continue
			}
			p := Piece{Start: a, End: b, Segment: i}
			if a > s.Start+planTolerance || b < sEnd-planTolerance {
				p.Edge = true
			} else {
				p.Start, p.End = s.Start, sEnd
			}
			ps = append(ps, p)
		}
		pieces = append(pieces, joinShort(ps)...)
	}
	return pieces
}

// joinShort joins each edge piece of one kept span shorter than MinPiece
// to its neighbour, so no edge is too short to hold a frame.
func joinShort(ps []Piece) []Piece {
	for i := 0; i < len(ps) && len(ps) > 1; {
		if !ps[i].Edge || ps[i].End-ps[i].Start >= MinPiece {
			i++
			continue
		}
		lo := i
		if i == len(ps)-1 {
			lo = i - 1
		}
		ps[lo] = Piece{Start: ps[lo].Start, End: ps[lo+1].End, Segment: ps[lo].Segment, Edge: true}
		ps = slices.Delete(ps, lo+1, lo+2)
		i = lo
	}
	return ps
}

// CoarseCuts is the cuts that drop only the segments wholly inside one,
// merged where dropped segments are adjacent. Touching or overlapping cuts
// count as one. Planned with PlanCuts, they leave no edge pieces.
func CoarseCuts(t Track, cuts []Cut) []Cut {
	union := unionCuts(cuts)
	var out []Cut
	prevDropped := false
	for _, s := range TrackSegments(t) {
		sEnd := s.Start + s.Duration
		dropped := slices.ContainsFunc(union, func(c Cut) bool {
			return c.Start <= s.Start+planTolerance && sEnd <= c.End+planTolerance
		})
		switch {
		case dropped && prevDropped:
			out[len(out)-1].End = sEnd
		case dropped:
			out = append(out, Cut{s.Start, sEnd})
		}
		prevDropped = dropped
	}
	return out
}

// Edges are the pieces of a plan that must be encoded, in order; CutEntries
// numbers them the same way.
func Edges(pieces []Piece) []Piece {
	var out []Piece
	for _, p := range pieces {
		if p.Edge {
			out = append(out, p)
		}
	}
	return out
}

// CutEntries turns a plan into playlist entries: a kept whole segment is a
// byte range of fileURI under the track's init; edge piece n is edgeURI(n),
// a byte range under that file's own init, given by edges[n]. edges holds
// one track per edge piece.
//
// An edge entry spans its file's fragments, from the first to the end of the
// last, and lasts as long as its piece. A discontinuity comes before the
// first entry of each kept span after the first: there the timestamps jump
// over a cut. Moving between a track file and an edge file within a span
// only changes the EXT-X-MAP, so every rendition planned with the same cuts
// has its discontinuities at the same presentation times, whatever its
// segment grid. A plan with no pieces has no entries.
//
// It is an error when edges does not hold one track per edge piece, when
// an edge track has no fragments, when edgeURI is nil and there are edge
// pieces, or when a piece names a segment t does not have.
func CutEntries(t Track, pieces []Piece, fileURI string, edgeURI func(n int) string, edges []Track) ([]Entry, error) {
	if want := len(Edges(pieces)); len(edges) != want {
		return nil, fmt.Errorf("hls: %d edge tracks for %d edge pieces", len(edges), want)
	} else if want > 0 && edgeURI == nil {
		return nil, fmt.Errorf("hls: %d edge pieces and no edge URI", want)
	}
	segs := TrackSegments(t)
	init := Init{URI: fileURI, Offset: t.InitOffset, Length: t.InitLength}
	entries := make([]Entry, 0, len(pieces))
	n := 0
	for i, p := range pieces {
		var e Entry
		if p.Edge {
			uri, edge := edgeURI(n), edges[n]
			if len(edge.Fragments) == 0 {
				return nil, fmt.Errorf("hls: edge %d (%s) has no fragments", n, uri)
			}
			n++
			first, last := edge.Fragments[0], edge.Fragments[len(edge.Fragments)-1]
			e = Entry{
				URI: uri, Offset: first.Offset, Length: last.Offset + last.Length - first.Offset, Duration: p.End - p.Start,
				Init: Init{URI: uri, Offset: edge.InitOffset, Length: edge.InitLength},
			}
		} else {
			if p.Segment < 0 || p.Segment >= len(segs) {
				return nil, fmt.Errorf("hls: piece %d names segment %d of %d", i, p.Segment, len(segs))
			}
			s := segs[p.Segment]
			e = Entry{URI: fileURI, Offset: s.Offset, Length: s.Length, Duration: s.Duration, Init: init}
		}
		e.Discontinuity = i > 0 && p.Start-pieces[i-1].End > planTolerance
		entries = append(entries, e)
	}
	return entries, nil
}

// EditID names a cut presentation: the applied cuts, rounded to the
// millisecond, and the segment layout of every rendition it lists, keyed
// "a" (Original), "c" (Clean) and "v" (Video). It is 12 hex characters.
//
// The layouts include byte lengths, the init section and the codecs, so
// re-encoding a track changes the id.
func EditID(applied []Cut, layouts map[string]Track) string {
	h := sha256.New()
	ms := func(x float64) string { return strconv.FormatFloat(math.Round(x*1000)/1000, 'g', -1, 64) }
	// A hash.Hash never returns a write error, so Fprintf's is not checked.
	for _, c := range applied {
		fmt.Fprintf(h, "cut %s %s\n", ms(c.Start), ms(c.End))
	}
	keys := make([]string, 0, len(layouts))
	for k := range layouts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		t := layouts[k]
		fmt.Fprintf(h, "track %q %q %d %d\n", k, t.Codecs, t.InitOffset, t.InitLength)
		for _, s := range TrackSegments(t) {
			fmt.Fprintf(h, "seg %s %s %d %d\n",
				strconv.FormatFloat(s.Start, 'g', -1, 64), strconv.FormatFloat(s.Duration, 'g', -1, 64), s.Offset, s.Length)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
