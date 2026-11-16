package feed_test

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

func TestX264Profile(t *testing.T) {
	for codecs, want := range map[string][2]string{
		"avc1.42C01E": {"baseline", "3.0"},
		"avc1.4D401F": {"main", "3.1"},
		"avc1.640028": {"high", "4.0"},
		"avc3.640033": {"high", "5.1"},
	} {
		p, l, ok := feed.X264Profile(codecs)
		assert.True(t, ok, codecs)
		assert.Equal(t, want[0], p, codecs)
		assert.Equal(t, want[1], l, codecs)
	}
	for _, codecs := range []string{"vp09.00.40.08", "", "avc1.6E0028", "avc1.7A0028", "avc1.F40028", "avc1.64", "avc1.zz0028"} {
		_, _, ok := feed.X264Profile(codecs)
		assert.False(t, ok, codecs)
	}
}

// twoSegments is a 12 s track of two 6 s segments.
func twoSegments(kind hls.Kind, codecs string) hls.Track {
	return hls.Track{Kind: kind, Codecs: codecs, InitLength: 100, Fragments: []hls.Fragment{
		{Start: 0, Duration: 6, Offset: 100, Length: 1000, Keyframe: true},
		{Start: 6, Duration: 6, Offset: 1100, Length: 1000, Keyframe: true},
	}}
}

// Edges are made only where they decode under the source's init: AAC-LC
// audio, and H.264 that x264 can match. Anything else leaves the whole
// presentation unskipped, so audio and video never disagree.
func TestSkipPlanSupport(t *testing.T) {
	store, id := readyItem(t)
	d := feed.Download{ItemID: id, SilenceRanges: `[{"start":2.5,"end":4.2,"action":"skip"}]`}
	lc, he := twoSegments(hls.Audio, "mp4a.40.2"), twoSegments(hls.Audio, "mp4a.40.5")
	for name, c := range map[string]struct {
		original     hls.Track
		clean, video *hls.Track
		unsupported  bool
	}{
		"aac-lc":         {original: lc},
		"he-aac":         {original: he, unsupported: true},
		"he-aac clean":   {original: lc, clean: &he, unsupported: true},
		"baseline video": {original: lc, video: ptr(twoSegments(hls.Video, "avc1.42C01E"))},
		"high 10 video":  {original: lc, video: ptr(twoSegments(hls.Video, "avc1.6E0028")), unsupported: true},
		"vp9 video":      {original: lc, video: ptr(twoSegments(hls.Video, "")), unsupported: true},
	} {
		p, err := feed.SkipPlan(store, d, c.original, c.clean, c.video)
		require.NoError(t, err)
		assert.Equal(t, c.unsupported, p.Unsupported, name)
		assert.Equal(t, 1, len(p.Cuts), name)
		assert.NotEqual(t, "", p.Edit, name)
		if c.unsupported {
			assert.Equal(t, 0, len(p.Original)+len(p.Clean)+len(p.Video), "%s: pieces of an unsupported plan", name)
		} else {
			// {2.6,4.1} inside [0,6): edges [0,2.6) and [4.1,6).
			assert.Equal(t, 2, len(hls.Edges(p.Original)), name)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// needFFmpeg skips a test that runs ffmpeg where it is not installed, and
// fails it in CI.
func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("ffmpeg is not installed: %v", err)
		}
		t.Skipf("ffmpeg is not installed: %v", err)
	}
}

// skipFixture is the silence of the skip tests: at the default shortest
// silence (1 s) the cuts are {2.6,4.1} and {7.1,9.5}. The fixtures' tracks
// have segments [0,6) and [6,12), so each rendition has four edge pieces.
var skipFixture = []feed.Range{{Start: 2.5, End: 4.2, Action: "skip"}, {Start: 7.0, End: 9.6, Action: "skip"}}

// skipItem is packagedItem with silence ranges, and its skip master URL.
func skipItem(t *testing.T, env *cogtest.Env, username string, withVideo bool, silence []feed.Range) (int64, string) {
	t.Helper()
	id := packagedItem(t, env, username, withVideo)
	require.NoError(t, feedStore(t, env, username).SetSilenceRanges(id, silence))
	return id, skipMaster(t, env, username, id)
}

