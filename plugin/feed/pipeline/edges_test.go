package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

// skipItem is a packaged video item (the video.mp4 and audio.m4a fixtures)
// with silence {2.5,4.2} and {7.0,9.6}, whose edges a player wants. At the
// default shortest silence (1 s) the cuts are {2.6,4.1} and {7.1,9.5}; both
// tracks have segments [0,6) and [6,12), so each has four edge pieces.
func skipItem(t *testing.T) (*feed.Store, feed.Space, int64) {
	t.Helper()
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "audio.m4a", "media/1.m4a")
	putFixture(t, space, "video.mp4", "media/1.video.mp4")
	require.NoError(t, s.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	require.NoError(t, s.SetSilenceRanges(id, []feed.Range{
		{Start: 2.5, End: 4.2, Action: "skip"}, {Start: 7.0, End: 9.6, Action: "skip"},
	}))
	wanted, err := s.WantEdges(id)
	require.NoError(t, err)
	require.True(t, wanted)
	return s, space, id
}

// plan is the item's skip plan, as the route computes it.
func plan(t *testing.T, s *feed.Store, space feed.Space, id int64) feed.EdgePlan {
	t.Helper()
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	original := storedTrack(t, space, d.LocalPath)
	var video *hls.Track
	if d.VideoPath != "" {
		v := storedTrack(t, space, d.VideoPath)
		video = &v
	}
	p, err := feed.SkipPlan(s, d, original, nil, video)
	require.NoError(t, err)
	return p
}

func decodeTime(t *testing.T, space feed.Space, name string) float64 {
	t.Helper()
	tfdt, err := space.CachedDecodeTime(name)
	require.NoError(t, err, name)
	return tfdt
}

func cachedTrack(t *testing.T, space feed.Space, name string) hls.Track {
	t.Helper()
	tr, _, err := space.CachedTrack(name)
	require.NoError(t, err, name)
	return tr
}

