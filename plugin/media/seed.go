package media

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"
)

var seedEpisodes = []struct {
	title    string
	duration string
	body     string
}{
	{"The Art of Simplicity", "12:34", "Great software is not about adding features — it is about removing everything that isn't essential."},
	{"Debugging Distributed Systems", "28:15", "When multiple services are talking to each other, the hardest bugs are the ones that only appear under specific timing conditions."},
	{"Why I Switched to Go", "15:42", "After years of dynamic languages, the simplicity and reliability of Go's type system won me over."},
	{"Understanding Memory Allocations", "22:08", "Every allocation has a cost. Understanding where your program allocates helps you write faster code."},
	{"The Future of WebAssembly", "31:20", "WebAssembly is not just for the browser anymore. Server-side WASM is opening up new possibilities."},
	{"SQLite: The Most Deployed Database", "18:55", "SQLite runs on billions of devices. Its design philosophy of simplicity and reliability holds lessons for all of us."},
	{"Building CLI Tools That Spark Joy", "14:30", "A good CLI tool should be discoverable, composable, and just fast enough that you never notice it."},
	{"Podcast Recording Tips", "09:45", "Good audio starts with a quiet room and a decent microphone. Everything else is polish."},
}

// seedServer starts an HTTP server that serves a podcast RSS feed.
func seedServer() *httptest.Server {
	var seq atomic.Int64
	mux := http.NewServeMux()

	mux.HandleFunc("GET /feed", func(w http.ResponseWriter, r *http.Request) {
		n := seq.Add(1)
		ep := seedEpisodes[rand.IntN(len(seedEpisodes))]
		slug := strings.ReplaceAll(strings.ToLower(ep.title), " ", "-")
		pubTime := time.Now().Add(-time.Duration(n) * time.Hour)

		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Cog Dev Podcast (seed)</title>
  <link>%s</link>
  <description>Seed podcast for Cog development.</description>
  <itunes:image href="%s/cover.jpg"/>
  <item>
    <title>%s</title>
    <link>%s/episode/%s</link>
    <guid>seed-%d-%s</guid>
    <pubDate>%s</pubDate>
    <description>%s</description>
    <enclosure url="%s/audio/%s.mp3" type="audio/mpeg" length="1024000"/>
    <itunes:duration>%s</itunes:duration>
  </item>
</channel>
</rss>`,
			"http://"+r.Host,
			"http://"+r.Host,
			ep.title,
			"http://"+r.Host, slug,
			n, slug,
			pubTime.Format(time.RFC1123Z),
			ep.body,
			"http://"+r.Host, slug,
			ep.duration,
		)
	})

	mux.HandleFunc("GET /audio/", func(w http.ResponseWriter, r *http.Request) {
		// Serve a tiny placeholder "audio" response.
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("fake-audio-data"))
	})

	return httptest.NewServer(mux)
}

// Seed creates sample media source subscriptions for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed media: %w", err)
	}

	// Check if already seeded.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&count)
	if count > 0 {
		return nil
	}

	srv := seedServer()
	// Server stays open so the poller can reach it.

	group, err := CreateGroup(db, "Podcasts")
	if err != nil {
		return fmt.Errorf("seed media group: %w", err)
	}

	feedURL := srv.URL + "/feed"
	src, err := CreateSource(db, SourcePodcast, feedURL, sql.NullInt64{Int64: group.ID, Valid: true})
	if err != nil {
		return fmt.Errorf("seed media source: %w", err)
	}
	UpdateSourceMeta(db, src.ID, "Cog Dev Podcast (seed)", "Seed podcast for development", "")

	// Insert a few initial episodes so seed data is immediately visible.
	picked := rand.Perm(len(seedEpisodes))[:3]
	now := p.ctx.Now()
	for i, idx := range picked {
		ep := seedEpisodes[idx]
		slug := strings.ReplaceAll(strings.ToLower(ep.title), " ", "-")
		hash := sha256.Sum256([]byte(slug))
		guid := fmt.Sprintf("seed-init-%d-%x", i, hash[:4])
		link := srv.URL + "/audio/" + slug + ".mp3"
		pubAt := now.Add(-time.Duration(i) * time.Hour).Unix()
		epObj := &Episode{
			SourceID:    src.ID,
			GUID:        guid,
			Title:       ep.title,
			Description: ep.body,
			MediaURL:    link,
			MediaType:   MediaAudio,
			DurationSec: parseDuration(ep.duration),
			PublishedAt: pubAt,
		}
		id, err := UpsertEpisode(db, epObj)
		if err != nil {
			p.log.Error("seed episode", "title", ep.title, "error", err)
			continue
		}
		AutoTagEpisode(db, id, MediaAudio)
		// Add the first episode to the queue.
		if i == 0 {
			AddToQueue(db, id)
		}
	}

	p.log.Info("seeded media source", "name", "Cog Dev Podcast (seed)", "initial", 3)
	return nil
}
