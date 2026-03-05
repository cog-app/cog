package rss

import (
	"database/sql"
	"encoding/xml"
)

// OPML types for import/export.
type opml struct {
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

// ExportOPML generates an OPML document from the user's feeds and folders.
func ExportOPML(db *sql.DB) ([]byte, error) {
	folders, err := ListFolders(db)
	if err != nil {
		return nil, err
	}
	feeds, err := ListFeeds(db)
	if err != nil {
		return nil, err
	}

	// Group feeds by folder.
	folderFeeds := make(map[int64][]Feed)
	var uncategorized []Feed
	for _, f := range feeds {
		if f.FolderID.Valid {
			folderFeeds[f.FolderID.Int64] = append(folderFeeds[f.FolderID.Int64], f)
		} else {
			uncategorized = append(uncategorized, f)
		}
	}

	doc := opml{
		Version: "2.0",
		Head:    opmlHead{Title: "Cog RSS Subscriptions"},
	}

	// Add folder outlines.
	for _, folder := range folders {
		outline := opmlOutline{Text: folder.Name, Title: folder.Name}
		for _, feed := range folderFeeds[folder.ID] {
			outline.Outlines = append(outline.Outlines, feedOutline(feed))
		}
		doc.Body.Outlines = append(doc.Body.Outlines, outline)
	}

	// Add uncategorized feeds.
	for _, feed := range uncategorized {
		doc.Body.Outlines = append(doc.Body.Outlines, feedOutline(feed))
	}

	output, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), output...), nil
}

func feedOutline(f Feed) opmlOutline {
	title := f.Title
	if title == "" {
		title = f.URL
	}
	return opmlOutline{
		Text:    title,
		Title:   title,
		Type:    "rss",
		XMLURL:  f.URL,
		HTMLURL: f.SiteURL,
	}
}

// ImportOPML parses an OPML document and creates folders and feeds.
func ImportOPML(db *sql.DB, data []byte) (int, error) {
	var doc opml
	if err := xml.Unmarshal(data, &doc); err != nil {
		return 0, err
	}

	count := 0
	for _, outline := range doc.Body.Outlines {
		if outline.XMLURL != "" {
			// Top-level feed, no folder.
			if _, err := CreateFeed(db, outline.XMLURL, sql.NullInt64{}); err == nil {
				count++
			}
			continue
		}

		// Folder with child feeds.
		var folderID sql.NullInt64
		if outline.Text != "" {
			folder, err := CreateFolder(db, outline.Text)
			if err != nil {
				// Folder might already exist — try to find it.
				folders, _ := ListFolders(db)
				for _, f := range folders {
					if f.Name == outline.Text {
						folderID = sql.NullInt64{Int64: f.ID, Valid: true}
						break
					}
				}
			} else {
				folderID = sql.NullInt64{Int64: folder.ID, Valid: true}
			}
		}

		for _, child := range outline.Outlines {
			if child.XMLURL == "" {
				continue
			}
			if _, err := CreateFeed(db, child.XMLURL, folderID); err == nil {
				count++
			}
		}
	}

	return count, nil
}
