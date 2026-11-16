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
