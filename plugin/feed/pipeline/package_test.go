package pipeline_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
)

// needFFmpeg skips a test that runs the real ffmpeg where there is none,
// except in CI, whose image has it.
func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("ffmpeg is not installed: %v", err)
		}
		t.Skipf("ffmpeg is not installed: %v", err)
	}
}

// putFixture stores testdata/media/<fixture> in the user's storage as name.
func putFixture(t *testing.T, space feed.Space, fixture, name string) {
	t.Helper()
	b, err := os.ReadFile("../testdata/media/" + fixture)
	require.NoError(t, err)
	_, err = space.Own.Put(name, bytes.NewReader(b))
	require.NoError(t, err)
}

// readyDownload makes an item of type typ whose download is ready.
func readyDownload(t *testing.T, s *feed.Store, typ string) int64 {
	t.Helper()
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://example.com/feed"})
	require.NoError(t, err)
	id, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: typ, MediaURL: "https://example.com/ep"})
	require.NoError(t, err)
	require.NoError(t, s.EnqueueDownload(id))
	return id
}

func storedTrack(t *testing.T, space feed.Space, name string) hls.Track {
	t.Helper()
	tr, _, err := space.Track(name)
	require.NoError(t, err)
	return tr
}

func storedBytes(t *testing.T, space feed.Space, name string) []byte {
	t.Helper()
	f, err := space.Own.Open(name)
	require.NoError(t, err)
	defer f.Close()
	b, err := io.ReadAll(f)
	require.NoError(t, err)
	return b
}

func size(t *testing.T, space feed.Space, name string) int64 {
	t.Helper()
	st, err := space.Own.Stat(name)
	require.NoError(t, err)
	return st.Size()
}

func packageMerged(t *testing.T) (*feed.Store, feed.Space, int64) {
	t.Helper()
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "merged.mp4", "media/1.mp4")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	return s, space, id
}

// An old merged download becomes a video and an audio track.
func TestPackageMerged(t *testing.T) {
	s, space, id := packageMerged(t)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "done", d.PackageState, d.PackageError)
	prefix := "media/" + strconv.FormatInt(id, 10)
	assert.Equal(t, prefix+".hls-audio.m4a", d.LocalPath)
	assert.Equal(t, prefix+".hls-video.mp4", d.VideoPath)
	audio := storedTrack(t, space, d.LocalPath)
	assert.Equal(t, hls.Audio, audio.Kind)
	assert.Equal(t, "mp4a.40.2", audio.Codecs)
	video := storedTrack(t, space, d.VideoPath)
	assert.Equal(t, hls.Video, video.Kind)
	assert.True(t, strings.HasPrefix(video.Codecs, "avc1."), video.Codecs)
	assert.Equal(t, size(t, space, d.LocalPath)+size(t, space, d.VideoPath), d.Bytes)
	cached, err := space.Cache.ReadDir("package")
	require.NoError(t, err)
	assert.Equal(t, 0, len(cached), "package left in the cache")
}

// Tracks that are already indexed fMP4, as YouTube's are, are used as they are.
func TestPackageUsesIndexedTracksAsIs(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "audio.m4a", "media/1.m4a")
	putFixture(t, space, "video.mp4", "media/1.video.mp4")
	require.NoError(t, s.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "done", d.PackageState, d.PackageError)
	assert.Equal(t, "media/1.m4a", d.LocalPath)
	assert.Equal(t, "media/1.video.mp4", d.VideoPath)
	assert.Equal(t, size(t, space, "media/1.m4a")+size(t, space, "media/1.video.mp4"), d.Bytes)
	for name, fixture := range map[string]string{"media/1.m4a": "audio.m4a", "media/1.video.mp4": "video.mp4"} {
		want, err := os.ReadFile("../testdata/media/" + fixture)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(want, storedBytes(t, space, name)), "%s was rewritten", name)
	}
	media, err := space.Own.ReadDir("media")
	require.NoError(t, err)
	assert.Equal(t, 2, len(media), "files under media/")
	cached, err := space.Cache.ReadDir("package")
	require.NoError(t, err)
	assert.Equal(t, 0, len(cached), "package left in the cache")
}

// A downloaded track that is not indexed fMP4 is remuxed.
func TestPackageRemuxesUnindexed(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "video")
	putFixture(t, space, "audio.m4a", "media/1.m4a")
	putFixture(t, space, "plain-video.mp4", "media/1.video.mp4")
	require.NoError(t, s.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "done", d.PackageState, d.PackageError)
	assert.Equal(t, "media/1.m4a", d.LocalPath)
	assert.Equal(t, "media/"+strconv.FormatInt(id, 10)+".hls-video.mp4", d.VideoPath)
	assert.Equal(t, hls.Video, storedTrack(t, space, d.VideoPath).Kind)
}