// cacheNames lists a cache directory.
func cacheNames(t *testing.T, space feed.Space, dir string) []string {
	t.Helper()
	entries, err := space.Cache.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func runEdges(t *testing.T, s *feed.Store, space feed.Space) {
	t.Helper()
	require.NoError(t, pipeline.NewEdges(s, space).RunOnce(context.Background()))
}

func TestEdgesMakesExactPieces(t *testing.T) {
	s, space, id := skipItem(t)
	runEdges(t, s, space)

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.EdgesError)
	p := plan(t, s, space, id)
	assert.Equal(t, []hls.Cut{{Start: 2.6, End: 4.1}, {Start: 7.1, End: 9.5}}, roundCuts(p.Cuts))
	assert.Equal(t, p.Edit, d.EdgesEdit)

	audio, video := hls.Edges(p.Original), hls.Edges(p.Video)
	require.Equal(t, 4, len(audio))
	require.Equal(t, 4, len(video))
	assert.Equal(t, 8, len(cacheNames(t, space, feed.EdgesDir(id)+"/"+p.Edit)))
	frame := 1024.0 / 44100
	for n, piece := range audio {
		tr := cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeOriginal, n))
		assert.Equal(t, hls.Audio, tr.Kind)
		assert.Equal(t, "mp4a.40.2", tr.Codecs)
		first, last := tr.Fragments[0], tr.Fragments[len(tr.Fragments)-1]
		start, end := first.Start, last.Start+last.Duration
		// Whole AAC frames, within a frame of the piece. The encoder's
		// priming frame is dropped: players that keep the first init never
		// skip it, so it would play 23 ms of nothing and overlap what comes
		// before.
		assert.InDelta(t, piece.Start, start, frame, "audio edge %d start", n)
		assert.InDelta(t, piece.End, end, frame, "audio edge %d end", n)
		// It keeps the end that joins its segment's other media: the start
		// after a cut, the end before media that follows without one. The
		// last ends with the track, whose last frame is short.
		switch {
		case n == len(audio)-1:
			assert.InDelta(t, piece.Start, start, 1e-3, "audio edge %d start", n)
		case n%2 == 0:
			assert.InDelta(t, piece.Start, start, 1e-3, "audio edge %d start", n)
			frames := (end - start) / frame
			assert.InDelta(t, math.Round(frames), frames, 1e-3, "audio edge %d: whole frames", n)
		default:
			assert.InDelta(t, piece.End, end, 1e-3, "audio edge %d end", n)
			frames := (end - start) / frame
			assert.InDelta(t, math.Round(frames), frames, 1e-3, "audio edge %d: whole frames", n)
		}
		// Players place fragments by tfdt, not the sidx: it is item time too.
		tfdt := decodeTime(t, space, feed.EdgeName(id, p.Edit, feed.EdgeOriginal, n))
		assert.InDelta(t, first.Start, tfdt, 1e-3, "audio edge %d tfdt", n)
	}
	a0 := cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeOriginal, 0))
	assert.InDelta(t, 0, a0.Fragments[0].Start, 0.05)
	assert.InDelta(t, 2.6, trackDuration(a0), 0.05)
	for n, piece := range video {
		tr := cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeVideo, n))
		assert.Equal(t, hls.Video, tr.Kind)
		assert.Equal(t, "avc1.42C01E", tr.Codecs)
		assert.Equal(t, 160, tr.Width)
		last := tr.Fragments[len(tr.Fragments)-1]
		assert.InDelta(t, piece.Start, tr.Fragments[0].Start, 1.0/24, "video edge %d start", n)
		tfdt := decodeTime(t, space, feed.EdgeName(id, p.Edit, feed.EdgeVideo, n))
		// The source's frames are at k/24 s: the edge keeps their times.
		assert.InDelta(t, math.Ceil(piece.Start*24-1e-6)/24, tfdt, 1e-6, "video edge %d tfdt", n)
		assert.InDelta(t, piece.End, last.Start+last.Duration, 1.0/24, "video edge %d end", n)
	}
	// Each video edge's init carries its source's parameter sets beside its
	// own (id 1), and the edge decodes cleanly on its own.
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	srcTrack := storedTrack(t, space, d.VideoPath)
	srcInit, err := space.TrackInit(d.VideoPath, srcTrack)
	require.NoError(t, err)
	srcSets, err := hls.InitParamSets(srcInit)
	require.NoError(t, err)
	for n := range video {
		name := feed.EdgeName(id, p.Edit, feed.EdgeVideo, n)
		f, err := space.Cache.Open(name)
		require.NoError(t, err)
		b, err := io.ReadAll(f)
		require.NoError(t, errors.Join(err, f.Close()))
		tr, err := hls.ReadTrack(bytes.NewReader(b), int64(len(b)))
		require.NoError(t, err)
		sets, err := hls.InitParamSets(b[tr.InitOffset : tr.InitOffset+tr.InitLength])
		require.NoError(t, err)
		require.Equal(t, 2, len(sets.SPS), name)
		require.Equal(t, 2, len(sets.PPS), name)
		assert.True(t, bytes.Equal(srcSets.SPS[0], sets.SPS[1]), "%s: source SPS", name)
		assert.True(t, bytes.Equal(srcSets.PPS[0], sets.PPS[1]), "%s: source PPS", name)
		local, err := space.Cache.LocalPath(name)
		require.NoError(t, err)
		out, err := exec.Command("ffmpeg", "-nostdin", "-v", "warning", "-i", local, "-f", "null", "-").CombinedOutput()
		require.NoError(t, err, string(out))
		assert.Equal(t, "", string(out), name)
	}
	v1 := cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeVideo, 1))
	assert.InDelta(t, 4.1, v1.Fragments[0].Start, 0.05)
	assert.True(t, v1.Fragments[0].Keyframe)
	// The edge keeps the source's frame times: 4.125 is a frame of the 24 fps
	// source, where a re-timed encode would start at 4.1 + 1/24.
	assert.InDelta(t, 4.125, v1.Fragments[0].Start, 0.001)

	pending, err := s.PendingEdges(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
}

func roundCuts(cuts []hls.Cut) []hls.Cut {
	r := func(x float64) float64 { return math.Round(x*1000) / 1000 }
	out := make([]hls.Cut, len(cuts))
	for i, c := range cuts {
		out[i] = hls.Cut{Start: r(c.Start), End: r(c.End)}
	}
	return out
}

