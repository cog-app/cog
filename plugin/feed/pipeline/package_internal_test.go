package pipeline

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// The flags must match testdata/media/make.sh's for video.mp4 and audio.m4a.
func TestPackageArgsVideo(t *testing.T) {
	got := strings.Join(packageArgs("in.mp4", "out.mp4", hls.Video, false), " ")
	assert.True(t, strings.HasPrefix(got, "-nostdin -y -v error -protocol_whitelist file "), got)
	assert.True(t, strings.HasSuffix(got, " -i file:in.mp4 -map 0:V:0 -c copy -movflags +frag_keyframe+empty_moov+default_base_moof+global_sidx -f mp4 out.mp4"), got)
}

func TestPackageArgsAudioCopy(t *testing.T) {
	got := strings.Join(packageArgs("in.m4a", "out.m4a", hls.Audio, false), " ")
	assert.True(t, strings.HasSuffix(got, " -i file:in.m4a -map 0:a:0 -c copy -movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4 out.m4a"), got)
}

func TestPackageArgsAudioTranscode(t *testing.T) {
	got := strings.Join(packageArgs("in.mp3", "out.m4a", hls.Audio, true), " ")
	assert.True(t, strings.HasSuffix(got, " -i file:in.mp3 -map 0:a:0 -c:a aac -b:a 128k -movflags +empty_moov+default_base_moof+global_sidx -frag_duration 2000000 -f mp4 out.m4a"), got)
}

func TestParseProbe(t *testing.T) {
	out := []byte(`{
		"programs": [],
		"streams": [
			{"index": 0, "codec_name": "mp3", "codec_type": "audio", "disposition": {"attached_pic": 0}},
			{"index": 1, "codec_name": "mjpeg", "codec_type": "video", "disposition": {"attached_pic": 1}}
		]
	}`)
	got, err := parseProbe(out)
	require.NoError(t, err)
	assert.Equal(t, []probeStream{
		{Index: 0, Type: "audio", Codec: "mp3"},
		{Index: 1, Type: "video", Codec: "mjpeg", AttachedPic: true},
	}, got)
	_, err = parseProbe([]byte("not json"))
	assert.Error(t, err)
}

func TestCleanName(t *testing.T) {
	assert.Equal(t, "media/7.clean.m4a", cleanName(7, "media/7.hls-audio.m4a"))
	assert.Equal(t, "media/7.clean.mp3", cleanName(7, "media/7.mp3"))
	assert.Equal(t, "media/7.clean", cleanName(7, "media/7"))
}
