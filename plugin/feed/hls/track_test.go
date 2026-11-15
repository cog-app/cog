package hls_test

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func readTrackErr(t *testing.T, name string) (hls.Track, error) {
	t.Helper()
	f, err := os.Open("../testdata/media/" + name)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	st, err := f.Stat()
	require.NoError(t, err)
	return hls.ReadTrack(f, st.Size())
}

func readTrack(t *testing.T, name string) hls.Track {
	t.Helper()
	tr, err := readTrackErr(t, name)
	require.NoError(t, err)
	st, err := os.Stat("../testdata/media/" + name)
	require.NoError(t, err)
	checkContiguous(t, tr, st.Size())
	return tr
}

// checkContiguous asserts the fragments tile the file after the init
// section, and their times tile the timeline.
func checkContiguous(t *testing.T, tr hls.Track, size int64) {
	t.Helper()
	assert.Equal(t, int64(0), tr.InitOffset)
	require.True(t, len(tr.Fragments) > 0, "no fragments")
	assert.True(t, tr.Fragments[0].Offset >= tr.InitOffset+tr.InitLength, "first fragment overlaps init")
	for i, f := range tr.Fragments[1:] {
		prev := tr.Fragments[i]
		assert.Equal(t, prev.Offset+prev.Length, f.Offset, "fragment %d offset", i+1)
		assert.InDelta(t, prev.Start+prev.Duration, f.Start, 0.001, "fragment %d start", i+1)
	}
	last := tr.Fragments[len(tr.Fragments)-1]
	// ffmpeg ends a fragmented file with an mfra box the sidx does not index.
	assert.True(t, last.Offset+last.Length <= size && last.Offset+last.Length > size-1024,
		"last fragment ends at %d, file at %d", last.Offset+last.Length, size)
}

func totalDuration(tr hls.Track) float64 {
	var d float64
	for _, f := range tr.Fragments {
		d += f.Duration
	}
	return d
}

func TestReadTrackVideo(t *testing.T) {
	tr := readTrack(t, "video.mp4")
	assert.Equal(t, hls.Video, tr.Kind)
	assert.Equal(t, 160, tr.Width)
	assert.Equal(t, 90, tr.Height)
	assert.True(t, strings.HasPrefix(tr.Codecs, "avc1.42") && strings.HasSuffix(tr.Codecs, "1E") && len(tr.Codecs) == 11, tr.Codecs)
	assert.Equal(t, 6, len(tr.Fragments)) // 12 s at a 2 s GOP
	for _, f := range tr.Fragments {
		assert.True(t, f.Keyframe)
	}
	assert.InDelta(t, 12.0, totalDuration(tr), 0.05)
}

func TestReadTrackAudio(t *testing.T) {
	tr := readTrack(t, "audio.m4a")
	assert.Equal(t, hls.Audio, tr.Kind)
	assert.Equal(t, "mp4a.40.2", tr.Codecs)
	assert.InDelta(t, 12.0, totalDuration(tr), 0.05)
	for _, f := range tr.Fragments {
		assert.True(t, f.Duration <= 2.1, math.Round(f.Duration*1000))
	}
}

// A merged download has two tracks; an MP3 has no index.
func TestReadTrackNotATrack(t *testing.T) {
	_, err := readTrackErr(t, "merged.mp4")
	assert.True(t, errors.Is(err, hls.ErrMultiTrack), err)
	_, err = readTrackErr(t, "episode.mp3")
	assert.True(t, errors.Is(err, hls.ErrNoIndex), err)
	_, err = readTrackErr(t, "plain-video.mp4")
	assert.True(t, errors.Is(err, hls.ErrNoIndex), err)
}