// The clean rendition gets its own edges, planned on its own grid.
func TestEdgesCleanRendition(t *testing.T) {
	s, space, id := skipItem(t)
	putFixture(t, space, "audio.m4a", "media/1.hls-audio.clean.m4a")
	require.NoError(t, s.SaveCleanTrack(id, "media/1.hls-audio.clean.m4a"))
	runEdges(t, s, space)

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.EdgesError)
	original := storedTrack(t, space, d.LocalPath)
	clean := storedTrack(t, space, d.CleanTrackPath)
	video := storedTrack(t, space, d.VideoPath)
	p, err := feed.SkipPlan(s, d, original, &clean, &video)
	require.NoError(t, err)
	assert.Equal(t, p.Edit, d.EdgesEdit)
	assert.Equal(t, 12, len(cacheNames(t, space, feed.EdgesDir(id)+"/"+p.Edit)))
	for n := range hls.Edges(p.Clean) {
		assert.Equal(t, hls.Audio, cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeClean, n)).Kind)
	}
}

func TestEdgesNothingToCut(t *testing.T) {
	s, space, id := skipItem(t)
	require.NoError(t, s.SetSilenceRanges(id, nil))
	runEdges(t, s, space)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesError)
	p := plan(t, s, space, id)
	assert.Equal(t, 0, len(p.Cuts))
	assert.Equal(t, p.Edit, d.EdgesEdit)
	assert.Equal(t, 0, len(cacheNames(t, space, "edges")))
}

func TestEdgesRemovesStale(t *testing.T) {
	s, space, id := skipItem(t)
	runEdges(t, s, space)
	first := plan(t, s, space, id).Edit

	prefs, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	prefs.SkipSilenceMinSec = 2
	require.NoError(t, s.SavePlayerPrefs(prefs))
	runEdges(t, s, space)

	second := plan(t, s, space, id)
	assert.NotEqual(t, first, second.Edit)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, second.Edit, d.EdgesEdit)
	assert.Equal(t, []string{second.Edit}, cacheNames(t, space, feed.EdgesDir(id)))
	// Only {7.0,9.6} is 2 s or longer.
	assert.Equal(t, []hls.Cut{{Start: 7.1, End: 9.5}}, roundCuts(second.Cuts))
}

// A video that is not H.264 gets no skip at all, so no edges, not even audio.
func TestEdgesNonAVCVideo(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "audio.m4a", "media/1.m4a")
	b, err := os.ReadFile("../testdata/media/video.mp4")
	require.NoError(t, err)
	i := bytes.Index(b, []byte("avc1"))
	require.True(t, i > 0, "no avc1 sample entry")
	copy(b[i:], "vp09")
	_, err = space.Own.Put("media/1.video.mp4", bytes.NewReader(b))
	require.NoError(t, err)
	require.NoError(t, s.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	require.NoError(t, s.SetSilenceRanges(id, []feed.Range{{Start: 2.5, End: 4.2, Action: "skip"}}))
	_, err = s.WantEdges(id)
	require.NoError(t, err)

	runEdges(t, s, space)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.EdgesError)
	p := plan(t, s, space, id)
	assert.True(t, p.Unsupported)
	assert.Equal(t, 0, len(p.Video))
	assert.Equal(t, p.Edit, d.EdgesEdit)
	assert.Equal(t, 0, len(cacheNames(t, space, "edges")))
}