func skipMaster(t *testing.T, env *cogtest.Env, username string, id int64) string {
	t.Helper()
	u, err := feedPlugin(t, env).HLSSkipMasterURL(feedStore(t, env, username), username, id)
	require.NoError(t, err)
	return u
}

// runEdges runs the edges worker once for the user.
func runEdges(t *testing.T, env *cogtest.Env, username string) {
	t.Helper()
	needFFmpeg(t)
	space, err := feedPlugin(t, env).Space(username)
	require.NoError(t, err)
	require.NoError(t, pipeline.NewEdges(feedStore(t, env, username), space).RunOnce(context.Background()))
}

var (
	sessionEdit = regexp.MustCompile(`DATA-ID="us\.duckfam\.cog\.edits",VALUE="([0-9a-f]{12})"`)
	mediaURI    = regexp.MustCompile(`URI="([^"]+\.m3u8\?[^"]+)"|\n([a-z]+\.m3u8\?[^\n]+)`)
	extinf      = regexp.MustCompile(`#EXTINF:([0-9.]+),`)
)

// masterEdit is the edit list a skip master applies, or "".
func masterEdit(body string) string {
	if m := sessionEdit.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// child is the URL of the file a master names: a URI relative to it.
func child(master, uri string) string {
	return master[:strings.LastIndex(master[:strings.Index(master, "?")], "/")+1] + uri
}

// masterURI is the URI the master names for file (e.g. "video.m3u8").
func masterURI(t *testing.T, body, file string) string {
	t.Helper()
	for _, m := range mediaURI.FindAllStringSubmatch(body, -1) {
		u := m[1] + m[2]
		if strings.HasPrefix(u, file+"?") {
			return u
		}
	}
	t.Fatalf("no %s in\n%s", file, body)
	return ""
}

func extinfSum(t *testing.T, pl string) float64 {
	t.Helper()
	sum := 0.0
	for _, m := range extinf.FindAllStringSubmatch(pl, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		require.NoError(t, err, m[0])
		sum += v
	}
	return sum
}

// playlistURIs are the media URIs of a playlist: its entries and maps.
func playlistURIs(pl string) []string {
	var out []string
	for _, line := range strings.Split(pl, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func download(t *testing.T, env *cogtest.Env, username string, id int64) feed.Download {
	t.Helper()
	d, err := feedStore(t, env, username).GetDownload(id)
	require.NoError(t, err)
	return d
}

func TestHLSSkipVideoPendingThenFull(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", true, skipFixture)

	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `DATA-ID="us.duckfam.cog.skip",VALUE="pending"`), body)
	assert.Equal(t, "", masterEdit(body))
	assert.False(t, strings.Contains(body, "edit="), body)
	pl := env.Get(t, child(skip, masterURI(t, body, "video.m3u8"))).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(pl, "#EXT-X-DISCONTINUITY"), pl)
	assert.True(t, download(t, env, "alice", id).EdgesWanted)

	runEdges(t, env, "alice")
	body = env.Get(t, skip).Status(http.StatusOK).ReadBody()
	edit := masterEdit(body)
	require.NotEqual(t, "", edit, body)
	assert.False(t, strings.Contains(body, "us.duckfam.cog.skip"), body)
	plain := masterURL(t, env, "alice", id)
	assert.Equal(t, edit, download(t, env, "alice", id).EdgesEdit)
	for _, file := range []string{"video.m3u8", "audio.m3u8"} {
		uri := masterURI(t, body, file)
		assert.True(t, strings.Contains(uri, "edit="+edit), uri)
		pl := env.Get(t, child(skip, uri)).Status(http.StatusOK).ReadBody()
		assert.Equal(t, 4, strings.Count(pl, "#EXTINF:"), pl)
		// Discontinuities only where a cut was (R3); every entry is an edge.
		assert.Equal(t, 2, strings.Count(pl, "#EXT-X-DISCONTINUITY"), pl)
		assert.Equal(t, 4, strings.Count(pl, "#EXT-X-MAP:URI=\"edge-"+edit+"-"), pl)
		// The cuts take 3.9 s; AAC frames make the audio track 12.023 s.
		whole := extinfSum(t, env.Get(t, sibling(plain, file)).Status(http.StatusOK).ReadBody())
		assert.InDelta(t, whole-1.5-2.4, extinfSum(t, pl), 0.01, pl)
		mime, key := "video/mp4", "-v-"
		if file == "audio.m3u8" {
			mime, key = "audio/mp4", "-a-"
		}
		for _, u := range playlistURIs(pl) {
			assert.True(t, strings.HasPrefix(u, "edge-"+edit+key), u)
			assert.True(t, strings.Contains(u, "edit="+edit), u)
			r := env.Get(t, child(skip, u), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
			assert.Equal(t, mime, r.Header.Get("Content-Type"))
			assert.Equal(t, "private, max-age=86400", r.Header.Get("Cache-Control"))
		}
	}
}

// An audio-only item plays a coarse skip before its edges are made: whole
// segments inside a cut are dropped, and nothing is encoded.
func TestHLSSkipAudioCoarse(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	// {5.6,12} at 1 s: the segment [6,12) is wholly inside it.
	id, skip := skipItem(t, env, "alice", false, []feed.Range{{Start: 5.5, End: 13, Action: "skip"}})
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	edit := masterEdit(body)
	require.NotEqual(t, "", edit, body)
	assert.True(t, download(t, env, "alice", id).EdgesWanted)
	plain := env.Get(t, sibling(masterURL(t, env, "alice", id), "audio.m3u8")).Status(http.StatusOK).ReadBody()
	pl := env.Get(t, child(skip, masterURI(t, body, "audio.m3u8"))).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(pl, "edge-"), pl)
	assert.Equal(t, 1, strings.Count(pl, "#EXTINF:"), pl)
	assert.InDelta(t, extinfSum(t, plain)/2, extinfSum(t, pl), 0.05, pl)
	assert.Equal(t, 0, strings.Count(pl, "#EXT-X-DISCONTINUITY"), pl)
	cuts, _, err := feedStore(t, env, "alice").EditCuts(id, edit)
	require.NoError(t, err)
	require.Equal(t, 1, len(cuts))
	assert.InDelta(t, 12, cuts[0].End, 0.05)
}