// A podcast MP3's cover art is not a video track.
func TestPackageMP3WithCover(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "audio")
	putFixture(t, space, "episode.mp3", "media/1.mp3")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.mp3", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "done", d.PackageState, d.PackageError)
	assert.Equal(t, "", d.VideoPath)
	audio := storedTrack(t, space, d.LocalPath)
	assert.Equal(t, hls.Audio, audio.Kind)
	assert.Equal(t, "mp4a.40.2", audio.Codecs)
	assert.InDelta(t, 6.0, audio.Fragments[len(audio.Fragments)-1].Start+audio.Fragments[len(audio.Fragments)-1].Duration, 0.2)
}

// Packaging that fails leaves the item playable as before, and is not tried every tick.
func TestPackageFailureKeepsMedia(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "audio")
	_, err := space.Own.Put("media/1.mp3", strings.NewReader("this is not media at all"))
	require.NoError(t, err)
	require.NoError(t, s.MarkDownloadReady(id, "media/1.mp3", 24))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))

	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "error", d.PackageState)
	assert.NotEqual(t, "", d.PackageError)
	assert.Equal(t, "media/1.mp3", d.LocalPath)
	assert.Equal(t, "this is not media at all", string(storedBytes(t, space, "media/1.mp3")))
	ids, err := s.PendingPackage(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(ids))
	media, err := space.Own.ReadDir("media")
	require.NoError(t, err)
	assert.Equal(t, 1, len(media), "files under media/")
}

// Collect removes the merged original once its tracks replace it.
func TestPackageThenCollect(t *testing.T) {
	s, space, id := packageMerged(t)
	require.NoError(t, pipeline.Collect(s, space))
	_, err := space.Own.Stat("media/1.mp4")
	assert.Error(t, err)
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	for _, name := range []string{d.LocalPath, d.VideoPath} {
		_, err := space.Own.Stat(name)
		assert.NoError(t, err, name)
	}
}

// Collect keeps the clean track a row names.
func TestCollectKeepsCleanTrack(t *testing.T) {
	s, space, id := packageMerged(t)
	_, err := space.Own.Put("media/1.hls-audio.clean.m4a", strings.NewReader("x"))
	require.NoError(t, err)
	require.NoError(t, s.SaveCleanTrack(id, "media/1.hls-audio.clean.m4a"))
	require.NoError(t, pipeline.Collect(s, space))
	f, err := space.Own.Open("media/1.hls-audio.clean.m4a")
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func trackDuration(tr hls.Track) float64 {
	var total float64
	for _, f := range tr.Fragments {
		total += f.Duration
	}
	return total
}

// packagedWithClean makes a packaged audio item whose clean file is the
// fixture, stored as media/<id>.clean<ext of name>.
func packagedWithClean(t *testing.T, fixture, cleanName string) (*feed.Store, feed.Space, int64) {
	t.Helper()
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "audio")
	putFixture(t, space, "audio.m4a", "media/1.hls-audio.m4a")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.hls-audio.m4a", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	putFixture(t, space, fixture, cleanName)
	require.NoError(t, s.SetCleanAudioPath(id, cleanName))
	return s, space, id
}

func TestPackageCleanTrack(t *testing.T) {
	s, space, id := packagedWithClean(t, "plain-audio.m4a", "media/1.clean.m4a")
	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanTrackError)
	assert.Equal(t, "media/1.hls-audio.clean.m4a", d.CleanTrackPath)
	assert.Equal(t, d.CleanTrackPath, d.CleanAudioPath)
	tr := storedTrack(t, space, d.CleanTrackPath)
	assert.Equal(t, hls.Audio, tr.Kind)
	assert.Equal(t, "mp4a.40.2", tr.Codecs)
	assert.True(t, trackDuration(tr) > 11.9 && trackDuration(tr) < 12.1, "duration %v", trackDuration(tr))
	require.NoError(t, pipeline.Collect(s, space))
	_, err = space.Own.Stat("media/1.clean.m4a")
	assert.Error(t, err)
	_, err = space.Own.Stat(d.CleanTrackPath)
	assert.NoError(t, err)
	cached, err := space.Cache.ReadDir("package")
	require.NoError(t, err)
	assert.Equal(t, 0, len(cached), "package left in the cache")
}

func TestPackageCleanTrackFromMP3(t *testing.T) {
	s, space, id := packagedWithClean(t, "episode.mp3", "media/1.clean.mp3")
	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.CleanTrackError)
	tr := storedTrack(t, space, d.CleanTrackPath)
	assert.Equal(t, hls.Audio, tr.Kind)
	assert.Equal(t, "mp4a.40.2", tr.Codecs)
}