func TestEdgesFailureNotRetried(t *testing.T) {
	s, space, id := skipItem(t)
	require.NoError(t, space.Own.Remove("media/1.m4a"))
	runEdges(t, s, space)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.NotEqual(t, "", d.EdgesError)
	pending, err := s.PendingEdges(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
	assert.Equal(t, 0, len(cacheNames(t, space, "edges")))
}

// An encode that fails part way is recorded against its edit and leaves
// nothing behind.
func TestEdgesEncodeFailure(t *testing.T) {
	s, space, id := skipItem(t)
	// A track whose index is intact but whose media is not: the encode fails.
	b, err := os.ReadFile("../testdata/media/video.mp4")
	require.NoError(t, err)
	tr, err := hls.ReadTrack(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	first := tr.Fragments[0]
	for i := first.Offset + 200; i < int64(len(b)); i++ {
		b[i] = 0
	}
	_, err = space.Own.Put("media/1.video.mp4", bytes.NewReader(b))
	require.NoError(t, err)

	runEdges(t, s, space)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.NotEqual(t, "", d.EdgesError)
	assert.Equal(t, plan(t, s, space, id).Edit, d.EdgesEdit)
	assert.Equal(t, 0, len(cacheNames(t, space, feed.EdgesDir(id))))
}

// Inputs that change while the edges are encoded make them stale: they are
// not recorded, and the next run makes the new plan's.
func TestEdgesInputsChangeDuringEncode(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, s *feed.Store, id int64){
		"silence": func(t *testing.T, s *feed.Store, id int64) {
			require.NoError(t, s.SetSilenceRanges(id, []feed.Range{{Start: 7.0, End: 9.6, Action: "skip"}}))
		},
		"pref": func(t *testing.T, s *feed.Store, id int64) {
			prefs, err := s.LoadPlayerPrefs()
			require.NoError(t, err)
			prefs.SkipSilenceMinSec = 2
			require.NoError(t, s.SavePlayerPrefs(prefs))
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, space, id := skipItem(t)
			stale := plan(t, s, space, id).Edit
			w := pipeline.NewEdges(s, space)
			w.SetAfterEncode(func() { change(t, s, id) })
			require.NoError(t, w.RunOnce(context.Background()))
			d, err := s.GetDownload(id)
			require.NoError(t, err)
			assert.Equal(t, "", d.EdgesEdit, "stale edges recorded")
			assert.Equal(t, "", d.EdgesError)
			assert.Equal(t, 0, len(cacheNames(t, space, feed.EdgesDir(id))))

			runEdges(t, s, space)
			fresh := plan(t, s, space, id).Edit
			assert.NotEqual(t, stale, fresh)
			d, err = s.GetDownload(id)
			require.NoError(t, err)
			assert.Equal(t, fresh, d.EdgesEdit)
		})
	}
}

// Collect removes the edges of items no longer packaged, and every edit
// directory but the one an item's edges are recorded for.
func TestCollectRemovesEdgesOfGoneItems(t *testing.T) {
	s, space, id := skipItem(t)
	runEdges(t, s, space)
	edit := plan(t, s, space, id).Edit
	put := func(name string) {
		t.Helper()
		_, err := space.Cache.Put(name, strings.NewReader("x"))
		require.NoError(t, err)
	}
	gone := strconv.FormatInt(id+100, 10)
	put("edges/" + gone + "/abcdefabcdef/a-0.mp4")
	put(feed.EdgesDir(id) + "/000000000000/a-0.mp4")

	require.NoError(t, pipeline.Collect(s, space))
	assert.Equal(t, []string{strconv.FormatInt(id, 10)}, cacheNames(t, space, "edges"))
	assert.Equal(t, []string{edit}, cacheNames(t, space, feed.EdgesDir(id)))
	assert.Equal(t, 8, len(cacheNames(t, space, feed.EdgesDir(id)+"/"+edit)))

	// Once its download is gone, so are its edges.
	require.NoError(t, s.DeleteItem(id))
	require.NoError(t, pipeline.Collect(s, space))
	assert.Equal(t, 0, len(cacheNames(t, space, "edges")))
}

// A cache with no edges is nothing to collect.
func TestCollectWithoutEdges(t *testing.T) {
	s, space := newPipeline(t)
	require.NoError(t, pipeline.Collect(s, space))
}

// Tracks that start 1.5 s in are seeked from their own start, and their
// edges keep item time.
func TestEdgesOffsetTracks(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "audio-offset.m4a", "media/1.m4a")
	putFixture(t, space, "video-offset.mp4", "media/1.video.mp4")
	require.NoError(t, s.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	require.NoError(t, s.SetSilenceRanges(id, []feed.Range{{Start: 4.0, End: 5.7, Action: "skip"}}))
	_, err := s.WantEdges(id)
	require.NoError(t, err)

	runEdges(t, s, space)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.EdgesError)
	p := plan(t, s, space, id)
	require.Equal(t, p.Edit, d.EdgesEdit)
	v := hls.Edges(p.Video)
	require.Equal(t, 2, len(v))
	tr := cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeVideo, 1))
	assert.InDelta(t, 5.6, tr.Fragments[0].Start, 1.0/24)
	assert.InDelta(t, tr.Fragments[0].Start, decodeTime(t, space, feed.EdgeName(id, p.Edit, feed.EdgeVideo, 1)), 1e-6)
	a := hls.Edges(p.Original)
	require.Equal(t, 2, len(a))
	tr = cachedTrack(t, space, feed.EdgeName(id, p.Edit, feed.EdgeOriginal, 0))
	assert.InDelta(t, a[0].Start, tr.Fragments[0].Start, 0.05)
}

