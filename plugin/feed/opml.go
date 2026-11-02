package feed

import (
	"database/sql"
	"encoding/xml"
	"fmt"
)

// OPML types for import/export.
type opmlDoc struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    opmlBody `xml:"body"`
}

type opmlHead struct {
	Title string `xml:"title"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string        `xml:"htmlUrl,attr,omitempty"`
	Outlines []opmlOutline `xml:"outline,omitempty"`
}

// ExportOPML generates an OPML 2.0 document from the user's RSS subscriptions
// and folders. Non-RSS subscriptions (youtube, url) are excluded.
func (s *Store) ExportOPML() ([]byte, error) {
	folders, err := s.ListFolders()
	if err != nil {
		return nil, fmt.Errorf("export opml: list folders: %w", err)
	}
	subs, err := s.ListSubscriptions()
	if err != nil {
		return nil, fmt.Errorf("export opml: list subscriptions: %w", err)
	}

	// Filter to RSS only and group by folder.
	folderSubs := make(map[int64][]Subscription)
	var uncategorized []Subscription
	for _, sub := range subs {
		if sub.Kind != "rss" {
			continue
		}
		if sub.FolderID.Valid {
			folderSubs[sub.FolderID.Int64] = append(folderSubs[sub.FolderID.Int64], sub)
		} else {
			uncategorized = append(uncategorized, sub)
		}
	}

	doc := opmlDoc{
		Version: "2.0",
		Head:    opmlHead{Title: "Cog Feed Subscriptions"},
	}

	// Add folder outlines (skip empty folders).
	for _, folder := range folders {
		fSubs := folderSubs[folder.ID]
		if len(fSubs) == 0 {
			continue
		}
		out := opmlOutline{Text: folder.Name, Title: folder.Name}
		for _, sub := range fSubs {
			out.Outlines = append(out.Outlines, subOutline(sub))
		}
		doc.Body.Outlines = append(doc.Body.Outlines, out)
	}

	// Add uncategorized feeds.
	for _, sub := range uncategorized {
		doc.Body.Outlines = append(doc.Body.Outlines, subOutline(sub))
	}

	output, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("export opml: marshal: %w", err)
	}
	return append([]byte(xml.Header), output...), nil
}

func subOutline(sub Subscription) opmlOutline {
	title := sub.Title
	if title == "" {
		title = sub.URL
	}
	return opmlOutline{
		Text:    title,
		Title:   title,
		Type:    "rss",
		XMLURL:  sub.URL,
		HTMLURL: sub.SiteURL,
	}
}

// ImportOPML parses an OPML document and creates folders and RSS subscriptions
// (kind="rss"). Returns the number of subscriptions successfully created.
// Existing subscriptions with the same URL are skipped (the UNIQUE constraint
// on subscriptions.url rejects duplicates).
func (s *Store) ImportOPML(data []byte) (int, error) {
	var doc opmlDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return 0, fmt.Errorf("parse opml: %w", err)
	}

	count := 0
	for _, outline := range doc.Body.Outlines {
		if outline.XMLURL != "" {
			// Top-level subscription, no folder.
			created, err := s.importSubscription(Subscription{
				Kind:            "rss",
				URL:             outline.XMLURL,
				Title:           outlineTitle(outline),
				SiteURL:         outline.HTMLURL,
				PollIntervalSec: 1800,
				NextCheckAt:     1,
			})
			if err != nil {
				return count, fmt.Errorf("import opml: %w", err)
			}
			if created {
				count++
			}
			continue
		}

		// Folder with child feeds.
		var folderID sql.NullInt64
		if outline.Text != "" {
			id, createErr := s.CreateFolder(outline.Text)
			if createErr == nil {
				folderID = sql.NullInt64{Int64: id, Valid: true}
			} else {
				// Folder might already exist — look it up.
				folders, err := s.ListFolders()
				if err != nil {
					return count, fmt.Errorf("import opml: %w", err)
				}
				for _, f := range folders {
					if f.Name == outline.Text {
						folderID = sql.NullInt64{Int64: f.ID, Valid: true}
						break
					}
				}
				if !folderID.Valid {
					return count, fmt.Errorf("import opml: folder %q: %w", outline.Text, createErr)
				}
			}
		}

		for _, child := range outline.Outlines {
			if child.XMLURL == "" {
				continue
			}
			created, err := s.importSubscription(Subscription{
				Kind:            "rss",
				URL:             child.XMLURL,
				Title:           outlineTitle(child),
				SiteURL:         child.HTMLURL,
				FolderID:        folderID,
				PollIntervalSec: 1800,
				NextCheckAt:     1,
			})
			if err != nil {
				return count, fmt.Errorf("import opml: %w", err)
			}
			if created {
				count++
			}
		}
	}

	return count, nil
}

func outlineTitle(o opmlOutline) string {
	if o.Title != "" {
		return o.Title
	}
	return o.Text
}
