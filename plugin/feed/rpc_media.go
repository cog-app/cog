package feed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

// resolveStream turns a YouTube watch URL into a stream URL; tests replace it.
var resolveStream = ResolveYouTubeStreamURL

// ResolveMedia says where a client plays an item from. A downloaded video
// whose tracks are packaged plays from its signed HLS master playlist, which
// needs no credentials. Other media Cog holds (a download's audio, or its
// profanity-cleaned audio) plays from the item's media route, a path on this
// server the client fetches with its own credentials. A YouTube item Cog has
// not downloaded plays its audio from a resolved stream, and its video, while
// no download is ready, from a signed HLS master over googlevideo's tracks
// (remoteMaster), leaving any download queued or running alone. When those
// tracks cannot be HLS, or the item is to be muted, its video is downloaded
// first: asking queues the download and answers Unavailable until it is
// downloaded and packaged, which a client asks again on. Any other enclosure
// plays from its origin. An item to be muted (Download.muted) plays nothing
// but its clean media: until it has some (Download.uncleanOnly) it answers
// Unavailable with why (waitClean), whatever it is and whichever variant is
// asked for.
func (s *FeedService) ResolveMedia(ctx context.Context, req *connect.Request[feedpb.ResolveMediaRequest]) (*connect.Response[feedpb.MediaSource], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	id := req.Msg.ItemId
	if _, err := getItem(store, id); err != nil {
		return nil, err
	}
	ip, err := store.GetItemPlayback(id)
	if err != nil {
		return nil, err
	}
	dl, err := store.FindDownload(id)
	if err != nil {
		return nil, err
	}
	kind := feedpb.Variant_VARIANT_VIDEO
	if ip.Type == "audio" {
		kind = feedpb.Variant_VARIANT_AUDIO
	}
	audio := req.Msg.Variant == feedpb.Variant_VARIANT_AUDIO || ip.Type == "audio"
	if dl.uncleanOnly() {
		why, err := s.plugin.waitClean(store, plugin.UserFromContext(ctx).Username, id, dl, audio)
		if err != nil {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New(why))
	}
	if dl.State == "ready" && (dl.LocalPath != "" || dl.CleanAudioPath != "") {
		if kind == feedpb.Variant_VARIANT_VIDEO && req.Msg.Variant != feedpb.Variant_VARIANT_AUDIO {
			switch videoPlayOf(dl) {
			case playHLS:
				master, err := s.plugin.HLSMasterURL(store, plugin.UserFromContext(ctx).Username, id, hlsOptions{})
				if err != nil {
					return nil, err
				}
				return connect.NewResponse(&feedpb.MediaSource{
					Url: master, Mime: hlsMIME, Variant: feedpb.Variant_VARIANT_VIDEO, Bytes: dl.Bytes,
					Cleaned: dl.CleanTrackPath != "",
				}), nil
			case playPreparing:
				return nil, connect.NewError(connect.CodeUnavailable, errors.New(preparingVideo))
			case playPackageFailed:
				return nil, s.downloadFirst(ctx, store, id, dl, "video")
			}
		}
		src := &feedpb.MediaSource{
			Url:     "/apps/feed/items/" + strconv.FormatInt(id, 10) + "/media",
			Variant: kind,
			Cleaned: dl.CleanAudioPath != "",
			Bytes:   dl.Bytes,
		}
		// Cleaned audio is audio, and so is the media route's file once
		// the download is tracks apart.
		if src.Cleaned || dl.PackageState == "done" || dl.VideoPath != "" {
			src.Variant = feedpb.Variant_VARIANT_AUDIO
		}
		return connect.NewResponse(src), nil
	}
	if ip.MediaURL == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("item %d has no media", id))
	}
	if dl.muted() {
		// Its clean audio is made from a download, and neither the
		// enclosure nor googlevideo's stream is clean.
		return nil, s.downloadFirst(ctx, store, id, dl, mediaNoun(audio))
	}
	if ip.SourceKind != "youtube" {
		return connect.NewResponse(&feedpb.MediaSource{Url: ip.MediaURL, Mime: ip.MediaMIME, Variant: kind}), nil
	}
	if !audio {
		username := plugin.UserFromContext(ctx).Username
		master, ok, err := s.plugin.remoteMaster(ctx, store, username, ip)
		if err != nil {
			return nil, err
		}
		if ok {
			return connect.NewResponse(&feedpb.MediaSource{Url: master, Mime: hlsMIME, Variant: feedpb.Variant_VARIANT_VIDEO}), nil
		}
		return nil, s.downloadFirst(ctx, store, id, dl, "video")
	}
	stream, err := resolveStream(ctx, ip.MediaURL, audio)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("resolve the YouTube stream: %w", err))
	}
	src := &feedpb.MediaSource{Url: stream, Variant: feedpb.Variant_VARIANT_VIDEO, ExpiresAt: timestamppb.New(streamExpiry(stream))}
	if audio {
		src.Variant = feedpb.Variant_VARIANT_AUDIO
	}
	return connect.NewResponse(src), nil
}

