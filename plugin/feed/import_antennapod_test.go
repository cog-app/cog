package feed_test

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"os"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// makeAntennaPodDB creates a minimal AntennaPod SQLite database in memory and
// returns its raw bytes. Schema matches the Feeds table used by ImportAntennaPod.
func makeAntennaPodDB(t *testing.T) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "ap-test-*.db")
	require.NoError(t, err)
	tmpPath := tmp.Name()
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmpPath) })

	db, err := sql.Open("sqlite3", tmpPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE Feeds (
		id INTEGER PRIMARY KEY,
		download_url TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		image_url TEXT NOT NULL DEFAULT ''
	)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO Feeds (download_url, title, image_url) VALUES
		('https://go.dev/blog/feed.atom', 'Go Blog', 'https://go.dev/logo.png'),
		('https://hnrss.org/frontpage', 'Hacker News', '')`)
	require.NoError(t, err)

	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err)
	return data
}

func TestImportAntennaPod(t *testing.T) {
	s := newStore(t)
	data := makeAntennaPodDB(t)

	n, err := feed.ImportAntennaPod(s, data)
	require.NoError(t, err)
	if n != 2 {
		t.Fatalf("expected 2 created, got %d", n)
	}

	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	for _, sub := range subs {
		if sub.Kind != "rss" {
			t.Errorf("expected kind=rss, got %q", sub.Kind)
		}
		if sub.PollIntervalSec != 1800 {
			t.Errorf("expected poll interval 1800, got %d", sub.PollIntervalSec)
		}
	}
}

func TestImportAntennaPodSkipsDuplicates(t *testing.T) {
	s := newStore(t)
	data := makeAntennaPodDB(t)

	_, _ = feed.ImportAntennaPod(s, data)
	n, err := feed.ImportAntennaPod(s, data)
	require.NoError(t, err)
	if n != 0 {
		t.Errorf("second import should create 0, got %d", n)
	}
}

// makeNewPipeZip creates a minimal NewPipe ZIP export containing a newpipe.db.
func makeNewPipeZip(t *testing.T) []byte {
	t.Helper()

	// Build the SQLite DB first.
	tmp, err := os.CreateTemp("", "np-test-*.db")
	require.NoError(t, err)
	tmpPath := tmp.Name()
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmpPath) })

	db, err := sql.Open("sqlite3", tmpPath)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE subscriptions (
		uid INTEGER PRIMARY KEY,
		service_id INTEGER NOT NULL DEFAULT 0,
		url TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT ''
	)`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO subscriptions (service_id, url, name) VALUES
		(0, 'https://www.youtube.com/channel/UC-lHJZR3Gqxm24_Vd_AJ5Yw', 'PewDiePie'),
		(0, 'https://www.youtube.com/channel/UCVHFbw7woebKtfvug_tEqjg', 'Linus Tech Tips')`)
	require.NoError(t, err)

	// Close DB before reading file.
	db.Close()

	dbBytes, err := os.ReadFile(tmpPath)
	require.NoError(t, err)

	// Wrap in ZIP.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create("newpipe.db")
	require.NoError(t, err)
	_, err = fw.Write(dbBytes)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	return buf.Bytes()
}

func TestImportNewPipe(t *testing.T) {
	s := newStore(t)
	data := makeNewPipeZip(t)

	n, err := feed.ImportNewPipe(s, data)
	require.NoError(t, err)
	if n != 2 {
		t.Fatalf("expected 2 created, got %d", n)
	}

	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	for _, sub := range subs {
		if sub.Kind != "youtube" {
			t.Errorf("expected kind=youtube, got %q", sub.Kind)
		}
	}
}

func TestImportNewPipeSkipsDuplicates(t *testing.T) {
	s := newStore(t)
	data := makeNewPipeZip(t)

	_, _ = feed.ImportNewPipe(s, data)
	n, err := feed.ImportNewPipe(s, data)
	require.NoError(t, err)
	if n != 0 {
		t.Errorf("second import should create 0, got %d", n)
	}
}

func TestImportNewPipeMissingDB(t *testing.T) {
	s := newStore(t)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, err := zw.Create("other_file.txt")
	require.NoError(t, err)
	_, _ = fw.Write([]byte("not a db"))
	require.NoError(t, zw.Close())

	_, err = feed.ImportNewPipe(s, buf.Bytes())
	if err == nil {
		t.Fatal("expected error when newpipe.db is absent from ZIP")
	}
}
