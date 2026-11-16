package feed

import (
	"encoding/hex"
	"fmt"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// EdgePlan is what a skip presentation of an item needs: the cuts it
// applies, each rendition planned against its own segment grid, and the
// edit id that names them. It is shared by the edges worker, which encodes
// the edge pieces, and the HLS route, which lists them.
type EdgePlan struct {
	Edit                   string
	Cuts                   []hls.Cut
	Original, Clean, Video []hls.Piece
	// Unsupported is a presentation with a rendition whose edges cannot be
	// made (see EdgesSupported): it is not skipped and has no pieces.
	Unsupported bool
}

// Rendition keys of EditID and EdgeName.
const (
	EdgeOriginal = "a"
	EdgeClean    = "c"
	EdgeVideo    = "v"
)

// SkipPlan plans the skip presentation of d's renditions: original, and clean
// and video when the presentation lists them (nil otherwise). The cuts are
// d's silence ranges through the user's shortest-silence pref, bounded by
// the end of original. A plan with no cuts still has an edit id; the route
// serves it unskipped. So it does a plan with a rendition whose edges
// cannot be made (Unsupported): cutting the others alone would put the
// renditions out of step.
func SkipPlan(store *Store, d Download, original hls.Track, clean, video *hls.Track) (EdgePlan, error) {
	ranges, err := d.silenceRanges()
	if err != nil {
		return EdgePlan{}, err
	}
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		return EdgePlan{}, err
	}
	var silences []hls.Cut
	for _, r := range ranges {
		if r.Action == "skip" {
			silences = append(silences, hls.Cut{Start: r.Start, End: r.End})
		}
	}
	duration := 0.0
	if n := len(original.Fragments); n > 0 {
		last := original.Fragments[n-1]
		duration = last.Start + last.Duration
	}
	p := EdgePlan{Cuts: hls.EffectiveCuts(silences, prefs.SkipSilenceMinSec, duration)}
	layouts := map[string]hls.Track{EdgeOriginal: original}
	supported := EdgesSupported(original)
	if clean != nil {
		layouts[EdgeClean] = *clean
		supported = supported && EdgesSupported(*clean)
	}
	if video != nil {
		layouts[EdgeVideo] = *video
		supported = supported && EdgesSupported(*video)
	}
	p.Edit = hls.EditID(p.Cuts, layouts)
	if !supported {
		p.Unsupported = true
		return p, nil
	}
	p.Original = hls.PlanCuts(original, p.Cuts)
	if clean != nil {
		p.Clean = hls.PlanCuts(*clean, p.Cuts)
	}
	if video != nil {
		p.Video = hls.PlanCuts(*video, p.Cuts)
	}
	return p, nil
}

// EdgesSupported reports whether edges of t can be made that decode under
// t's own init section: AAC-LC audio (ffmpeg's encoder makes no other), and
// H.264 in a profile x264 can match (X264Profile).
func EdgesSupported(t hls.Track) bool {
	if t.Kind == hls.Audio {
		return t.Codecs == "mp4a.40.2"
	}
	_, _, ok := X264Profile(t.Codecs)
	return ok
}

// X264Profile is the x264 profile and level of an H.264 codecs string
// (avc1.PPCCLL or avc3.): baseline, main or high, and the level as "3.1".
// Other profiles (High 10, 4:2:2, 4:4:4) are not matched.
func X264Profile(codecs string) (profile, level string, ok bool) {
	rest, found := strings.CutPrefix(codecs, "avc1.")
	if !found {
		rest, found = strings.CutPrefix(codecs, "avc3.")
	}
	if !found || len(rest) != 6 {
		return "", "", false
	}
	b, err := hex.DecodeString(rest)
	if err != nil {
		return "", "", false
	}
	switch b[0] {
	case 0x42:
		profile = "baseline"
	case 0x4D:
		profile = "main"
	case 0x64:
		profile = "high"
	default:
		return "", "", false
	}
	if b[2] < 10 {
		return "", "", false
	}
	return profile, fmt.Sprintf("%d.%d", b[2]/10, b[2]%10), true
}

// EdgesDir is the cache directory holding an item's edges, one directory
// per edit id.
func EdgesDir(item int64) string {
	return fmt.Sprintf("edges/%d", item)
}

// EdgeName is the cache name of edge piece n of a rendition (EdgeOriginal,
// EdgeClean or EdgeVideo) of an edit.
func EdgeName(item int64, edit, track string, n int) string {
	return fmt.Sprintf("%s/%s/%s-%d.mp4", EdgesDir(item), edit, track, n)
}
