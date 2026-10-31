package bookmarks

import (
	"database/sql"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
)

// ImportNetscapeHTML parses a Netscape Bookmark HTML file and imports bookmarks.
// Returns the number of bookmarks imported.
func ImportNetscapeHTML(db *sql.DB, r io.Reader) (int, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("read import file: %w", err)
	}

	doc := string(data)
	count := 0

	// Parse <DT><A HREF="..." ...>Title</A> entries.
	linkRe := regexp.MustCompile(`(?i)<DT>\s*<A\s+([^>]*)>(.*?)</A>`)
	hrefRe := regexp.MustCompile(`(?i)HREF="([^"]*)"`)
	tagsRe := regexp.MustCompile(`(?i)TAGS="([^"]*)"`)

	matches := linkRe.FindAllStringSubmatch(doc, -1)
	for _, m := range matches {
		attrs := m[1]
		title := html.UnescapeString(strings.TrimSpace(m[2]))

		hrefMatch := hrefRe.FindStringSubmatch(attrs)
		if hrefMatch == nil || hrefMatch[1] == "" {
			continue
		}
		url := html.UnescapeString(hrefMatch[1])
		if title == "" {
			title = url
		}

		var tags []string
		if tagsMatch := tagsRe.FindStringSubmatch(attrs); tagsMatch != nil && tagsMatch[1] != "" {
			for t := range strings.SplitSeq(html.UnescapeString(tagsMatch[1]), ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, t)
				}
			}
		}

		if _, err := CreateBookmark(db, url, title, "", tags, nil); err != nil {
			return count, fmt.Errorf("import %s: %w", url, err)
		}
		count++
	}

	return count, nil
}

// ExportNetscapeHTML writes all bookmarks in Netscape Bookmark HTML format.
// The bookmarks are read before anything is written, so a failed query does
// not leave half a file behind.
func ExportNetscapeHTML(db *sql.DB, w io.Writer) error {
	rows, err := db.Query(`SELECT b.url, b.title, b.created_at, COALESCE((SELECT group_concat(tag, ',') FROM (SELECT tag FROM tags WHERE bookmark_id = b.id ORDER BY tag)), '')
		FROM bookmarks b ORDER BY b.title COLLATE NOCASE, b.id`)
	if err != nil {
		return fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	b.WriteString("<!DOCTYPE NETSCAPE-Bookmark-file-1>\n" +
		"<!-- This is an automatically generated file.\n" +
		"     It will be read and overwritten.\n" +
		"     DO NOT EDIT! -->\n" +
		`<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">` + "\n" +
		"<TITLE>Bookmarks</TITLE>\n<H1>Bookmarks</H1>\n<DL><p>\n")
	for rows.Next() {
		var url, title, tags string
		var added int64
		if err := rows.Scan(&url, &title, &added, &tags); err != nil {
			return fmt.Errorf("read bookmark: %w", err)
		}
		tagsAttr := ""
		if tags != "" {
			tagsAttr = fmt.Sprintf(` TAGS="%s"`, html.EscapeString(tags))
		}
		fmt.Fprintf(&b, "    <DT><A HREF=\"%s\" ADD_DATE=\"%d\"%s>%s</A>\n",
			html.EscapeString(url), added, tagsAttr, html.EscapeString(title))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list bookmarks: %w", err)
	}
	b.WriteString("</DL><p>\n")
	_, err = io.WriteString(w, b.String())
	return err
}
