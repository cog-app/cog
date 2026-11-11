package feed_test

import (
	"database/sql"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

const sampleYouTubeCSV = `Channel Id,Channel Url,Channel Title
UCVHFbw7woebKtfvug_tEqjg,https://www.youtube.com/channel/UCVHFbw7woebKtfvug_tEqjg,Linus Tech Tips
UC-lHJZR3Gqxm24_Vd_AJ5Yw,https://www.youtube.com/channel/UC-lHJZR3Gqxm24_Vd_AJ5Yw,PewDiePie
`

func TestImportYouTubeTakeout(t *testing.T) {
	s := newStore(t)

	res, err := s.Import(feed.ImportYouTubeCSV, []byte(sampleYouTubeCSV), sql.NullInt64{})
	require.NoError(t, err)
	if res.Added != 2 {
		t.Fatalf("expected 2 created, got %d", res.Added)
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
		if sub.PollIntervalSec != 1800 {
			t.Errorf("expected poll interval 1800, got %d", sub.PollIntervalSec)
		}
	}
}

func TestImportYouTubeTakeoutSkipsDuplicates(t *testing.T) {
	s := newStore(t)

	_, err := s.Import(feed.ImportYouTubeCSV, []byte(sampleYouTubeCSV), sql.NullInt64{})
	require.NoError(t, err)
	res, err := s.Import(feed.ImportYouTubeCSV, []byte(sampleYouTubeCSV), sql.NullInt64{})
	require.NoError(t, err)
	require.Equal(t, 0, res.Added)
	require.Equal(t, 2, res.Skipped)
}

func TestImportYouTubeTakeoutFallbackURL(t *testing.T) {
	// If Channel Url is empty, should construct URL from Channel Id.
	s := newStore(t)
	csv := "Channel Id,Channel Url,Channel Title\nUCtest123,,Test Channel\n"

	res, err := s.Import(feed.ImportYouTubeCSV, []byte(csv), sql.NullInt64{})
	require.NoError(t, err)
	if res.Added != 1 {
		t.Fatalf("expected 1, got %d", res.Added)
	}

	subs, err := s.ListSubscriptions()
	require.NoError(t, err)
	if subs[0].URL != "https://www.youtube.com/channel/UCtest123" {
		t.Errorf("unexpected URL: %s", subs[0].URL)
	}
}

func TestImportYouTubeTakeoutInvalidCSV(t *testing.T) {
	s := newStore(t)
	// Mismatched quotes — encoding/csv will return an error.
	bad := "Channel Id,Channel Url,Channel Title\n\"unclosed,url,title\n"
	_, err := s.Import(feed.ImportYouTubeCSV, []byte(bad), sql.NullInt64{})
	if err == nil {
		t.Fatal("expected error for malformed CSV")
	}
}
