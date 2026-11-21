package hls_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func TestFirstDecodeTime(t *testing.T) {
	b, err := os.ReadFile("../testdata/media/video.mp4")
	require.NoError(t, err)
	got, err := hls.FirstDecodeTime(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	assert.Equal(t, 0.0, got)

	// The first fragment's tfdt, moved to 4.125 s at the 12288 Hz timescale.
	i := bytes.Index(b, []byte("tfdt"))
	require.True(t, i > 0, "no tfdt")
	switch b[i+4] { // version
	case 0:
		binary.BigEndian.PutUint32(b[i+8:], 4.125*12288)
	default:
		binary.BigEndian.PutUint64(b[i+8:], 4.125*12288)
	}
	got, err = hls.FirstDecodeTime(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	assert.InDelta(t, 4.125, got, 1e-9)

	b, err = os.ReadFile("../testdata/media/audio.m4a")
	require.NoError(t, err)
	got, err = hls.FirstDecodeTime(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	assert.Equal(t, 0.0, got)
}

func TestFirstDecodeTimeNoFragment(t *testing.T) {
	b, err := os.ReadFile("../testdata/media/plain-video.mp4")
	require.NoError(t, err)
	_, err = hls.FirstDecodeTime(bytes.NewReader(b), int64(len(b)))
	assert.Error(t, err)
}
