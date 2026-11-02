package profanity

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
	"golang.org/x/net/html"
)

// Entry maps a profane word to its replacement.
type Entry struct {
	Word        string // lowercase
	Replacement string // e.g. "darn"
}

// WordList holds profanity entries for matching and replacement.
type WordList struct {
	entries []Entry
	re      *regexp.Regexp // compiled pattern matching any entry
}

// ParseWordList reads a word list from lines of "word=replacement" or just "word".
// Lines starting with # are comments. Blank lines are ignored.
func ParseWordList(r io.Reader) (*WordList, error) {
	var entries []Entry
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		word, replacement, _ := strings.Cut(line, "=")
		word = strings.TrimSpace(word)
		replacement = strings.TrimSpace(replacement)
		if word == "" {
			continue
		}
		if replacement == "" {
			replacement = "***"
		}
		entries = append(entries, Entry{Word: strings.ToLower(word), Replacement: replacement})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read word list: %w", err)
	}
	wl := &WordList{entries: entries}
	wl.compile()
	return wl, nil
}

func (wl *WordList) compile() {
	if len(wl.entries) == 0 {
		wl.re = nil
		return
	}
	var parts []string
	for _, e := range wl.entries {
		parts = append(parts, regexp.QuoteMeta(e.Word))
	}
	// Match whole words, case-insensitive.
	pattern := `(?i)\b(` + strings.Join(parts, "|") + `)\b`
	wl.re = regexp.MustCompile(pattern)
}

// Empty returns true if the word list has no entries.
func (wl *WordList) Empty() bool {
	return len(wl.entries) == 0
}

// replacementFor returns the bracketed replacement for a matched word.
func (wl *WordList) replacementFor(matched string) string {
	lower := strings.ToLower(matched)
	for _, e := range wl.entries {
		if e.Word == lower {
			return "[" + e.Replacement + "]"
		}
	}
	return "[***]"
}

// cleanReplacementFor returns the unbracketed replacement (for TTS).
func (wl *WordList) cleanReplacementFor(matched string) string {
	lower := strings.ToLower(matched)
	for _, e := range wl.entries {
		if e.Word == lower {
			return e.Replacement
		}
	}
	return ""
}

// FilterText replaces profane words in plain text with [replacement].
func (wl *WordList) FilterText(s string) string {
	if wl.re == nil {
		return s
	}
	return wl.re.ReplaceAllStringFunc(s, wl.replacementFor)
}

// FilterHTML replaces profane words in HTML text nodes, preserving tags.
func (wl *WordList) FilterHTML(s string) string {
	if wl.re == nil {
		return s
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return wl.FilterText(s)
	}
	wl.filterHTMLNode(doc)
	var buf strings.Builder
	html.Render(&buf, doc)
	// html.Parse wraps in <html><head><body>; extract just the body content.
	rendered := buf.String()
	if start := strings.Index(rendered, "<body>"); start >= 0 {
		rendered = rendered[start+6:]
		if end := strings.LastIndex(rendered, "</body>"); end >= 0 {
			rendered = rendered[:end]
		}
	}
	return rendered
}

func (wl *WordList) filterHTMLNode(n *html.Node) {
	if n.Type == html.TextNode {
		n.Data = wl.FilterText(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		wl.filterHTMLNode(c)
	}
}

// AudioRanges identifies caption segments containing profane words
// and returns time ranges with clean replacement text.
func (wl *WordList) AudioRanges(captions []plugin.CaptionSegment) []plugin.AudioRange {
	if wl.re == nil {
		return nil
	}
	var ranges []plugin.AudioRange
	for _, c := range captions {
		if wl.re.MatchString(c.Text) {
			replacement := wl.re.ReplaceAllStringFunc(c.Text, wl.cleanReplacementFor)
			ranges = append(ranges, plugin.AudioRange{
				Start:           c.Start,
				End:             c.End,
				ReplacementText: replacement,
			})
		}
	}
	return ranges
}