// Coarse cuts are per segment grid, so a coarse presentation lists only
// the default audio rendition (R7).
func TestHLSSkipCoarseListsDefaultRenditionOnly(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", false)
	require.NoError(t, feedStore(t, env, "alice").SetSilenceRanges(id, []feed.Range{{Start: 5.5, End: 13, Action: "skip"}}))
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	assert.False(t, strings.Contains(body, `NAME="Original"`), body)
	assert.False(t, strings.Contains(body, "audio.m3u8"), body)
	uri := masterURI(t, body, "clean.m3u8")
	env.Get(t, child(skip, uri)).Status(http.StatusOK).Contains("clean.m4a?")
	env.Get(t, child(skip, strings.Replace(uri, "clean.m3u8", "audio.m3u8", 1))).Status(http.StatusNotFound)
}

func TestHLSSkipNoRanges(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", false, nil)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "SESSION-DATA"), body)
	assert.False(t, strings.Contains(body, "edit="), body)
	assert.False(t, download(t, env, "alice", id).EdgesWanted)
	pl := env.Get(t, child(skip, masterURI(t, body, "audio.m3u8"))).Status(http.StatusOK).ReadBody()
	plain := env.Get(t, sibling(masterURL(t, env, "alice", id), "audio.m3u8")).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(pl, "DISCONTINUITY"), pl)
	strip := regexp.MustCompile(`\?[^"\n]*`)
	assert.Equal(t, strip.ReplaceAllString(plain, ""), strip.ReplaceAllString(pl, ""))
}