// The race check failing is recorded too, so the item is not encoded again
// every tick.
func TestEdgesRecheckFailure(t *testing.T) {
	s, space, id := skipItem(t)
	w := pipeline.NewEdges(s, space)
	w.SetAfterEncode(func() { require.NoError(t, space.Own.Remove("media/1.m4a")) })
	require.NoError(t, w.RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Contains(t, d.EdgesError, "original track")
	pending, err := s.PendingEdges(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
	assert.Equal(t, 0, len(cacheNames(t, space, feed.EdgesDir(id))))
}

// skipItems makes n packaged audio items with silence {2.5,4.2}, wanted.
func skipItems(t *testing.T, n int) (*feed.Store, feed.Space, []int64) {
	t.Helper()
	needFFmpeg(t)
	s, space := newPipeline(t)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://example.com/feed"})
	require.NoError(t, err)
	var ids []int64
	for i := range n {
		id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g" + strconv.Itoa(i), Type: "audio", MediaURL: "https://example.com/ep"})
		require.NoError(t, err)
		require.NoError(t, s.EnqueueDownload(id))
		name := "media/" + strconv.FormatInt(id, 10) + ".m4a"
		putFixture(t, space, "audio.m4a", name)
		require.NoError(t, s.MarkDownloadReady(id, name, 1))
		require.NoError(t, s.SetSilenceRanges(id, []feed.Range{{Start: 2.5, End: 4.2, Action: "skip"}}))
		ids = append(ids, id)
	}
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	_, err = s.WantEdges(ids...)
	require.NoError(t, err)
	return s, space, ids
}

// One run takes only a few items, so it cannot hold up every other user.
func TestEdgesRunTakesFewItems(t *testing.T) {
	s, space, ids := skipItems(t, 3)
	runEdges(t, s, space)
	pending, err := s.PendingEdges(10)
	require.NoError(t, err)
	assert.Equal(t, []int64{ids[2]}, pending)
	runEdges(t, s, space)
	pending, err = s.PendingEdges(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
}

// A run out of time stops between edges; the next run keeps what it made,
// though Collect runs in between (every tick runs it before the worker).
func TestEdgesBudgetResumes(t *testing.T) {
	s, space, ids := skipItems(t, 1)
	id := ids[0]
	w := pipeline.NewEdges(s, space)
	w.SetBudget(2, time.Nanosecond)
	require.NoError(t, w.RunOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
	assert.Equal(t, "", d.EdgesError)
	p := plan(t, s, space, id)
	require.Equal(t, 2, len(hls.Edges(p.Original)))
	first := feed.EdgeName(id, p.Edit, feed.EdgeOriginal, 0)
	assert.Equal(t, []string{"a-0.mp4"}, cacheNames(t, space, feed.EdgesDir(id)+"/"+p.Edit))
	made, err := space.Cache.Stat(first)
	require.NoError(t, err)

	require.NoError(t, pipeline.Collect(s, space))
	runEdges(t, s, space)
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, p.Edit, d.EdgesEdit)
	assert.Equal(t, 2, len(cacheNames(t, space, feed.EdgesDir(id)+"/"+p.Edit)))
	again, err := space.Cache.Stat(first)
	require.NoError(t, err)
	assert.Equal(t, made.ModTime(), again.ModTime(), "edge 0 made again")
	assert.True(t, os.SameFile(made, again), "edge 0 made again")
}

// A run cancelled part way keeps the edges it made, for the next run.
func TestEdgesCancelKeepsMade(t *testing.T) {
	s, space, ids := skipItems(t, 1)
	id := ids[0]
	p := plan(t, s, space, id)
	first := feed.EdgeName(id, p.Edit, feed.EdgeOriginal, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := pipeline.NewEdges(s, space)
	w.SetAfterEdge(cancel)
	err := w.RunOnce(ctx)
	assert.True(t, errors.Is(err, context.Canceled), "run: %v", err)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.EdgesEdit)
	assert.Equal(t, "", d.EdgesError)
	made, err := space.Cache.Stat(first)
	require.NoError(t, err, "edge 0 removed")

	require.NoError(t, pipeline.Collect(s, space))
	runEdges(t, s, space)
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, p.Edit, d.EdgesEdit)
	again, err := space.Cache.Stat(first)
	require.NoError(t, err)
	assert.True(t, os.SameFile(made, again), "edge 0 made again")
}
