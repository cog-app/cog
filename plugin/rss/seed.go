//go:build seed

package rss

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

var seedArticles = []struct {
	title string
	body  string
}{
	{"Clear is better than clever", "Code is read many more times than it is written. A little verbosity is a small price to pay for clarity."},
	{"Constant errors", "Sentinel errors in Go should be constant values, not variables that can be mutated at runtime."},
	{"The Zen of Go", "Each package fulfils a single purpose. Handle errors explicitly. Return early rather than nesting deeply."},
	{"Do not fear first class functions", "Functions are values in Go. Pass them around, store them in structs, return them from other functions."},
	{"Understand Go pointers in less than 800 words", "A pointer is a value that points to the memory address of another variable. That is really all there is to it."},
	{"Prefer table driven tests", "Table driven tests reduce boilerplate and make it trivial to add new cases to existing test suites."},
	{"Errors and exceptions", "Go does not have exceptions. Instead, functions return errors as ordinary values that callers must handle."},
	{"Struct composition with Go", "Embedding lets you compose types without inheritance. It is one of Go's most powerful design tools."},
	{"Channel axioms", "A send to a nil channel blocks forever. A receive from a nil channel blocks forever. Know your axioms."},
	{"Simplicity debt", "Simplicity is not a goal; it is a byproduct of good design. Complexity is the debt you pay for shortcuts."},
	{"Ice cream makers and data races", "Data races are like ice cream makers left running overnight: everything looks fine until the mess appears."},
	{"Context is for cancelation", "Do not store values in context for convenience. Context carries deadlines, cancelation signals, and request-scoped data."},
	{"Avoid package level state", "Global state couples packages together and makes testing difficult. Pass dependencies explicitly instead."},
	{"Internals of slices", "A slice header is three words: a pointer, a length, and a capacity. Understanding this explains append behavior."},
	{"GOPATH is deprecated", "Modules replaced GOPATH. If you are still setting GOPATH, you can stop now."},
	{"Fatih's contribution to Go", "The Go community thrives because individuals like Fatih Arslan build tools that everyone depends on daily."},
}

// seedServer starts an HTTP server that serves an RSS feed with a new random
// article appended on each fetch, plus an article page endpoint.
func seedServer() *httptest.Server {
	var seq atomic.Int64
	mux := http.NewServeMux()

	mux.HandleFunc("GET /article/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		idx := r.PathValue("id")
		// Find article by matching guid suffix; just serve a simple page.
		for _, a := range seedArticles {
			slug := strings.ReplaceAll(strings.ToLower(a.title), " ", "-")
			if idx == slug {
				fmt.Fprintf(w, "<html><head><title>%s</title></head><body><h1>%s</h1><p>%s</p></body></html>",
					a.title, a.title, a.body)
				return
			}
		}
		http.NotFound(w, r)
	})

	mux.HandleFunc("GET /feed", func(w http.ResponseWriter, r *http.Request) {
		n := seq.Add(1)
		// Pick a random article for this fetch.
		a := seedArticles[rand.IntN(len(seedArticles))]
		slug := strings.ReplaceAll(strings.ToLower(a.title), " ", "-")
		pubTime := time.Now().Add(-time.Duration(n) * time.Minute)

		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
<channel>
  <title>Go Blog (seed)</title>
  <link>%s</link>
  <description>Seed data for Cog development.</description>
  <item>
    <title>%s</title>
    <link>%s/article/%s</link>
    <guid>seed-%d-%s</guid>
    <pubDate>%s</pubDate>
    <description>%s</description>
  </item>
</channel>
</rss>`, r.Host, a.title, "http://"+r.Host, slug, n, slug,
			pubTime.Format(time.RFC1123Z), a.body)
	})

	return httptest.NewServer(mux)
}

// Seed creates sample RSS feed subscriptions for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed rss: %w", err)
	}

	// Check if already seeded.
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM feeds`).Scan(&count)
	if count > 0 {
		return nil
	}

	srv := seedServer()
	// srv stays open for the lifetime of the process so the poller can reach it.

	folder, err := CreateFolder(db, "Blogs")
	if err != nil {
		return fmt.Errorf("seed rss folder: %w", err)
	}

	feedURL := srv.URL + "/feed"
	feed, err := CreateFeed(db, feedURL, sql.NullInt64{Int64: folder.ID, Valid: true})
	if err != nil {
		return fmt.Errorf("seed rss feed: %w", err)
	}
	if err := RenameFeed(db, feed.ID, "Go Blog (seed)"); err != nil {
		return fmt.Errorf("seed rss rename: %w", err)
	}

	// Insert a few initial articles so seed data is immediately visible.
	picked := rand.Perm(len(seedArticles))[:3]
	now := time.Now()
	for i, idx := range picked {
		a := seedArticles[idx]
		slug := strings.ReplaceAll(strings.ToLower(a.title), " ", "-")
		guid := fmt.Sprintf("seed-init-%d-%s", i, slug)
		link := srv.URL + "/article/" + slug
		pubAt := now.Add(-time.Duration(i) * time.Hour).Unix()
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(a.body)))
		db.Exec(`INSERT INTO items (feed_id, guid, title, url, content_html, published_at, content_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			feed.ID, guid, a.title, link, a.body, pubAt, hash)
	}

	p.log.Info("seeded RSS feed", "name", "Go Blog (seed)", "initial", 3)
	return nil
}