func TestHLSSkipSilencePending(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "SESSION-DATA"), body)
	assert.False(t, strings.Contains(body, "edit="), body)
	assert.False(t, download(t, env, "alice", id).EdgesWanted)
}

func TestHLSSkipAfterPrefChange(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	_, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	oldVideo := child(skip, masterURI(t, body, "video.m3u8"))
	oldEdge := child(skip, playlistURIs(env.Get(t, oldVideo).Status(http.StatusOK).ReadBody())[0])
	env.Get(t, oldEdge).Status(http.StatusOK)

	// At 2 s only {7.0,9.6} is cut: a new plan, whose edges are not made.
	store := feedStore(t, env, "alice")
	prefs, err := store.LoadPlayerPrefs()
	require.NoError(t, err)
	prefs.SkipSilenceMinSec = 2
	require.NoError(t, store.SavePlayerPrefs(prefs))
	body = env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `VALUE="pending"`), body)
	env.Get(t, oldVideo).Status(http.StatusNotFound)
	env.Get(t, oldEdge).Status(http.StatusNotFound)
}

func TestHLSSkipSubtitles(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItem(t, env, "alice", false, testVTT)
	// {2.6,4.1} and {7.1,9.5}: "Hello there" (3–5) starts inside a cut.
	require.NoError(t, feedStore(t, env, "alice").SetSilenceRanges(id, skipFixture))
	skip := skipMaster(t, env, "alice", id)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	edit := masterEdit(body)
	require.NotEqual(t, "", edit, body)
	audio := env.Get(t, child(skip, masterURI(t, body, "audio.m3u8"))).Status(http.StatusOK).ReadBody()
	subs := env.Get(t, child(skip, masterURI(t, body, "subs.m3u8"))).Status(http.StatusOK).ReadBody()
	assert.Equal(t, strings.Count(audio, "#EXTINF:"), strings.Count(subs, "#EXTINF:"), subs)
	discont := func(pl string) []int {
		var at []int
		n := 0
		for _, line := range strings.Split(pl, "\n") {
			switch {
			case line == "#EXT-X-DISCONTINUITY":
				at = append(at, n)
			case strings.HasPrefix(line, "#EXTINF:"):
				n++
			}
		}
		return at
	}
	assert.Equal(t, discont(audio), discont(subs))
	assert.InDelta(t, extinfSum(t, audio), extinfSum(t, subs), 0.01)
	vtts := playlistURIs(subs)
	require.Equal(t, 4, len(vtts))
	for _, u := range vtts {
		assert.True(t, strings.Contains(u, "edit="+edit), u)
	}
	seg0 := env.Get(t, child(skip, vtts[0])).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(seg0, "Hello there"), seg0)
	// Piece 1 is [4.1,6): the cue is clipped to it, in caption time.
	seg1 := env.Get(t, child(skip, vtts[1])).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(seg1, "X-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000"), seg1)
	assert.True(t, strings.Contains(seg1, "00:00:04.100 --> 00:00:05.000\nHello there\n"), seg1)
	env.Get(t, child(skip, strings.Replace(vtts[0], "subs-0.vtt", "subs-4.vtt", 1))).Status(http.StatusNotFound)
}

// Captions of a cleaned item with no profanity plugin are withheld under
// skip too (R9).
func TestHLSSkipCaptionsWithheld(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", false)
	store := feedStore(t, env, "alice")
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	name := "captions/" + strconv.FormatInt(id, 10) + ".vtt"
	_, err = space.Own.Put(name, strings.NewReader(testVTT))
	require.NoError(t, err)
	require.NoError(t, store.SetCaptionsPath(id, name))
	require.NoError(t, store.SetSilenceRanges(id, []feed.Range{{Start: 5.5, End: 13, Action: "skip"}}))
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	edit := masterEdit(body)
	require.NotEqual(t, "", edit, body)
	assert.False(t, strings.Contains(body, "SUBTITLES"), body)
	env.Get(t, sibling(skip, "subs.m3u8")+"&edit="+edit).Status(http.StatusNotFound)
	env.Get(t, sibling(skip, "subs-0.vtt")+"&edit="+edit).Status(http.StatusNotFound)
	env.Get(t, sibling(skip, "subs.m3u8")).Status(http.StatusNotFound)
}

