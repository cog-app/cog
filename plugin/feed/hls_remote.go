package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"golang.org/x/sync/errgroup"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// errUpstream marks a failure of what a remote presentation comes from:
// yt-dlp's resolve or googlevideo. Such a request answers 502; anything else
// (the store, say) is the server's own failure.
var errUpstream = errors.New("feed: upstream failed")

// YouTubeMode is how a remote presentation's segments reach the player.
type YouTubeMode string

const (
	// YouTubeDirect redirects each segment request to googlevideo.
	YouTubeDirect YouTubeMode = "direct"
	// YouTubeProxy fetches each segment's byte range and relays it.
	YouTubeProxy YouTubeMode = "proxy"
)

// youtubeMode is how remote presentations serve their segments: the admin's
// server-wide choice (SettingYouTubeMode), direct until they make one. A
// stored value it does not know is an error rather than a guess.
func (p *Plugin) youtubeMode() (YouTubeMode, error) {
	db, err := p.RootDB()
	if err != nil {
		return "", err
	}
	mode, err := SettingYouTubeMode.Get(db)
	if err != nil {
		return "", fmt.Errorf("feed: read youtube mode: %w", err)
	}
	if !mode.valid() {
		return "", fmt.Errorf("feed: unknown youtube mode %q", mode)
	}
	return mode, nil
}

// valid is whether m is a mode the server knows.
func (m YouTubeMode) valid() bool { return m == YouTubeDirect || m == YouTubeProxy }

// remotePresentation is an undownloaded YouTube item's tracks and where they live.
type remotePresentation struct {
	Video, Audio           YouTubeTrack
	VideoTrack, AudioTrack hls.Track
}

// remoteYouTube resolves the item's tracks (youtubeTracks) and reads both
// indexes from googlevideo. A cached URL that googlevideo refuses is
// resolved again, once.
func (p *Plugin) remoteYouTube(ctx context.Context, store *Store, username string, itemID int64, watchURL string, refresh bool) (remotePresentation, error) {
	v, a, err := p.youtubeTracks(ctx, store, username, itemID, watchURL, p.ctx.Now(), refresh)
	if err != nil {
		return remotePresentation{}, err
	}
	rp := remotePresentation{Video: v, Audio: a}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		rp.VideoTrack, err = RemoteTrack(gctx, HTTP, v.URL)
		return err
	})
	g.Go(func() (err error) {
		rp.AudioTrack, err = RemoteTrack(gctx, HTTP, a.URL)
		return err
	})
	if err := g.Wait(); err != nil {
		if errors.Is(err, errRefused) && !refresh {
			return p.remoteYouTube(ctx, store, username, itemID, watchURL, true)
		}
		return remotePresentation{}, fmt.Errorf("%w: youtube item %d: %w", errUpstream, itemID, err)
	}
	return rp, nil
}

// remoteIndex reads the index of one of the item's tracks, as remoteYouTube
// does both.
func (p *Plugin) remoteIndex(ctx context.Context, store *Store, username string, itemID int64, watchURL string, kind hls.Kind, refresh bool) (hls.Track, error) {
	u, err := p.remoteURL(ctx, store, username, itemID, watchURL, kind, refresh)
	if err != nil {
		return hls.Track{}, err
	}
	t, err := RemoteTrack(ctx, HTTP, u)
	if err != nil {
		if errors.Is(err, errRefused) && !refresh {
			return p.remoteIndex(ctx, store, username, itemID, watchURL, kind, true)
		}
		return hls.Track{}, fmt.Errorf("%w: youtube item %d %s: %w", errUpstream, itemID, kind, err)
	}
	return t, nil
}

// remoteURL is the current googlevideo URL of the item's track of kind.
func (p *Plugin) remoteURL(ctx context.Context, store *Store, username string, itemID int64, watchURL string, kind hls.Kind, refresh bool) (string, error) {
	v, a, err := p.youtubeTracks(ctx, store, username, itemID, watchURL, p.ctx.Now(), refresh)
	if err != nil {
		return "", err
	}
	if kind == hls.Video {
		return v.URL, nil
	}
	return a.URL, nil
}

