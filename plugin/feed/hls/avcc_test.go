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

// Parameter sets with id 1, as an edge encoded with x264's sps-id=1 has.
// Only their ids are read: seq_parameter_set_id after the SPS's first three
// bytes, pic_parameter_set_id first in the PPS, both Exp-Golomb 010.
var (
	sps1 = []byte{0x67, 0x42, 0xc0, 0x1e, 0x40, 0xaa}
	pps1 = []byte{0x68, 0x48, 0xbb}
)

func trackOf(t *testing.T, b []byte) hls.Track {
	t.Helper()
	tr, err := hls.ReadTrack(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	return tr
}

func initOf(t *testing.T, b []byte) []byte {
	t.Helper()
	tr := trackOf(t, b)
	return b[tr.InitOffset : tr.InitOffset+tr.InitLength]
}

func TestInitParamSets(t *testing.T) {
	b, err := os.ReadFile("../testdata/media/video.mp4")
	require.NoError(t, err)
	sets, err := hls.InitParamSets(initOf(t, b))
	require.NoError(t, err)
	require.Equal(t, 1, len(sets.SPS))
	require.Equal(t, 1, len(sets.PPS))
	assert.Equal(t, byte(0x67), sets.SPS[0][0])
	assert.Equal(t, byte(0x68), sets.PPS[0][0])

	a, err := os.ReadFile("../testdata/media/audio.m4a")
	require.NoError(t, err)
	_, err = hls.InitParamSets(initOf(t, a))
	assert.Error(t, err)
}

// An init that also carries other sets is a bigger moov; the fragments it
// indexes keep their times and lengths, move with it, and decode at the
// same time.
func TestAddParamSets(t *testing.T) {
	for _, fixture := range []string{"video.mp4", "video-high.mp4"} {
		b, err := os.ReadFile("../testdata/media/" + fixture)
		require.NoError(t, err)
		src, err := hls.InitParamSets(initOf(t, b))
		require.NoError(t, err)

		out, err := hls.AddParamSets(b, hls.ParamSets{SPS: [][]byte{sps1}, PPS: [][]byte{pps1}})
		require.NoError(t, err, fixture)
		grown := int64(len(out) - len(b))
		assert.Equal(t, int64(2+len(sps1)+2+len(pps1)), grown, fixture)

		before, after := trackOf(t, b), trackOf(t, out)
		assert.Equal(t, before.Codecs, after.Codecs)
		assert.Equal(t, before.InitLength+grown, after.InitLength)
		require.Equal(t, len(before.Fragments), len(after.Fragments))
		for i, f := range before.Fragments {
			g := after.Fragments[i]
			assert.Equal(t, f.Start, g.Start)
			assert.Equal(t, f.Duration, g.Duration)
			assert.Equal(t, f.Length, g.Length)
			assert.Equal(t, f.Offset+grown, g.Offset)
			// The fragment's bytes are the same.
			assert.True(t, bytes.Equal(b[f.Offset:f.Offset+f.Length], out[g.Offset:g.Offset+g.Length]), "%s fragment %d", fixture, i)
		}
		tfdt0, err := hls.FirstDecodeTime(bytes.NewReader(b), int64(len(b)))
		require.NoError(t, err)
		tfdt1, err := hls.FirstDecodeTime(bytes.NewReader(out), int64(len(out)))
		require.NoError(t, err)
		assert.Equal(t, tfdt0, tfdt1)

		sets, err := hls.InitParamSets(initOf(t, out))
		require.NoError(t, err)
		assert.Equal(t, [][]byte{src.SPS[0], sps1}, sets.SPS)
		assert.Equal(t, [][]byte{src.PPS[0], pps1}, sets.PPS)

		// The same sets again change nothing; an id with other contents
		// is refused.
		again, err := hls.AddParamSets(out, sets)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(out, again))
		_, err = hls.AddParamSets(b, hls.ParamSets{SPS: [][]byte{append(append([]byte{}, src.SPS[0]...), 0)}})
		assert.Error(t, err)
	}
}

// The mfra at the end of a fragmented file lists its moofs by absolute
// offset: they move with the moov.
func TestAddParamSetsMovesTfra(t *testing.T) {
	b, err := os.ReadFile("../testdata/media/video.mp4")
	require.NoError(t, err)
	before := tfraOffsets(t, b)
	require.True(t, len(before) > 0, "fixture has no tfra")
	out, err := hls.AddParamSets(b, hls.ParamSets{SPS: [][]byte{sps1}})
	require.NoError(t, err)
	after := tfraOffsets(t, out)
	require.Equal(t, len(before), len(after))
	for i := range before {
		assert.Equal(t, before[i]+uint64(len(out)-len(b)), after[i])
		assert.Equal(t, "moof", string(out[after[i]+4:after[i]+8]))
	}
}

// tfraOffsets reads the moof offsets of the version 1 tfra in the file's
// trailing mfra (mfro gives its size).
func tfraOffsets(t *testing.T, b []byte) []uint64 {
	t.Helper()
	mfraSize := int(binary.BigEndian.Uint32(b[len(b)-4:]))
	mfra := b[len(b)-mfraSize:]
	require.Equal(t, "mfra", string(mfra[4:8]))
	tfra := mfra[8:]
	require.Equal(t, "tfra", string(tfra[4:8]))
	body := tfra[8:]
	wide := body[0] == 1
	sizes := binary.BigEndian.Uint32(body[8:])
	trailing := int((sizes>>4)&3+1) + int((sizes>>2)&3+1) + int(sizes&3+1)
	n := int(binary.BigEndian.Uint32(body[12:]))
	field := 4
	if wide {
		field = 8
	}
	var out []uint64
	for i := range n {
		at := 16 + i*(2*field+trailing) + field
		if wide {
			out = append(out, binary.BigEndian.Uint64(body[at:]))
		} else {
			out = append(out, uint64(binary.BigEndian.Uint32(body[at:])))
		}
	}
	return out
}