func TestHLSSkipRecordsEdit(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	edit := masterEdit(env.Get(t, skip).Status(http.StatusOK).ReadBody())
	require.NotEqual(t, "", edit)
	cuts, _, err := feedStore(t, env, "alice").EditCuts(id, edit)
	require.NoError(t, err)
	require.Equal(t, 2, len(cuts))
	for i, want := range []hls.Cut{{Start: 2.6, End: 4.1}, {Start: 7.1, End: 9.5}} {
		assert.InDelta(t, want.Start, cuts[i].Start, 1e-6)
		assert.InDelta(t, want.End, cuts[i].End, 1e-6)
	}
}

func TestHLSSkipUnsupportedVideo(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", true, skipFixture)
	b, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	i := bytes.Index(b, []byte("avc1"))
	require.True(t, i > 0)
	copy(b[i:], "vp09")
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	_, err = space.Own.Put(download(t, env, "alice", id).VideoPath, bytes.NewReader(b))
	require.NoError(t, err)

	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `DATA-ID="us.duckfam.cog.skip",VALUE="unsupported"`), body)
	assert.Equal(t, "", masterEdit(body))
	assert.False(t, strings.Contains(body, "edit="), body)
	assert.False(t, download(t, env, "alice", id).EdgesWanted)
	pl := env.Get(t, child(skip, masterURI(t, body, "video.m3u8"))).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(pl, "DISCONTINUITY"), pl)
}

// Cuts that fall on segment boundaries need no edges: the plan is full at
// once, and no worker is asked for (R17).
func TestHLSSkipNoEdgePieces(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", false)
	// A silence from 0.1 s before the second segment to past the end cuts
	// that segment exactly.
	plain := env.Get(t, sibling(masterURL(t, env, "alice", id), "audio.m3u8")).Status(http.StatusOK).ReadBody()
	first, err := strconv.ParseFloat(extinf.FindStringSubmatch(plain)[1], 64)
	require.NoError(t, err)
	require.NoError(t, feedStore(t, env, "alice").SetSilenceRanges(id, []feed.Range{{Start: first - 0.1, End: 13, Action: "skip"}}))
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	assert.False(t, strings.Contains(body, "us.duckfam.cog.skip"), body)
	assert.False(t, download(t, env, "alice", id).EdgesWanted)
	pl := env.Get(t, child(skip, masterURI(t, body, "audio.m3u8"))).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 1, strings.Count(pl, "#EXTINF:"), pl)
	assert.False(t, strings.Contains(pl, "edge-"), pl)
}

// A skip request names the plan it is for; any other edit list, and any
// edge not in it, is not found.
func TestHLSSkipRejectsOtherPlans(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	_, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	edit := masterEdit(body)
	require.NotEqual(t, "", edit)
	uri := masterURI(t, body, "video.m3u8")
	env.Get(t, child(skip, strings.Replace(uri, "edit="+edit, "edit=abcdef012345", 1))).Status(http.StatusNotFound)
	edge := playlistURIs(env.Get(t, child(skip, uri)).Status(http.StatusOK).ReadBody())[0]
	env.Get(t, child(skip, edge)).Status(http.StatusOK)
	for _, bad := range []string{
		strings.Replace(edge, "-v-0.mp4", "-v-4.mp4", 1),            // not in the plan
		strings.Replace(edge, "-v-0.mp4", "-x-0.mp4", 1),            // no such rendition
		strings.Replace(edge, "-v-0.mp4", "-c-0.mp4", 1),            // no clean track
		strings.Replace(edge, "edge-"+edit, "edge-abcdef012345", 1), // file and query disagree
		strings.Replace(edge, "edit="+edit, "edit=abcdef012345", 1),
	} {
		env.Get(t, child(skip, bad)).Status(http.StatusNotFound)
	}
}

