package feed

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/plugin"
)

// feedEnricher implements plugin.SchemaEnricher against a feed user's DB.
// The collector hands us the per-user DB the search source registered.
type feedEnricher struct{}

// NewEnricher returns a plugin.SchemaEnricher for feed items.
func NewEnricher() plugin.SchemaEnricher { return feedEnricher{} }

func (feedEnricher) EnrichSchema(db *sql.DB, rowIDs []int64) ([]plugin.Document, error) {
	if len(rowIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(rowIDs))
	args := make([]any, len(rowIDs))
	for i, id := range rowIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	in := "(" + strings.Join(placeholders, ",") + ")"

	// Wide join: items + their subscription + folder + downloads. Tags handled
	// separately because of the many-to-many shape.
	q := `SELECT
			i.id, i.title, i.author, i.content_html, i.url, i.type,
			i.is_read, i.is_starred, i.published_at,
			s.kind, s.title,
			COALESCE(f.name, ''),
			COALESCE(d.state, '')
		FROM items i
		LEFT JOIN subscriptions s ON s.id = i.subscription_id
		LEFT JOIN folders f ON f.id = s.folder_id
		LEFT JOIN downloads d ON d.item_id = i.id
		WHERE i.id IN ` + in
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("feed enrich schema query: %w", err)
	}
	defer rows.Close()

	type row struct {
		ID            int64
		Title         string
		Author        string
		Content       string
		URL           string
		Type          string
		IsRead        int
		IsStarred     int
		Published     int64
		Kind          string
		SubTitle      string
		Folder        string
		DownloadState string
	}
	var loaded []row
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.ID, &r.Title, &r.Author, &r.Content, &r.URL, &r.Type,
			&r.IsRead, &r.IsStarred, &r.Published,
			&r.Kind, &r.SubTitle, &r.Folder, &r.DownloadState,
		); err != nil {
			return nil, fmt.Errorf("feed enrich schema scan: %w", err)
		}
		loaded = append(loaded, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feed enrich schema rows: %w", err)
	}

	// Fetch tags per item in one query.
	tagsByItem := map[int64][]string{}
	if len(loaded) > 0 {
		tq := `SELECT it.item_id, t.name
			FROM item_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.item_id IN ` + in
		trows, err := db.Query(tq, args...)
		if err != nil {
			return nil, fmt.Errorf("feed enrich schema tags: %w", err)
		}
		defer trows.Close()
		for trows.Next() {
			var id int64
			var name string
			if err := trows.Scan(&id, &name); err != nil {
				return nil, fmt.Errorf("feed enrich schema tags scan: %w", err)
			}
			tagsByItem[id] = append(tagsByItem[id], name)
		}
		if err := trows.Err(); err != nil {
			return nil, fmt.Errorf("feed enrich schema tags rows: %w", err)
		}
	}

	docs := make([]plugin.Document, 0, len(loaded))
	for _, r := range loaded {
		var isVals []string
		if r.IsRead == 0 {
			isVals = append(isVals, "unread")
		}
		if r.IsStarred != 0 {
			isVals = append(isVals, "starred")
		}
		if r.DownloadState == "ready" {
			isVals = append(isVals, "downloaded")
		}
		fields := map[string]any{
			"title":        r.Title,
			"author":       r.Author,
			"content":      r.Content,
			"url":          r.URL,
			"type":         r.Type,
			"kind":         r.Kind,
			"subscription": r.SubTitle,
			"published":    time.Unix(r.Published, 0).UTC().Format(time.RFC3339),
		}
		if r.Folder != "" {
			fields["folder"] = r.Folder
		}
		if tags := tagsByItem[r.ID]; len(tags) > 0 {
			fields["tag"] = tags
		}
		if len(isVals) > 0 {
			fields["is"] = isVals
		}
		docs = append(docs, plugin.Document{ID: "feed:" + strconv.FormatInt(r.ID, 10), Fields: fields})
	}
	return docs, nil
}
