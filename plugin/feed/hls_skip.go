package feed

import (
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
	// VideoUnsupported is a video that is not H.264: its edges cannot be
	// made, so the presentation is not skipped and Video is nil.
	VideoUnsupported bool
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
// serves it unskipped.
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
	p.Original = hls.PlanCuts(original, p.Cuts)
	if clean != nil {
		layouts[EdgeClean] = *clean
		p.Clean = hls.PlanCuts(*clean, p.Cuts)
	}
	if video != nil {
		layouts[EdgeVideo] = *video
		if IsAVC(video.Codecs) {
			p.Video = hls.PlanCuts(*video, p.Cuts)
		} else {
			p.VideoUnsupported = true
		}
	}
	p.Edit = hls.EditID(p.Cuts, layouts)
	return p, nil
}

// IsAVC reports whether codecs (RFC 6381) names H.264, the only video whose
// edges are made.
func IsAVC(codecs string) bool {
	return strings.HasPrefix(codecs, "avc1.") || strings.HasPrefix(codecs, "avc3.")
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