// A cache that lost the edges sends the item back to the worker (R13).
func TestHLSSkipSelfHealsMissingEdge(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	edit := masterEdit(env.Get(t, skip).Status(http.StatusOK).ReadBody())
	require.NotEqual(t, "", edit)
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	// The cache dropped the edit's edges.
	require.NoError(t, space.Cache.RemoveAll(feed.EdgesDir(id)+"/"+edit))

	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `VALUE="pending"`), body)
	d := download(t, env, "alice", id)
	assert.Equal(t, "", d.EdgesEdit)
	assert.True(t, d.EdgesWanted)
	runEdges(t, env, "alice")
	assert.Equal(t, edit, masterEdit(env.Get(t, skip).Status(http.StatusOK).ReadBody()))
}

// Edges that failed for the current plan are not retried by the route:
// the item plays as pending, and the error stays.
func TestHLSSkipEdgesErrorNoLoop(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	edit := masterEdit(env.Get(t, skip).Status(http.StatusOK).ReadBody())
	require.NotEqual(t, "", edit)
	store := feedStore(t, env, "alice")
	require.NoError(t, store.MarkEdgesError(id, edit, "ffmpeg: boom"))
	for range 2 {
		body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
		assert.True(t, strings.Contains(body, `VALUE="pending"`), body)
		d := download(t, env, "alice", id)
		assert.Equal(t, edit, d.EdgesEdit)
		assert.Equal(t, "ffmpeg: boom", d.EdgesError)
	}
	env.Get(t, sibling(skip, "video.m3u8")+"&edit="+edit).Status(http.StatusNotFound)
}

// fullSkipItem is a video skip item whose edges are made, its skip master
// URL, and the master's body.
func fullSkipItem(t *testing.T, env *cogtest.Env) (int64, string, string) {
	t.Helper()
	id, skip := skipItem(t, env, "alice", true, skipFixture)
	env.Get(t, skip).Status(http.StatusOK)
	runEdges(t, env, "alice")
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	return id, skip, body
}

// Playlists list edges from the index the worker recorded, as they would
// from the edge files themselves.
func TestHLSSkipPlaylistFromIndex(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip, body := fullSkipItem(t, env)
	edit := masterEdit(body)
	store := feedStore(t, env, "alice")
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	d := download(t, env, "alice", id)
	original, _, err := space.Track(d.LocalPath)
	require.NoError(t, err)
	video, _, err := space.Track(d.VideoPath)
	require.NoError(t, err)
	plan, err := feed.SkipPlan(store, d, original, nil, &video)
	require.NoError(t, err)
	require.Equal(t, edit, plan.Edit)
	for _, c := range []struct {
		file, media, key string
		track            hls.Track
		pieces           []hls.Piece
	}{
		{"video.m3u8", "video.mp4", feed.EdgeVideo, video, plan.Video},
		{"audio.m3u8", "audio.m4a", feed.EdgeOriginal, original, plan.Original},
	} {
		uri := masterURI(t, body, c.file)
		query := uri[strings.Index(uri, "?"):]
		var edges []hls.Track
		for n := range hls.Edges(c.pieces) {
			tr, _, err := space.CachedTrack(feed.EdgeName(id, edit, c.key, n))
			require.NoError(t, err)
			edges = append(edges, tr)
		}
		entries, err := hls.CutEntries(c.track, c.pieces, c.media+query, func(n int) string {
			return "edge-" + edit + "-" + c.key + "-" + strconv.Itoa(n) + ".mp4" + query
		}, edges)
		require.NoError(t, err)
		got := env.Get(t, child(skip, uri)).Status(http.StatusOK).ReadBody()
		assert.Equal(t, hls.Playlist(entries), got)
	}
}