// hlsMIME is the media type of an HLS playlist.
const hlsMIME = "application/vnd.apple.mpegurl"

// preparingVideo is why a downloaded video cannot play while the packager
// has yet to make it HLS.
const preparingVideo = "Preparing the video"

// videoPlay is how a ready download's video plays.
type videoPlay int

const (
	// playFile: from the media route, as downloaded (a merged MP4 from
	// before tracks were kept apart, or a podcast's video).
	playFile videoPlay = iota
	// playHLS: from the signed master playlist.
	playHLS
	// playPreparing: not yet; the packager has the download's tracks to look at.
	playPreparing
	// playPackageFailed: not until it is downloaded again; the tracks could
	// not be made HLS, and there is no single file with both picture and
	// sound to fall back to.
	playPackageFailed
)

func videoPlayOf(dl Download) videoPlay {
	switch {
	case dl.CleanAudioPath != "" && dl.CleanTrackPath == "":
		// Clean audio not yet a track, or failed to become one: it plays
		// as audio from the media route, never HLS with the unclean audio.
		return playFile
	case dl.PackageState == "done" && dl.VideoPath != "":
		return playHLS
	case dl.VideoPath == "":
		return playFile
	case dl.PackageState == "error":
		return playPackageFailed
	}
	return playPreparing
}

// remoteEligible reports whether an item may play from googlevideo over HLS
// (hls_remote.go), from what is known without resolving: a YouTube video
// with a watch URL, not packaged, and not to be muted (Download.muted): its
// clean audio is made from a download, and googlevideo's is never clean.
// remoteMaster and the M3U route (m3u_serve.go) both decide by it.
func remoteEligible(sourceKind, itemType, watchURL string, dl Download) bool {
	return sourceKind == "youtube" && itemType == "video" && watchURL != "" &&
		dl.PackageState != "done" && !dl.muted()
}

// remoteMaster answers the signed master of an undownloaded YouTube video
// after checking both tracks resolve to indexed fMP4; ok is false (err nil)
// when they don't, and the caller falls back to downloading.
//
// It is also false for an item that is not remoteEligible, such as one that
// is to be muted. A client that goes away meanwhile gets ctx's error, never
// a download.
func (p *Plugin) remoteMaster(ctx context.Context, store *Store, username string, it ItemPlayback) (master string, ok bool, err error) {
	itemID, watchURL := it.ID, it.MediaURL
	dl, err := store.FindDownload(itemID)
	if err != nil {
		return "", false, err
	}
	if !remoteEligible(it.SourceKind, it.Type, watchURL, dl) {
		p.youtubeLog().Info("youtube remote: item cannot play remote; downloading", "item", itemID, "muted", dl.muted())
		return "", false, nil
	}
	if _, err := p.remoteYouTube(ctx, store, username, itemID, watchURL, false); err != nil {
		if ctx.Err() != nil {
			// The client went away; googlevideo did not fail, so there
			// is nothing to fall back from.
			return "", false, fmt.Errorf("youtube item %d: %w", itemID, ctx.Err())
		}
		if !errors.Is(err, errUpstream) {
			return "", false, err
		}
		// No index, more than one track, a parse or resolve error, or a
		// refusal that a fresh resolve did not cure.
		p.youtubeLog().Info("youtube remote: cannot play over HLS; downloading", "item", itemID, "error", err)
		return "", false, nil
	}
	master, err = p.HLSMasterURL(store, username, itemID, hlsOptions{})
	if err != nil {
		return "", false, err
	}
	return master, true, nil
}

