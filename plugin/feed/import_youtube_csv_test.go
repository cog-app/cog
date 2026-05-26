package feed_test

import (
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

	n, err := feed.ImportYouTubeTakeout(s, []byte(sampleYouTubeCSV))
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
		if sub.PollIntervalSec != 1800 {
			t.Errorf("expected poll interval 1800, got %d", sub.PollIntervalSec)
		}
	}
}

func TestImportYouTubeTakeoutSkipsDuplicates(t *testing.T) {
	s := newStore(t)

	_, _ = feed.ImportYouTubeTakeout(s, []byte(sampleYouTubeCSV))
	n, err := feed.ImportYouTubeTakeout(s, []byte(sampleYouTubeCSV))
	require.NoError(t, err)
	if n != 0 {
		t.Errorf("second import should create 0, got %d", n)
	}
}

func TestImportYouTubeTakeoutFallbackURL(t *testing.T) {
	// If Channel Url is empty, should construct URL from Channel Id.
	s := newStore(t)
	csv := "Channel Id,Channel Url,Channel Title\nUCtest123,,Test Channel\n"

	n, err := feed.ImportYouTubeTakeout(s, []byte(csv))
	require.NoError(t, err)
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}

	subs, _ := s.ListSubscriptions()
	if subs[0].URL != "https://www.youtube.com/channel/UCtest123" {
		t.Errorf("unexpected URL: %s", subs[0].URL)
	}
}

func TestImportYouTubeTakeoutInvalidCSV(t *testing.T) {
	s := newStore(t)
	// Mismatched quotes — encoding/csv will return an error.
	bad := "Channel Id,Channel Url,Channel Title\n\"unclosed,url,title\n"
	_, err := feed.ImportYouTubeTakeout(s, []byte(bad))
	if err == nil {
		t.Fatal("expected error for malformed CSV")
	}
}