// An edge request is checked against the recorded edges alone: it reads
// no track, so it is served even when the item's tracks cannot be read.
func TestHLSSkipEdgeReadsNoTrack(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip, body := fullSkipItem(t, env)
	edge := playlistURIs(env.Get(t, child(skip, masterURI(t, body, "video.m3u8"))).Status(http.StatusOK).ReadBody())[0]
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	d := download(t, env, "alice", id)
	for _, stored := range []string{d.LocalPath, d.VideoPath} {
		_, err := space.Own.Put(stored, strings.NewReader("not a track"))
		require.NoError(t, err)
	}
	env.Get(t, child(skip, edge), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
	env.Get(t, child(skip, masterURI(t, body, "video.m3u8"))).Status(http.StatusInternalServerError)
}

// A file gone from the cache or storage is not found, not a server error.
func TestHLSSkipMissingFiles(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id, skip, body := fullSkipItem(t, env)
	edit := masterEdit(body)
	edge := playlistURIs(env.Get(t, child(skip, masterURI(t, body, "video.m3u8"))).Status(http.StatusOK).ReadBody())[0]
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	require.NoError(t, space.Cache.Remove(feed.EdgeName(id, edit, feed.EdgeVideo, 0)))
	env.Get(t, child(skip, edge)).Status(http.StatusNotFound)

	d := download(t, env, "alice", id)
	require.NoError(t, space.Own.Remove(d.VideoPath))
	env.Get(t, sibling(masterURL(t, env, "alice", id), "video.mp4")).Status(http.StatusNotFound)
	env.Get(t, sibling(masterURL(t, env, "alice", id), "video.m3u8")).Status(http.StatusNotFound)
	env.Get(t, skip).Status(http.StatusNotFound)
}

// Under skip the captions keep the unskipped presentation's map: kept
// media and edges keep their media time.
func TestHLSSkipSubsTimestampMapOffset(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItemFrom(t, env, "alice", "audio-offset.m4a", "", testVTT)
	require.NoError(t, feedStore(t, env, "alice").SetSilenceRanges(id, []feed.Range{{Start: 7, End: 14, Action: "skip"}}))
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	subs := env.Get(t, child(skip, masterURI(t, body, "subs.m3u8"))).Status(http.StatusOK).ReadBody()
	vtts := playlistURIs(subs)
	require.True(t, len(vtts) > 0, subs)
	for _, u := range vtts {
		seg := env.Get(t, child(skip, u)).Status(http.StatusOK).ReadBody()
		assert.True(t, strings.Contains(seg, "X-TIMESTAMP-MAP=MPEGTS:132910,LOCAL:00:00:00.000\n"), seg)
	}
}

// A cleaned item whose tracks start after 0 maps its captions the same in
// skip and unskipped presentations: the clean track starts where the
// Original does.
func TestHLSSubsTimestampMapCleanOffset(t *testing.T) {
	env := cogtest.New(t, feed.New(), profanity.New())
	// Start is where the feed plugin finds the profanity plugin, without
	// which a clean item's captions are withheld.
	require.NoError(t, env.Providers.Plugins.Start(t.Context()))
	env.CreateUser(t, "alice", "pw")
	id := captionedItemFrom(t, env, "alice", "audio-offset.m4a", "", testVTT)
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	b, err := os.ReadFile("testdata/media/audio-offset.m4a")
	require.NoError(t, err)
	name := "media/" + strconv.FormatInt(id, 10) + ".hls-audio.clean.m4a"
	_, err = space.Own.Put(name, bytes.NewReader(b))
	require.NoError(t, err)
	store := feedStore(t, env, "alice")
	require.NoError(t, store.SaveCleanTrack(id, name))
	require.NoError(t, store.SetSilenceRanges(id, []feed.Range{{Start: 7, End: 14, Action: "skip"}}))

	const want = "X-TIMESTAMP-MAP=MPEGTS:132910,LOCAL:00:00:00.000\n"
	plain := env.Get(t, sibling(masterURL(t, env, "alice", id), "subs-0.vtt")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(plain, want), plain)
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	require.NotEqual(t, "", masterEdit(body), body)
	subs := env.Get(t, child(skip, masterURI(t, body, "subs.m3u8"))).Status(http.StatusOK).ReadBody()
	for _, u := range playlistURIs(subs) {
		seg := env.Get(t, child(skip, u)).Status(http.StatusOK).ReadBody()
		assert.True(t, strings.Contains(seg, want), seg)
	}
}
