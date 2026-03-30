package bookmarks

import (
	"fmt"
)

var seedBookmarks = []struct {
	url   string
	title string
	desc  string
	tags  []string
}{
	{"https://go.dev", "The Go Programming Language", "Official Go website", []string{"go", "dev"}},
	{"https://pkg.go.dev", "Go Packages", "Search and discover Go packages", []string{"go", "reference"}},
	{"https://gobyexample.com", "Go by Example", "Hands-on introduction to Go using annotated example programs", []string{"go", "tutorial"}},
	{"https://github.com", "GitHub", "Where the world builds software", []string{"dev", "tools"}},
	{"https://news.ycombinator.com", "Hacker News", "Social news for hackers", []string{"news", "tech"}},
	{"https://lobste.rs", "Lobsters", "Computing-focused community", []string{"news", "tech"}},
	{"https://sqlite.org", "SQLite", "Small fast self-contained SQL database engine", []string{"database", "reference"}},
	{"https://htmx.org", "htmx", "High power tools for HTML", []string{"dev", "frontend"}},
	{"https://templ.guide", "templ", "Go HTML templating language", []string{"go", "frontend"}},
	{"https://developer.mozilla.org", "MDN Web Docs", "Resources for developers by developers", []string{"reference", "frontend"}},
}

// Seed creates sample bookmarks for a user.
func (p *Plugin) Seed(username string) error {
	db, err := p.DB(username)
	if err != nil {
		return fmt.Errorf("seed bookmarks: %w", err)
	}

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM bookmarks`).Scan(&count)
	if count > 0 {
		return nil
	}

	// Create a couple of folders.
	devFolder, _ := CreateFolder(db, "Development", nil)
	_, _ = CreateFolder(db, "Reading", nil)

	for _, sb := range seedBookmarks {
		var folders []int64
		for _, t := range sb.tags {
			if t == "dev" || t == "go" || t == "frontend" {
				if devFolder != nil {
					folders = []int64{devFolder.ID}
				}
				break
			}
		}
		if _, err := CreateBookmark(db, sb.url, sb.title, sb.desc, sb.tags, folders); err != nil {
			p.log.Error("seed bookmark failed", "url", sb.url, "error", err)
		}
	}

	p.log.Info("seeded bookmarks", "count", len(seedBookmarks), "user", username)
	return nil
}
