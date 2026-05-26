package feed

import (
	"encoding/csv"
	"fmt"
	"strings"
)

// ImportYouTubeTakeout parses a Google Takeout subscriptions CSV and creates
// YouTube subscriptions. The expected columns are:
//
//	Channel Id, Channel Url, Channel Title
//
// Returns the number of subscriptions created.
// Duplicates (same URL) are silently skipped.
func ImportYouTubeTakeout(s *Store, data []byte) (int, error) {
	reader := csv.NewReader(strings.NewReader(string(data)))
	records, err := reader.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("import youtube takeout: parse csv: %w", err)
	}

	count := 0
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

		if _, err := s.CreateSubscription(Subscription{
			Kind:            "youtube",
			URL:             channelURL,
			Title:           channelTitle,
			PollIntervalSec: 1800,
			NextCheckAt:     1,
		}); err == nil {
			count++
		}
		// Duplicate URLs are rejected by the UNIQUE constraint; skip silently.
	}

	return count, nil
}
