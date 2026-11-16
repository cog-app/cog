package feed

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// youtubeRefreshMargin is how long before a URL expires youtubeTracks
// resolves anew.
const youtubeRefreshMargin = 10 * time.Minute

// youtubeResolveTimeout bounds a resolve shared by several requests, which
// must not die with the first request's context.
const youtubeResolveTimeout = 2 * time.Minute

// YouTubeStreams returns an item's cached tracks; ok is true when both rows
// are present.
func (s *Store) YouTubeStreams(itemID int64) (video, audio YouTubeTrack, ok bool, err error) {
	rows, err := s.db.Query(
		`SELECT kind, format_id, url, expires_at FROM youtube_streams WHERE item_id = ?`, itemID)
	if err != nil {
		return video, audio, false, fmt.Errorf("youtube streams: %w", err)
	}
	defer rows.Close()
	var haveVideo, haveAudio bool
	for rows.Next() {
		var t YouTubeTrack
		var kind string
		var expires int64
		if err := rows.Scan(&kind, &t.FormatID, &t.URL, &expires); err != nil {
			return video, audio, false, fmt.Errorf("youtube streams: %w", err)
		}
		t.Kind = hls.Kind(kind)
		t.ExpiresAt = time.Unix(expires, 0)
		switch t.Kind {
		case hls.Video:
			video, haveVideo = t, true
		case hls.Audio:
			audio, haveAudio = t, true
		}
	}
	if err := rows.Err(); err != nil {
		return video, audio, false, fmt.Errorf("youtube streams: %w", err)
	}
	return video, audio, haveVideo && haveAudio, nil
}

// SaveYouTubeStreams replaces an item's cached tracks, both in one transaction.
func (s *Store) SaveYouTubeStreams(itemID int64, video, audio YouTubeTrack) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("save youtube streams: %w", err)
	}
	defer tx.Rollback()
	for _, t := range []YouTubeTrack{video, audio} {
		_, err := tx.Exec(
			`INSERT INTO youtube_streams(item_id, kind, format_id, url, expires_at) VALUES(?, ?, ?, ?, ?)
			 ON CONFLICT(item_id, kind) DO UPDATE SET format_id = excluded.format_id,
			   url = excluded.url, expires_at = excluded.expires_at`,
			itemID, string(t.Kind), t.FormatID, t.URL, t.ExpiresAt.Unix())
		if err != nil {
			return fmt.Errorf("save youtube streams: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("save youtube streams: %w", err)
	}
	return nil
}

// ForgetYouTubeStreams drops an item's cached tracks.
func (s *Store) ForgetYouTubeStreams(itemID int64) error {
	if _, err := s.db.Exec(`DELETE FROM youtube_streams WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("forget youtube streams: %w", err)
	}
	return nil
}

// resolveTracks is ResolveYouTubeTracks; tests replace it (SetTrackResolver).
var resolveTracks = ResolveYouTubeTracks

// youtubeTracks returns the item's cached tracks, resolving when none are
// cached, when either expires within 10 minutes of now, or when refresh is
// set. A resolve with tracks cached pins their format ids, so the segment
// index made against them stays valid. Concurrent calls for one user/item
// share one resolve.
func (p *Plugin) youtubeTracks(ctx context.Context, store *Store, username string, itemID int64, watchURL string, now time.Time, refresh bool) (video, audio YouTubeTrack, err error) {
	fresh := func() (v, a YouTubeTrack, cached, ok bool, err error) {
		v, a, cached, err = store.YouTubeStreams(itemID)
		if err != nil {
			return v, a, false, false, err
		}
		limit := now.Add(youtubeRefreshMargin)
		return v, a, cached, cached && v.ExpiresAt.After(limit) && a.ExpiresAt.After(limit), nil
	}
	v, a, _, ok, err := fresh()
	if err != nil {
		return video, audio, err
	}
	if ok && !refresh {
		return v, a, nil
	}
	type pair struct{ video, audio YouTubeTrack }
	res, err, _ := p.youtubeFlight.Do(username+"/"+strconv.FormatInt(itemID, 10), func() (any, error) {
		// A flight that finished since the check above has saved fresh tracks.
		v, a, cached, ok, err := fresh()
		if err != nil {
			return nil, err
		}
		if ok && !refresh {
			return pair{v, a}, nil
		}
		format := YouTubeFormat(YouTubeVideoFormat, YouTubeAudioFormat)
		if cached {
			format = YouTubeFormat(v.FormatID, a.FormatID)
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), youtubeResolveTimeout)
		defer cancel()
		v, a, err = resolveTracks(rctx, watchURL, format)
		if err != nil {
			return nil, fmt.Errorf("resolve youtube tracks: %w", err)
		}
		if err := store.SaveYouTubeStreams(itemID, v, a); err != nil {
			return nil, err
		}
		return pair{v, a}, nil
	})
	if err != nil {
		return video, audio, err
	}
	r := res.(pair)
	return r.video, r.audio, nil
}
