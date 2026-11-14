package feed

import (
	"encoding/csv"
	"fmt"
	"strings"
)

// parseYouTubeTakeout lists the channels in a Google Takeout subscriptions
// CSV, whose columns are:
//
//	Channel Id, Channel Url, Channel Title
func parseYouTubeTakeout(data []byte) ([]importEntry, error) {
	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}

	var entries []importEntry
	for i, row := range records {
		if i == 0 {
			continue // skip header
		}
		if len(row) < 3 {
			continue
		}
		channelID := strings.TrimSpace(row[0])
		channelURL := strings.TrimSpace(row[1])
		if channelURL == "" && channelID != "" {
			channelURL = "https://www.youtube.com/channel/" + channelID
		}
		if channelURL == "" {
			continue
		}
		entries = append(entries, importEntry{Subscription: Subscription{
			Kind:  "youtube",
			URL:   channelURL,
			Title: strings.TrimSpace(row[2]),
		}})
	}
	return entries, nil
}
