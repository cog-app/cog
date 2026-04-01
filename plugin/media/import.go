package media

import (
	"archive/zip"
	"database/sql"
	"encoding/csv"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ImportResult summarizes what was imported.
type ImportResult struct {
	Sources   int
	Episodes  int
	Completed int
}

func (r ImportResult) String() string {
	parts := []string{fmt.Sprintf("%d sources", r.Sources)}
	if r.Episodes > 0 {
		parts = append(parts, fmt.Sprintf("%d episodes", r.Episodes))
	}
	if r.Completed > 0 {
		parts = append(parts, fmt.Sprintf("%d marked completed", r.Completed))
	}
	return strings.Join(parts, ", ")
}

// --- OPML ---

type opml struct {
	XMLName xml.Name `xml:"opml"`
	Body    opmlBody `xml:"body"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	Outlines []opmlOutline `xml:"outline,omitempty"`
}

// ImportOPML parses OPML XML and creates podcast sources.
func ImportOPML(db *sql.DB, data []byte, groupID sql.NullInt64) (ImportResult, error) {
	var doc opml
	if err := xml.Unmarshal(data, &doc); err != nil {
		return ImportResult{}, fmt.Errorf("invalid OPML: %w", err)
	}

	var result ImportResult
	for _, outline := range doc.Body.Outlines {
		if outline.XMLURL != "" {
			if _, err := CreateSource(db, SourcePodcast, outline.XMLURL, groupID); err == nil {
				result.Sources++
				title := outline.Title
				if title == "" {
					title = outline.Text
				}
				if title != "" {
					// Best-effort title update.
					sources, _ := ListSources(db)
					for _, s := range sources {
						if s.URL == outline.XMLURL && s.Title == "" {
							UpdateSourceMeta(db, s.ID, title, "", "")
						}
					}
				}
			}
			continue
		}

		// Folder → group.
		var gid sql.NullInt64
		if outline.Text != "" {
			if g, err := CreateGroup(db, outline.Text); err == nil {
				gid = sql.NullInt64{Int64: g.ID, Valid: true}
			} else {
				groups, _ := ListGroups(db)
				for _, g := range groups {
					if g.Name == outline.Text {
						gid = sql.NullInt64{Int64: g.ID, Valid: true}
						break
					}
				}
			}
		}

		for _, child := range outline.Outlines {
			if child.XMLURL == "" {
				continue
			}
			effectiveGroup := gid
			if !effectiveGroup.Valid {
				effectiveGroup = groupID
			}
			if _, err := CreateSource(db, SourcePodcast, child.XMLURL, effectiveGroup); err == nil {
				result.Sources++
			}
		}
	}

	return result, nil
}

// --- AntennaPod ---

// ImportAntennaPod reads an AntennaPod SQLite database and creates podcast sources.
// Also imports listen history to mark completed episodes.
func ImportAntennaPod(db *sql.DB, dbPath string, groupID sql.NullInt64, now int64) (ImportResult, error) {
	apDB, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return ImportResult{}, fmt.Errorf("open AntennaPod db: %w", err)
	}
	defer apDB.Close()

	var result ImportResult

	// Import feeds as sources.
	feedRows, err := apDB.Query(`SELECT id, download_url, title, image_url FROM Feeds WHERE download_url != ''`)
	if err != nil {
		return result, fmt.Errorf("query AntennaPod feeds: %w", err)
	}
	defer feedRows.Close()

	// Map AntennaPod feed ID → our source ID.
	feedMap := make(map[int64]int64)
	for feedRows.Next() {
		var apFeedID int64
		var feedURL, title, imageURL string
		if err := feedRows.Scan(&apFeedID, &feedURL, &title, &imageURL); err != nil {
			continue
		}
		src, err := CreateSource(db, SourcePodcast, feedURL, groupID)
		if err != nil {
			// Already exists — find it.
			sources, _ := ListSources(db)
			for _, s := range sources {
				if s.URL == feedURL {
					feedMap[apFeedID] = s.ID
					break
				}
			}
			continue
		}
		result.Sources++
		if title != "" {
			UpdateSourceMeta(db, src.ID, title, "", imageURL)
		}
		feedMap[apFeedID] = src.ID
	}

	// Import played episodes.
	itemRows, err := apDB.Query(`SELECT feed, link, title, pubDate FROM FeedItems
		WHERE (read = 1 OR media_played_duration > 0) AND link != ''`)
	if err != nil {
		return result, nil // non-fatal
	}
	defer itemRows.Close()

	for itemRows.Next() {
		var apFeedID int64
		var link, title string
		var pubDate int64
		if err := itemRows.Scan(&apFeedID, &link, &title, &pubDate); err != nil {
			continue
		}
		sourceID, ok := feedMap[apFeedID]
		if !ok {
			continue
		}
		ep := &Episode{
			SourceID:    sourceID,
			GUID:        link,
			Title:       title,
			MediaURL:    link,
			MediaType:   MediaAudio,
			PublishedAt: pubDate / 1000, // AntennaPod uses millis
		}
		id, err := UpsertEpisode(db, ep)
		if err != nil {
			continue
		}
		result.Episodes++
		MarkCompleted(db, id, now)
		result.Completed++
	}

	return result, nil
}

// --- YouTube Takeout CSV ---

// ImportYouTubeTakeout parses a Google Takeout subscriptions.csv.
func ImportYouTubeTakeout(db *sql.DB, data []byte, groupID sql.NullInt64) (ImportResult, error) {
	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		return ImportResult{}, fmt.Errorf("invalid CSV: %w", err)
	}

	var result ImportResult
	for i, row := range records {
		if i == 0 {
			continue // skip header
		}
		if len(row) < 3 {
			continue
		}
		channelID := strings.TrimSpace(row[0])
		channelURL := strings.TrimSpace(row[1])
		channelTitle := strings.TrimSpace(row[2])

		if channelURL == "" && channelID != "" {
			channelURL = "https://www.youtube.com/channel/" + channelID
		}
		if channelURL == "" {
			continue
		}

		src, err := CreateSource(db, SourceYouTube, channelURL, groupID)
		if err != nil {
			continue
		}
		result.Sources++
		if channelTitle != "" {
			UpdateSourceMeta(db, src.ID, channelTitle, "", "")
		}
	}

	return result, nil
}

// --- NewPipe ---

// ImportNewPipe reads a NewPipe ZIP export and creates YouTube sources.
// Also imports watch history to mark completed episodes.
func ImportNewPipe(db *sql.DB, zipPath string, groupID sql.NullInt64, now int64) (ImportResult, error) {
	// Extract newpipe.db from the ZIP.
	dbPath, cleanup, err := extractNewPipeDB(zipPath)
	if err != nil {
		return ImportResult{}, err
	}
	defer cleanup()

	npDB, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return ImportResult{}, fmt.Errorf("open NewPipe db: %w", err)
	}
	defer npDB.Close()

	var result ImportResult

	// Import subscriptions.
	subRows, err := npDB.Query(`SELECT url, name FROM subscriptions WHERE service_id = 0 AND url != ''`)
	if err != nil {
		return result, fmt.Errorf("query NewPipe subscriptions: %w", err)
	}
	defer subRows.Close()

	// Map NewPipe channel URL → our source ID.
	channelMap := make(map[string]int64)
	for subRows.Next() {
		var channelURL, name string
		if err := subRows.Scan(&channelURL, &name); err != nil {
			continue
		}
		src, err := CreateSource(db, SourceYouTube, channelURL, groupID)
		if err != nil {
			// Already exists — find it.
			sources, _ := ListSources(db)
			for _, s := range sources {
				if s.URL == channelURL {
					channelMap[channelURL] = s.ID
					break
				}
			}
			continue
		}
		result.Sources++
		if name != "" {
			UpdateSourceMeta(db, src.ID, name, "", "")
		}
		channelMap[channelURL] = src.ID
	}

	// Import watch history.
	histRows, err := npDB.Query(`SELECT s.url, s.title, s.duration, s.uploader_url
		FROM streams s
		JOIN stream_history sh ON sh.stream_id = s.uid
		WHERE s.service_id = 0 AND s.url != ''`)
	if err != nil {
		return result, nil // non-fatal
	}
	defer histRows.Close()

	for histRows.Next() {
		var videoURL, title, uploaderURL string
		var duration int64
		if err := histRows.Scan(&videoURL, &title, &duration, &uploaderURL); err != nil {
			continue
		}

		// Find the source for this video's channel.
		sourceID, ok := channelMap[uploaderURL]
		if !ok {
			// Try to match any known source.
			for chURL, sID := range channelMap {
				if strings.Contains(uploaderURL, chURL) || strings.Contains(chURL, uploaderURL) {
					sourceID = sID
					ok = true
					break
				}
			}
		}
		if !ok {
			continue
		}

		videoID := extractYouTubeVideoID(videoURL)
		if videoID == "" {
			continue
		}

		ep := &Episode{
			SourceID:     sourceID,
			GUID:         videoID,
			Title:        title,
			MediaURL:     videoURL,
			MediaType:    MediaVideo,
			DurationSec:  int(duration),
			ThumbnailURL: fmt.Sprintf("https://i.ytimg.com/vi/%s/hqdefault.jpg", videoID),
			PublishedAt:  now,
		}
		id, err := UpsertEpisode(db, ep)
		if err != nil {
			continue
		}
		result.Episodes++
		MarkCompleted(db, id, now)
		result.Completed++
	}

	return result, nil
}

func extractNewPipeDB(zipPath string) (string, func(), error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", func() {}, fmt.Errorf("open NewPipe zip: %w", err)
	}

	for _, f := range r.File {
		if filepath.Base(f.Name) == "newpipe.db" {
			rc, err := f.Open()
			if err != nil {
				r.Close()
				return "", func() {}, err
			}
			tmpFile, err := os.CreateTemp("", "newpipe-*.db")
			if err != nil {
				rc.Close()
				r.Close()
				return "", func() {}, err
			}
			io.Copy(tmpFile, rc)
			rc.Close()
			r.Close()
			tmpPath := tmpFile.Name()
			tmpFile.Close()
			return tmpPath, func() { os.Remove(tmpPath) }, nil
		}
	}

	r.Close()
	return "", func() {}, fmt.Errorf("newpipe.db not found in ZIP")
}

func extractYouTubeVideoID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Host == "youtu.be" {
		return strings.TrimPrefix(u.Path, "/")
	}
	if strings.Contains(u.Host, "youtube.com") {
		return u.Query().Get("v")
	}
	return ""
}

// nowUnix is a helper for tests; production code passes time.Now().Unix().
var _ = time.Now // ensure import
