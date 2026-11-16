package feed

import "context"

// SetStreamResolver makes ResolveMedia resolve YouTube streams with f, and
// returns what puts the real one back.
func SetStreamResolver(f func(ctx context.Context, watchURL string, audio bool) (string, error)) func() {
	old := resolveStream
	resolveStream = f
	return func() { resolveStream = old }
}

// HLSOptions is the signed options of a media link, for the external tests.
type HLSOptions = hlsOptions

// SetTrackResolver makes youtubeTracks resolve with f, and returns what puts
// the real one back.
func SetTrackResolver(f func(ctx context.Context, watchURL, format string) (YouTubeTrack, YouTubeTrack, error)) func() {
	old := resolveTracks
	resolveTracks = f
	return func() { resolveTracks = old }
}
