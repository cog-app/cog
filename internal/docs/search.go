package docs

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// Cog searches the docs with a Bleve index of its own (Documents), shared
// by every user; the website, which has no server, with docs-search.js
// over search.json (Index): every word of the query must start a word of
// the page, and a word in the title counts for more than one in a heading,
// which counts for more than one in the text.

// entry is a page in search.json.
type entry struct {
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Section  string   `json:"section,omitempty"`
	Headings []string `json:"headings,omitempty"`
	Text     string   `json:"text"`
}

func (d *Docs) searchIndex() ([]byte, error) {
	es := make([]entry, len(d.Pages))
	for i, p := range d.Pages {
		es[i] = entry{Title: p.Title, URL: p.Path, Section: p.Section, Headings: p.headings, Text: p.text}
	}
	b, err := json.Marshal(es)
	if err != nil {
		return nil, fmt.Errorf("docs: search index: %w", err)
	}
	return b, nil
}

// DocSource is the search source of the pages' Documents.
const DocSource = "docs"

// Documents are the pages for a search index: name the title, subtext the
// section, content the headings and text, _id the page's path.
func (d *Docs) Documents() []plugin.Document {
	out := make([]plugin.Document, len(d.Pages))
	for i, p := range d.Pages {
		out[i] = plugin.Document{
			ID: DocSource + ":" + p.Path,
			Fields: map[string]any{
				"_id":     p.Path,
				"name":    p.Title,
				"subtext": p.Section,
				"content": strings.Join(append(p.headings, p.text), "\n"),
				"source":  DocSource,
			},
		}
	}
	return out
}
