package bookmarks

import (
	"database/sql"
	"errors"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
)

// The parts of a Netscape Bookmark file the import reads: each
// <DT><A HREF="..." ...>Title</A> entry, and its HREF and TAGS attributes.
var (
	netscapeLinkRe = regexp.MustCompile(`(?i)<DT>\s*<A\s+([^>]*)>(.*?)</A>`)
	netscapeHrefRe = regexp.MustCompile(`(?i)HREF="([^"]*)"`)
	netscapeTagsRe = regexp.MustCompile(`(?i)TAGS="([^"]*)"`)
)

// ImportNetscapeHTML parses a Netscape Bookmark HTML file and imports its
// bookmarks, returning how many. The import is one transaction: if any
// bookmark fails, none is kept and the count is 0, so a retry after fixing
// the file does not start from a half-imported state.
//
// A bookmark whose URL is already saved is updated as CreateBookmark would:
// its title and description are replaced, its tags only when the file gives
// some, and its folders are kept.
func ImportNetscapeHTML(db *sql.DB, r io.Reader) (int, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("read import file: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin import: %w", err)
	}
	defer tx.Rollback()

	count := 0
	for _, m := range netscapeLinkRe.FindAllStringSubmatch(string(data), -1) {
		attrs := m[1]
		title := html.UnescapeString(strings.TrimSpace(m[2]))

		hrefMatch := netscapeHrefRe.FindStringSubmatch(attrs)
		if hrefMatch == nil || hrefMatch[1] == "" {
			continue
		}
		url := html.UnescapeString(hrefMatch[1])
		if title == "" {
			title = url
		}

		var tags []string
		if tagsMatch := netscapeTagsRe.FindStringSubmatch(attrs); tagsMatch != nil && tagsMatch[1] != "" {
			for t := range strings.SplitSeq(html.UnescapeString(tagsMatch[1]), ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					tags = append(tags, t)
				}
			}
		}

		if err := importBookmark(tx, url, title, tags); err != nil {
			return 0, fmt.Errorf("import %s: %w", url, err)
		}
		count++
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit import: %w", err)
	}
	return count, nil
}

// importBookmark saves one imported bookmark in tx: a new one in the root
// folder, or an update of the one already saved under url.
func importBookmark(tx *sql.Tx, url, title string, tags []string) error {
	var id int64
	err := tx.QueryRow(`SELECT id FROM bookmarks WHERE url = ?`, url).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := tx.QueryRow(`INSERT INTO bookmarks (url, title, description) VALUES (?, ?, '') RETURNING id`, url, title).Scan(&id); err != nil {
			return fmt.Errorf("insert bookmark: %w", err)
		}
		if err := setFolders(tx, id, nil); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("find bookmark by url: %w", err)
	default:
		if _, err := tx.Exec(`UPDATE bookmarks SET title = ?, description = '', updated_at = unixepoch() WHERE id = ?`, title, id); err != nil {
			return fmt.Errorf("update bookmark: %w", err)
		}
	}
	if tags != nil {
		if err := setTags(tx, id, tags); err != nil {
			return err
		}
	}
	return nil
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
