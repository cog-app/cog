package feed

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

// resolveStream turns a YouTube watch URL into a stream URL; tests replace it.
var resolveStream = ResolveYouTubeStreamURL

// ResolveMedia says where a client plays an item from. Media Cog holds (a
// download, or its profanity-cleaned audio) plays from the item's media
// route, a path on this server the client fetches with its own credentials.
// A YouTube video Cog has not downloaded is resolved to a stream, audio only
// for the audio variant; any other enclosure plays from its origin.
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
		src := &feedpb.MediaSource{
			Url:     "/apps/feed/items/" + strconv.FormatInt(id, 10) + "/media",
			Variant: kind,
			Cleaned: dl.CleanAudioPath != "",
			Bytes:   dl.Bytes,
		}
		if src.Cleaned {
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
	audio := req.Msg.Variant == feedpb.Variant_VARIANT_AUDIO
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