// downloadFirst queues an item's download and answers why its video or
// audio (what) cannot play yet.
func (s *FeedService) downloadFirst(ctx context.Context, store *Store, id int64, dl Download, what string) error {
	why, err := queueDownload(store, s.plugin, plugin.UserFromContext(ctx).Username, id, dl, what)
	if err != nil {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, errors.New(why))
}

// queueDownload queues an item's download, retrying one that failed to
// download or to package, and answers why its video or audio (what) cannot
// play yet.
func queueDownload(store *Store, p *Plugin, username string, id int64, dl Download, what string) (string, error) {
	if dl.State != "downloading" {
		if err := store.EnqueueDownload(id); err != nil {
			return "", err
		}
		p.TriggerFetch(username, 0)
	}
	if dl.State == "error" {
		return "Downloading the " + what + " again; the last try failed: " + dl.Error, nil
	}
	if dl.State == "ready" && dl.PackageState == "error" {
		return "Downloading the " + what + " again; preparing it failed: " + dl.PackageError, nil
	}
	return "Downloading the " + what, nil
}

// mediaNoun names what a player asked for, in a reason it cannot play yet.
func mediaNoun(audio bool) string {
	if audio {
		return "audio"
	}
	return "video"
}

// waitClean answers why an item with no clean audio yet
// (Download.uncleanOnly) cannot play, queueing its download when it has
// none; audio is whether its audio, not its video, was asked for.
func (p *Plugin) waitClean(store *Store, username string, id int64, dl Download, audio bool) (string, error) {
	if dl.State == "ready" {
		return p.cleaningWhy(dl), nil
	}
	return queueDownload(store, p, username, id, dl, mediaNoun(audio))
}

// waitWhy is waitClean's reason without queueing anything, for a page that
// shows it before the player asks.
func (p *Plugin) waitWhy(dl Download) string {
	if dl.State == "ready" {
		return p.cleaningWhy(dl)
	}
	return "Downloading the audio"
}

// cleaningWhy says why a downloaded item has no clean audio yet: cleaning
// is under way, or it failed or cannot happen, which a retry may not cure.
func (p *Plugin) cleaningWhy(dl Download) string {
	switch {
	case dl.ProfanityError != "":
		return "Cleaning failed: " + firstLine(dl.ProfanityError)
	case dl.TranscribeError != "":
		return "Cleaning failed: " + firstLine(dl.TranscribeError)
	case p.Profanity() == nil:
		return "Cleaning failed: the profanity plugin is not installed"
	case dl.CaptionsPath == "" && !dl.NeedsTranscribe:
		// Transcription ended without captions, as it does with whisper off.
		return "Cleaning failed: no captions to find the words by"
	}
	return "Cleaning the audio"
}

// firstLine is the first line of a stored error, at most 200 bytes of it:
// a tool's error can carry its whole output.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = strings.ToValidUTF8(s[:200], "") + "…"
	}
	return s
}

// streamExpiry reads when a resolved stream stops working from its expire
// parameter; without one, assume an hour.
func streamExpiry(stream string) time.Time {
	if u, err := url.Parse(stream); err == nil {
		if sec, err := strconv.ParseInt(u.Query().Get("expire"), 10, 64); err == nil {
			return time.Unix(sec, 0)
		}
	}
	return time.Now().Add(time.Hour)
}
