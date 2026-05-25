package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestBuildRecord(t *testing.T) {
	it := feed.Item{
		Title: "Hello", Author: "Ada", ContentHTML: "body",
		Type: "audio", URL: "https://x/", PublishedAt: 1700000000,
	}
	sub := feed.Subscription{Kind: "rss", Title: "Daily", URL: "https://x/feed"}

	rec := feed.BuildRecord(it, sub, "Development", []string{"sunday", "weekly"}, true, false, true)

	wantSingle := map[string]string{
		"title":        "Hello",
		"author":       "Ada",
		"content":      "body",
		"type":         "audio",
		"kind":         "rss",
		"folder":       "Development",
		"subscription": "Daily",
	}
	for k, v := range wantSingle {
		got := rec[k]
		if len(got) != 1 || got[0] != v {
			t.Errorf("%s: got %v want [%q]", k, got, v)
		}
	}

	if tags := rec["tag"]; len(tags) != 2 || tags[0] != "sunday" || tags[1] != "weekly" {
		t.Errorf("tag: got %v", tags)
	}
	if is := rec["is"]; len(is) != 2 || is[0] != "unread" || is[1] != "downloaded" {
		t.Errorf("is: got %v", is)
	}
	if pub := rec["published"]; len(pub) != 1 || pub[0] == "" {
		t.Errorf("published not populated: %v", pub)
	}
}

func TestBuildRecordEmptyFolderAndTags(t *testing.T) {
	rec := feed.BuildRecord(
		feed.Item{Title: "x", Type: "text"},
		feed.Subscription{Kind: "url"},
		"", nil, false, false, false,
	)
	if _, has := rec["folder"]; has {
		t.Error("empty folder should not be present")
	}
	if _, has := rec["tag"]; has {
		t.Error("empty tags should not be present")
	}
	if _, has := rec["is"]; has {
		t.Error("no status flags should mean no 'is' key")
	}
}

func TestBuildRecordMatchesGrammar(t *testing.T) {
	rec := feed.BuildRecord(
		feed.Item{Title: "Morning Brew", Type: "audio"},
		feed.Subscription{Kind: "rss", Title: "Daily"},
		"Development",
		[]string{"sunday", "podcast"},
		true, false, true,
	)
	expr, err := query.Parse("type:audio AND folder:Development AND tag:sunday")
	if err != nil {
		t.Fatal(err)
	}
	if !expr.Match(rec) {
		t.Fatal("expected match")
	}

	expr2, _ := query.Parse("is:downloaded AND kind:rss")
	if !expr2.Match(rec) {
		t.Fatal("expected match on is:downloaded AND kind:rss")
	}

	expr3, _ := query.Parse("tag:never")
	if expr3.Match(rec) {
		t.Fatal("expected no match")
	}
}
