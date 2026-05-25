package feed

import (
	"time"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// BuildRecord constructs the cog-grammar Record used by the filter engine
// at ingest time and by the cog-level search source enrichment.
//
// Status flags:
//
//	unread     - item not yet marked read
//	starred    - item starred
//	downloaded - item has a download in state='ready'
func BuildRecord(it Item, sub Subscription, folderName string, tags []string, unread, starred, downloaded bool) query.Record {
	rec := query.Record{
		"title":        {it.Title},
		"author":       {it.Author},
		"content":      {it.ContentHTML},
		"type":         {it.Type},
		"kind":         {sub.Kind},
		"subscription": {sub.Title},
		"url":          {it.URL},
		"published":    {time.Unix(it.PublishedAt, 0).UTC().Format(time.RFC3339)},
	}
	if folderName != "" {
		rec["folder"] = []string{folderName}
	}
	if len(tags) > 0 {
		out := make([]string, len(tags))
		copy(out, tags)
		rec["tag"] = out
	}
	var is []string
	if unread {
		is = append(is, "unread")
	}
	if starred {
		is = append(is, "starred")
	}
	if downloaded {
		is = append(is, "downloaded")
	}
	if len(is) > 0 {
		rec["is"] = is
	}
	return rec
}