func TestPackageCleanTrackFromMerged(t *testing.T) {
	s, space, id := packagedWithClean(t, "merged.mp4", "media/1.clean.mp4")
	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.CleanTrackError)
	tr := storedTrack(t, space, d.CleanTrackPath)
	assert.Equal(t, hls.Audio, tr.Kind)
	assert.Equal(t, "mp4a.40.2", tr.Codecs)
}

func TestPackageCleanTrackFailure(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "audio")
	putFixture(t, space, "audio.m4a", "media/1.hls-audio.m4a")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.hls-audio.m4a", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	_, err := space.Own.Put("media/1.clean.m4a", bytes.NewReader(make([]byte, 1024)))
	require.NoError(t, err)
	require.NoError(t, s.SetCleanAudioPath(id, "media/1.clean.m4a"))

	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.NotEqual(t, "", d.CleanTrackError)
	assert.Equal(t, "media/1.clean.m4a", d.CleanAudioPath)
	assert.Equal(t, "", d.CleanTrackPath)
	pending, err := s.PendingCleanPackage(10)
	require.NoError(t, err)
	assert.Equal(t, 0, len(pending))
	_, err = space.Own.Stat("media/1.hls-audio.clean.m4a")
	assert.Error(t, err)
	cached, err := space.Cache.ReadDir("package")
	require.NoError(t, err)
	assert.Equal(t, 0, len(cached), "package left in the cache")
}

// Items cleaned before the clean track existed have their clean file under
// the track's own name; packaging replaces it in place.
func TestPackageCleanTrackLegacyName(t *testing.T) {
	s, space, id := packagedWithClean(t, "plain-audio.m4a", "media/1.hls-audio.clean.m4a")
	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, "", d.CleanTrackError)
	assert.Equal(t, "media/1.hls-audio.clean.m4a", d.CleanTrackPath)
	assert.Equal(t, d.CleanTrackPath, d.CleanAudioPath)
	assert.Equal(t, hls.Audio, storedTrack(t, space, d.CleanTrackPath).Kind)
}

