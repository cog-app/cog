package feed

import (
	"context"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"

	"connectrpc.com/connect"
)

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

// YouTubeModeOf is how p serves remote segments.
func YouTubeModeOf(p *Plugin) (YouTubeMode, error) { return p.youtubeMode() }

// ResolveMediaAs calls ResolveMedia in process as username, with ctx, so a
// test can cancel ctx as a client that goes away would.
func ResolveMediaAs(ctx context.Context, p *Plugin, username string, req *feedpb.ResolveMediaRequest) (*feedpb.MediaSource, error) {
	ctx = plugin.WithUserInfo(ctx, &plugin.UserInfo{Username: username})
	res, err := (&FeedService{plugin: p}).ResolveMedia(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// HLSMasterPath is hlsMasterPath, for the external tests.
var HLSMasterPath = hlsMasterPath

// M3UList, M3USignature and ViewTag are the M3U signing surface, for the external tests.
type M3UList = m3uList

var M3USignature = m3uSignature
