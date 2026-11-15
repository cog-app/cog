package pipeline_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
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