// A failure after the clean file was replaced by the track must not leave a
// clean_audio_path naming the file the cleanup removed: the item is cleaned again.
func TestPackageCleanTrackLegacyNameFailure(t *testing.T) {
	needFFmpeg(t)
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	space, err := p.(*feed.Plugin).Space(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	id := readyDownload(t, s, "audio")
	putFixture(t, space, "audio.m4a", "media/1.hls-audio.m4a")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.hls-audio.m4a", 1))
	require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
	legacy := "media/1.hls-audio.clean.m4a"
	putFixture(t, space, "plain-audio.m4a", legacy)
	require.NoError(t, s.SetCleanAudioPath(id, legacy))
	// Saving the track fails, after it has replaced the clean file.
	_, err = db.Exec(`CREATE TRIGGER fail_clean_track BEFORE UPDATE OF clean_track_path ON downloads
		WHEN NEW.clean_track_path != '' BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	require.NoError(t, err)

	require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanAudioPath)
	assert.Equal(t, "", d.CleanTrackPath)
	assert.True(t, d.NeedsProfanity, "item is cleaned again")
	assert.True(t, strings.Contains(d.CleanTrackError, "boom"), d.CleanTrackError)
	_, err = space.Own.Stat(legacy)
	assert.Error(t, err)
}

// copyFixture is a profanity Processor that writes a fixture as the clean file.
type copyFixture struct{ name string }

func (c *copyFixture) Process(_ context.Context, _, out string, _ []cogplugin.CaptionSegment) error {
	b, err := os.ReadFile("../testdata/media/" + c.name)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0o644)
}

func TestRecleanReplacesTrack(t *testing.T) {
	needFFmpeg(t)
	s, space := newPipeline(t)
	id := readyDownload(t, s, "audio")
	putFixture(t, space, "audio.m4a", "media/1.hls-audio.m4a")
	require.NoError(t, s.MarkDownloadReady(id, "media/1.hls-audio.m4a", 1))
	_, err := space.Own.Put("captions/g.vtt", strings.NewReader("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello\n"))
	require.NoError(t, err)
	require.NoError(t, s.SetCaptionsPath(id, "captions/g.vtt"))
	ctx := context.Background()
	pk := pipeline.NewPackager(s, space)
	require.NoError(t, pk.RunOnce(ctx))

	clean := func(fixture string) {
		t.Helper()
		require.NoError(t, s.EnqueueProfanity(id))
		require.NoError(t, pipeline.NewProfanity(s, space, &copyFixture{fixture}).RunOnce(ctx))
	}
	clean("plain-audio.m4a")
	require.NoError(t, pk.RunCleanOnce(ctx))
	require.NoError(t, pipeline.Collect(s, space))
	d, err := s.GetDownload(id)
	require.NoError(t, err)
	track := d.CleanTrackPath
	require.Equal(t, "media/1.hls-audio.clean.m4a", track)
	assert.True(t, trackDuration(storedTrack(t, space, track)) > 11.9)

	clean("episode.mp3")
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.CleanTrackPath)

	require.NoError(t, pk.RunCleanOnce(ctx))
	require.NoError(t, pipeline.Collect(s, space))
	d, err = s.GetDownload(id)
	require.NoError(t, err)
	require.Equal(t, track, d.CleanTrackPath)
	dur := trackDuration(storedTrack(t, space, track))
	assert.True(t, dur > 5.9 && dur < 6.1, "duration %v", dur)
	entries, err := space.Own.ReadDir("media")
	require.NoError(t, err)
	for _, e := range entries {
		n := e.Name()
		if strings.Contains(n, ".clean.") && n != "1.hls-audio.clean.m4a" {
			t.Errorf("stray clean file %s", n)
		}
	}
}

// Players place a packaged track's fragments by their tfdt, and captions
// map their time 0 to its sidx start (hls.TimestampMap): the two must agree,
// including for a source that starts later than 0.
func TestPackagedTrackDecodesAtItsIndex(t *testing.T) {
	needFFmpeg(t)
	offset := filepath.Join(t.TempDir(), "offset.m4a")
	out, err := exec.Command("ffmpeg", "-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-output_ts_offset", "1.5", "-avoid_negative_ts", "disabled", "-c:a", "aac", "-b:a", "32k", "-movflags", "+faststart", offset).CombinedOutput()
	require.NoError(t, err, string(out))
	for _, c := range []struct{ name, fixture, typ string }{
		{"merged", "merged.mp4", "video"},
		{"mp3", "episode.mp3", "audio"},
		{"offset", "", "audio"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, space := newPipeline(t)
			id := readyDownload(t, s, c.typ)
			stored := "media/1.src"
			if c.fixture != "" {
				putFixture(t, space, c.fixture, stored)
			} else {
				b, err := os.ReadFile(offset)
				require.NoError(t, err)
				_, err = space.Own.Put(stored, bytes.NewReader(b))
				require.NoError(t, err)
			}
			require.NoError(t, s.MarkDownloadReady(id, stored, 1))
			require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
			d, err := s.GetDownload(id)
			require.NoError(t, err)
			require.Equal(t, "done", d.PackageState, d.PackageError)
			for _, stored := range []string{d.LocalPath, d.VideoPath} {
				if stored == "" {
					continue
				}
				tr := storedTrack(t, space, stored)
				p, err := space.Path(stored)
				require.NoError(t, err)
				f, err := os.Open(p)
				require.NoError(t, err)
				st, err := f.Stat()
				require.NoError(t, err)
				tfdt, err := hls.FirstDecodeTime(f, st.Size())
				require.NoError(t, errors.Join(err, f.Close()))
				assert.InDelta(t, tr.Fragments[0].Start, tfdt, 1e-3, stored)
			}
		})
	}
}

// A clean track starts where the Original does, in its index and its
// fragments' tfdt: the clean file's first sample is the Original's first
// (the profanity worker decodes it from there), and an Original used as
// downloaded can start after 0.
func TestPackageCleanTrackKeepsOriginalStart(t *testing.T) {
	for _, c := range []struct{ original, clean string }{
		{"audio-offset.m4a", "plain-audio.m4a"}, // copied
		{"audio-offset.m4a", "episode.mp3"},     // encoded
		{"audio.m4a", "plain-audio.m4a"},
	} {
		t.Run(c.original+"/"+c.clean, func(t *testing.T) {
			needFFmpeg(t)
			s, space := newPipeline(t)
			id := readyDownload(t, s, "audio")
			putFixture(t, space, c.original, "media/1.hls-audio.m4a")
			require.NoError(t, s.MarkDownloadReady(id, "media/1.hls-audio.m4a", 1))
			require.NoError(t, pipeline.NewPackager(s, space).RunOnce(context.Background()))
			putFixture(t, space, c.clean, "media/1.clean.m4a")
			require.NoError(t, s.SetCleanAudioPath(id, "media/1.clean.m4a"))
			require.NoError(t, pipeline.NewPackager(s, space).RunCleanOnce(context.Background()))
			d, err := s.GetDownload(id)
			require.NoError(t, err)
			require.Equal(t, "", d.CleanTrackError)
			original := storedTrack(t, space, d.LocalPath)
			clean := storedTrack(t, space, d.CleanTrackPath)
			assert.InDelta(t, original.Fragments[0].Start, clean.Fragments[0].Start, 1e-3, "clean sidx start")
			tfdt, err := space.DecodeTime(d.CleanTrackPath)
			require.NoError(t, err)
			assert.InDelta(t, original.Fragments[0].Start, tfdt, 1e-3, "clean tfdt")
		})
	}
}
