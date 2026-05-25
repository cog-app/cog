package feed_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

const sampleOPML = `<?xml version="1.0"?>
<opml version="2.0">
  <head><title>my subs</title></head>
  <body>
    <outline text="Tech">
      <outline text="Go Blog" title="Go Blog" type="rss" xmlUrl="https://go.dev/blog/feed.atom" htmlUrl="https://go.dev/blog/"/>
      <outline text="Hacker News" type="rss" xmlUrl="https://hnrss.org/frontpage" htmlUrl="https://news.ycombinator.com/"/>
    </outline>
    <outline text="Lone Feed" type="rss" xmlUrl="https://example.com/feed.xml" htmlUrl="https://example.com/"/>
  </body>
</opml>`

func TestImportOPML(t *testing.T) {
	s := newStore(t)
	n, err := s.ImportOPML([]byte(sampleOPML))
	require.NoError(t, err)
	if n != 3 {
		t.Fatalf("got %d created want 3", n)
	}

	subs, _ := s.ListSubscriptions()
	if len(subs) != 3 {
		t.Fatalf("got %d subscriptions", len(subs))
	}

	// Tech folder should exist with 2 subs in it.
	folders, _ := s.ListFolders()
	if len(folders) != 1 || folders[0].Name != "Tech" {
		t.Fatalf("got folders %+v", folders)
	}
	folderID := folders[0].ID

	var inFolder int
	for _, sub := range subs {
		if sub.Kind != "rss" {
			t.Errorf("kind: %s", sub.Kind)
		}
		if sub.FolderID.Valid && sub.FolderID.Int64 == folderID {
			inFolder++
		}
	}
	if inFolder != 2 {
		t.Errorf("expected 2 subs in Tech folder, got %d", inFolder)
	}
}

func TestImportOPMLSkipsDuplicates(t *testing.T) {
	s := newStore(t)
	_, _ = s.ImportOPML([]byte(sampleOPML))
	n, err := s.ImportOPML([]byte(sampleOPML))
	require.NoError(t, err)
	if n != 0 {
		t.Errorf("second import should add 0, got %d", n)
	}
}

func TestExportOPMLRoundTrip(t *testing.T) {
	s := newStore(t)
	_, err := s.ImportOPML([]byte(sampleOPML))
	require.NoError(t, err)

	data, err := s.ExportOPML()
	require.NoError(t, err)

	str := string(data)
	if !strings.Contains(str, "Go Blog") {
		t.Error("Go Blog missing from export")
	}
	if !strings.Contains(str, "https://example.com/feed.xml") {
		t.Error("Lone Feed missing")
	}
	if !strings.Contains(str, `<outline text="Tech"`) {
		t.Error("Tech folder missing")
	}
}

func TestExportOPMLExcludesNonRSS(t *testing.T) {
	s := newStore(t)
	_, _ = s.CreateSubscription(feed.Subscription{
		Kind: "youtube", URL: "https://yt.example/c1", Title: "YT Chan",
	})
	_, _ = s.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://rss.example/feed", Title: "RSS Feed",
	})
	data, _ := s.ExportOPML()
	str := string(data)
	if strings.Contains(str, "yt.example") {
		t.Error("youtube subscription leaked into OPML")
	}
	if !strings.Contains(str, "rss.example") {
		t.Error("RSS subscription missing")
	}
}