// serveRemoteHLS answers master.m3u8, audio.m3u8, video.m3u8, yt-audio.m4a,
// yt-video.mp4 for an undownloaded YouTube item, whose tracks are
// googlevideo's. It has no captions, no Clean rendition and no skip: a skip
// master plays the item whole and says the skip is unavailable.
func (p *Plugin) serveRemoteHLS(w http.ResponseWriter, r *http.Request, store *Store, username string, itemID int64, watchURL, file string, opts hlsOptions, sig string) {
	if opts.Edit != "" {
		// No remote master names an edit list.
		http.NotFound(w, r)
		return
	}
	query := opts.query(sig)
	ctx := r.Context()
	switch file {
	case "master.m3u8":
		rp, err := p.remoteYouTube(ctx, store, username, itemID, watchURL, false)
		if err != nil {
			remoteError(w, r, err)
			return
		}
		pres := presentation(itemTracks{original: rp.AudioTrack, video: &rp.VideoTrack}, query, false)
		if opts.Skip {
			pres.SessionData = append(pres.SessionData, [2]string{"us.duckfam.cog.skip", "unavailable"})
		}
		writePlaylist(w, r, hls.Master(pres))
	case "audio.m3u8", "video.m3u8":
		kind, media := hls.Audio, "yt-audio.m4a"
		if file == "video.m3u8" {
			kind, media = hls.Video, "yt-video.mp4"
		}
		t, err := p.remoteIndex(ctx, store, username, itemID, watchURL, kind, false)
		if err != nil {
			remoteError(w, r, err)
			return
		}
		writePlaylist(w, r, hls.MediaPlaylist(t, media+query))
	case "yt-audio.m4a":
		p.remoteSegment(w, r, store, username, itemID, watchURL, hls.Audio)
	case "yt-video.mp4":
		p.remoteSegment(w, r, store, username, itemID, watchURL, hls.Video)
	default:
		http.NotFound(w, r)
	}
}

// remoteSegment answers a request for a byte range of a remote track: a
// redirect to googlevideo in direct mode, the range relayed in proxy mode.
func (p *Plugin) remoteSegment(w http.ResponseWriter, r *http.Request, store *Store, username string, itemID int64, watchURL string, kind hls.Kind) {
	mode, err := p.youtubeMode()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	u, err := p.remoteURL(r.Context(), store, username, itemID, watchURL, kind, false)
	if err != nil {
		remoteError(w, r, err)
		return
	}
	switch mode {
	case YouTubeDirect:
		// The URL expires, so neither the redirect nor where it goes is kept.
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, u, http.StatusFound)
		return
	case YouTubeProxy:
	default:
		httperr.Internal(w, r, fmt.Errorf("feed hls: unknown youtube mode %q", mode))
		return
	}
	resp, err := proxyRange(r, u)
	if err == nil && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone) {
		// The URL has expired or been withdrawn: resolve afresh and try once more.
		closeUpstream(resp)
		if u, err = p.remoteURL(r.Context(), store, username, itemID, watchURL, kind, true); err != nil {
			remoteError(w, r, err)
			return
		}
		resp, err = proxyRange(r, u)
	}
	if err != nil {
		remoteError(w, r, err)
		return
	}
	defer closeUpstream(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
	default:
		remoteError(w, r, fmt.Errorf("%w: youtube item %d %s: %s", errUpstream, itemID, kind, resp.Status))
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Debug("feed hls: relay youtube segment", "item", itemID, "kind", kind, "err", err)
	}
}

// proxyRange asks u for what r asks: the same method, Range and If-Range.
func proxyRange(r *http.Request, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("feed hls: segment request: %w", err)
	}
	for _, h := range []string{"Range", "If-Range"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: segment: %w", errUpstream, err)
	}
	return resp, nil
}

func closeUpstream(resp *http.Response) {
	if err := resp.Body.Close(); err != nil {
		slog.Debug("feed hls: close upstream", "err", err)
	}
}

// remoteError answers a remote request that failed: 502, with the cause
// logged, when yt-dlp or googlevideo failed (errUpstream), and as hlsError
// does otherwise.
func remoteError(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, errUpstream) {
		hlsError(w, r, err)
		return
	}
	httperr.Log(r, err)
	http.Error(w, "Bad Gateway", http.StatusBadGateway)
}
