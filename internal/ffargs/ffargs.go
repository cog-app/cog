// Package ffargs builds ffmpeg and ffprobe arguments for media a user
// supplied: a file they uploaded, or one a feed pointed at.
package ffargs

// Formats are the demuxers user media may be read with: audio and video
// containers. ffmpeg otherwise picks a demuxer from the file's contents, and
// some read other files — an HLS playlist or a concat list saved as
// "episode.mp3" opens whatever paths or URLs it names, and the result (a
// cleaned episode, a thumbnail) comes back to the user.
const Formats = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi,mpegts,mpeg,flv,asf,ogg,mp3,wav,flac,aac"

// Input returns the arguments that open the local file path as an input,
// restricted to Formats and to the file itself. They go before the input's
// position in the command, replacing a plain "-i path".
func Input(path string) []string {
	return []string{
		"-protocol_whitelist", "file",
		"-format_whitelist", Formats,
		"-i", "file:" + path,
	}
}

// Probe returns ffprobe's arguments for the same restrictions, ending with the
// file, for a command that probes one.
func Probe(path string) []string {
	return []string{
		"-protocol_whitelist", "file",
		"-format_whitelist", Formats,
		"file:" + path,
	}
}
