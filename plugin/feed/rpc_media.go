package feed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
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
// not downloaded plays its audio from a resolved stream. Its video is
// downloaded first: YouTube serves picture and sound apart, and only HLS
// presents them together, so asking for it queues the download and answers
// Unavailable until it is downloaded and packaged, which a client asks again
// on. Any other enclosure plays from its origin.
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
				return nil, s.downloadFirst(ctx, store, id, dl)
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
	if ip.SourceKind != "youtube" {
		return connect.NewResponse(&feedpb.MediaSource{Url: ip.MediaURL, Mime: ip.MediaMIME, Variant: kind}), nil
	}
	audio := req.Msg.Variant == feedpb.Variant_VARIANT_AUDIO || ip.Type == "audio"
	if !audio {
		return nil, s.downloadFirst(ctx, store, id, dl)
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

// downloadFirst queues an item's video and answers why it cannot play yet.
func (s *FeedService) downloadFirst(ctx context.Context, store *Store, id int64, dl Download) error {
	why, err := queueVideo(store, s.plugin, plugin.UserFromContext(ctx).Username, id, dl)
	if err != nil {
		return err
	}
	return connect.NewError(connect.CodeUnavailable, errors.New(why))
}

// queueVideo queues an item's download, retrying one that failed to
// download or to package, and answers why the video cannot play yet.
func queueVideo(store *Store, p *Plugin, username string, id int64, dl Download) (string, error) {
	if dl.State != "downloading" {
		if err := store.EnqueueDownload(id); err != nil {
			return "", err
		}
		p.TriggerFetch(username, 0)
	}
	if dl.State == "error" {
		return "Downloading the video again; the last try failed: " + dl.Error, nil
	}
	if dl.State == "ready" && dl.PackageState == "error" {
		return "Downloading the video again; preparing it failed: " + dl.PackageError, nil
	}
	return "Downloading the video", nil
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
