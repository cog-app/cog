package media

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// fetchYouTube runs yt-dlp to discover new videos from a YouTube channel or playlist.
func fetchYouTube(ctx context.Context, db *sql.DB, src *Source, clock func() time.Time) error {
	now := clock().Unix()

	// Fetch channel/playlist metadata for the source title.
	if src.Title == "" {
		if meta, err := ytdlpChannelMeta(ctx, src.URL); err == nil {
			title := ytSourceTitle(src.URL, meta)
			if title != "" {
				UpdateSourceMeta(db, src.ID, title, meta.Description, meta.Thumbnail)
				src.Title = title
			}
		}
	}

	entries, err := ytdlpListEntries(ctx, src.URL)
	if err != nil {
		return RecordSourceError(db, src, err, now)
	}

	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		webpageURL := entry.WebpageURL
		if webpageURL == "" {
			webpageURL = fmt.Sprintf("https://www.youtube.com/watch?v=%s", entry.ID)
		}
		thumb := entry.Thumbnail
		if thumb == "" {
			// yt-dlp --flat-playlist often omits thumbnails;
			// YouTube has a predictable thumbnail URL scheme.
			thumb = fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", entry.ID)
		}
		ep := &Episode{
			SourceID:     src.ID,
			GUID:         entry.ID,
			Title:        entry.Title,
			Description:  entry.Description,
			MediaURL:     webpageURL,
			MediaType:    MediaVideo,
			DurationSec:  int(entry.Duration),
			ThumbnailURL: thumb,
			PublishedAt:  now,
		}
		id, err := UpsertEpisode(db, ep)
		if err != nil {
			return fmt.Errorf("upsert youtube episode %q: %w", entry.ID, err)
		}
		if id > 0 {
			AutoTagEpisode(db, id, MediaVideo)
			QueueIfNew(db, id)
		}
	}

	return RecordSourceSuccess(db, src, "", "", now)
}

// ytSourceTitle picks the best display title for a YouTube source.
// Playlists use the playlist title; channels use the channel name.
func ytSourceTitle(url string, meta *ytdlpPlaylistMeta) string {
	if isYouTubePlaylist(url) {
		// Prefer playlist title over channel name.
		if meta.Title != "" {
			return meta.Title
		}
	}
	// Channel: prefer channel name, then uploader, then title.
	if meta.Channel != "" {
		return meta.Channel
	}
	if meta.Uploader != "" {
		return meta.Uploader
	}
	return meta.Title
}

// isYouTubePlaylist checks if the URL is a YouTube playlist (not a channel).
func isYouTubePlaylist(rawURL string) bool {
	return strings.Contains(rawURL, "playlist?list=") || strings.Contains(rawURL, "&list=")
}
