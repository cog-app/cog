package bookmarks

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// netscapeNode is a folder or bookmark read from a Netscape bookmarks file.
type netscapeNode struct {
	folder   bool
	title    string
	url      string
	tags     []string
	children []*netscapeNode
}

// parseNetscapeTree reads a Netscape bookmarks file keeping its folders: an
// <H3> names a folder, and the <DL> after it holds the folder's contents.
func parseNetscapeTree(r io.Reader) ([]*netscapeNode, error) {
	var top []*netscapeNode
	stack := []*[]*netscapeNode{&top}
	var pending *netscapeNode // a folder whose <DL> has not opened yet
	var text *strings.Builder // the title being read, inside <H3> or <A>
	var cur *netscapeNode

	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return top, nil
			}
			return nil, fmt.Errorf("parse bookmarks file: %w", z.Err())
		case html.TextToken:
			if text != nil {
				text.Write(z.Text())
			}
		case html.StartTagToken:
			tn, hasAttr := z.TagName()
			switch string(tn) {
			case "h3":
				cur = &netscapeNode{folder: true}
				text = &strings.Builder{}
			case "a":
				cur = &netscapeNode{}
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					switch string(k) {
					case "href":
						cur.url = string(v)
					case "tags":
						for t := range strings.SplitSeq(string(v), ",") {
							if t = strings.TrimSpace(t); t != "" {
								cur.tags = append(cur.tags, t)
							}
						}
					}
				}
				text = &strings.Builder{}
			case "dl":
				if pending != nil {
					stack = append(stack, &pending.children)
					pending = nil
				} else {
					stack = append(stack, stack[len(stack)-1])
				}
			}
		case html.EndTagToken:
			tn, _ := z.TagName()
			switch string(tn) {
			case "h3", "a":
				if cur == nil || text == nil {
					continue
				}
				cur.title = strings.TrimSpace(text.String())
				text = nil
				list := stack[len(stack)-1]
				if cur.folder {
					*list = append(*list, cur)
					pending = cur
				} else if cur.url != "" {
					if cur.title == "" {
						cur.title = cur.url
					}
					*list = append(*list, cur)
				}
				cur = nil
			case "dl":
				pending = nil
				if len(stack) > 1 {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
}

// ImportNetscapeTree adds the folders and bookmarks of a Netscape bookmarks
// file to the end of folder, all or none, and returns what it added there. A
// bookmark whose URL is already saved takes the file's title and tags and is
// added to the folder as well, keeping the folders it was in.
func ImportNetscapeTree(db *sql.DB, folder int64, r io.Reader) ([]ChildRef, error) {
	nodes, err := parseNetscapeTree(r)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin import: %w", err)
	}
	defer tx.Rollback()
	if err := folderExists(tx, folder); err != nil {
		return nil, err
	}
	refs, err := importNodes(tx, folder, nodes)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit import: %w", err)
	}
	return refs, nil
}

func importNodes(tx *sql.Tx, folder int64, nodes []*netscapeNode) ([]ChildRef, error) {
	refs := make([]ChildRef, 0, len(nodes))
	for _, n := range nodes {
		if n.folder {
			var id int64
			if err := tx.QueryRow(`INSERT INTO folders (title, parent_id, sort_order) VALUES (?, ?, (`+nextOrderSQL+`)) RETURNING id`,
				n.title, parentArg(folder), parentArg(folder), folder).Scan(&id); err != nil {
				return nil, fmt.Errorf("insert folder %q: %w", n.title, err)
			}
			if _, err := importNodes(tx, id, n.children); err != nil {
				return nil, err
			}
			refs = append(refs, ChildRef{ChildFolder, id})
			continue
		}
		var id int64
		err := tx.QueryRow(`SELECT id FROM bookmarks WHERE url = ?`, n.url).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = tx.QueryRow(`INSERT INTO bookmarks (url, title) VALUES (?, ?) RETURNING id`, n.url, n.title).Scan(&id)
		case err == nil:
			_, err = tx.Exec(`UPDATE bookmarks SET title = ?, updated_at = unixepoch() WHERE id = ?`, n.title, id)
		}
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", n.url, err)
		}
		if n.tags != nil {
			if err := setTags(tx, id, n.tags); err != nil {
				return nil, err
			}
		}
		if err := linkBookmark(tx, id, folder); err != nil {
			return nil, err
		}
		refs = append(refs, ChildRef{ChildBookmark, id})
	}
	return refs, nil
}
