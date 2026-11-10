package webtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Volatile values that differ between runs of an identical page. Everything
// else the browser serialises is stable, including the ?v= cache-busting
// hashes on assets — those are content hashes, so a stylesheet edit changes
// the DOM and a snapshot is re-taken. That is deliberate: it is what keeps the
// screenshot honest about CSS changes, which never touch the markup.
var (
	// Session and resource identifiers: CSRF tokens, avatar digests.
	volatileHex = regexp.MustCompile(`\b[0-9a-f]{16,}\b`)
	// User and group identifiers.
	volatileUUID = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	// The per-test directory t.TempDir creates under the system temp
	// directory, named after the test plus a random suffix. A page that shows
	// a path (an admin's volume list) shows the data directory cogtest made.
	volatileTempRoot = regexp.MustCompile(regexp.QuoteMeta(filepath.Clean(os.TempDir())) + `/Test[^/\s"'<>]*`)
	// The test server listens on a random loopback port, and any page that
	// prints an absolute URL (the admin's base URL, an invite link, a feed
	// address) carries it.
	volatileLoopback = regexp.MustCompile(`\b127\.0\.0\.1:\d+\b`)
)

// normalizeDOM turns a serialised page into a stable, readable golden.
//
// The DOM is the assertion because it is what the application controls.
// Pixels depend on the browser that rendered them, so comparing those makes a
// Chromium upgrade look like an application change.
func normalizeDOM(raw string) (string, error) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("parse dom: %w", err)
	}

	var b strings.Builder
	writeNode(&b, doc, 0)

	out := b.String()
	for _, m := range volatileMasks {
		out = m.re.ReplaceAllString(out, m.with)
	}
	return out, nil
}

// volatileMasks replace the volatile values, in order: the temp directory
// before the hex run its random suffix could contain. The screenshot applies
// the same masks to the page, so the image shows what the golden says.
var volatileMasks = []struct {
	re   *regexp.Regexp
	with string
}{
	{volatileTempRoot, "«tmp»"},
	{volatileLoopback, "127.0.0.1:«port»"},
	{volatileUUID, "«uuid»"},
	{volatileHex, "«hex»"},
}

// voidElements have no closing tag in HTML, so they are always written in the
// self-closing form.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// writeNode renders one element per line so a diff points at what changed
// rather than at a single enormous line.
func writeNode(b *strings.Builder, n *html.Node, depth int) {
	switch n.Type {
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeNode(b, c, depth)
		}
		return

	case html.TextNode:
		text := strings.Join(strings.Fields(n.Data), " ")
		if text == "" {
			return
		}
		fmt.Fprintf(b, "%s%s\n", indent(depth), text)
		return

	case html.CommentNode:
		return

	case html.ElementNode:
		// Scripts are dropped. The DOM is captured once the page has settled,
		// so whatever a script did to the page is already in the tree around
		// it; the element itself only adds noise, and its src carries an asset
		// hash that would churn every golden on a library upgrade that changed
		// nothing anyone can see.
		if n.Data == "script" {
			return
		}

	case html.DoctypeNode:
		fmt.Fprintf(b, "%s<!doctype %s>\n", indent(depth), n.Data)
		return
	}

	if n.FirstChild == nil || voidElements[n.Data] {
		fmt.Fprintf(b, "%s<%s%s />\n", indent(depth), n.Data, attrs(n))
		return
	}

	fmt.Fprintf(b, "%s<%s%s>\n", indent(depth), n.Data, attrs(n))
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		writeNode(b, c, depth+1)
	}
	fmt.Fprintf(b, "%s</%s>\n", indent(depth), n.Data)
}

// attrs renders attributes in name order. Serialisation order is an
// implementation detail of whatever produced the DOM, and sorting keeps a
// reordering from reading as a change.
func attrs(n *html.Node) string {
	if len(n.Attr) == 0 {
		return ""
	}

	pairs := make([]string, 0, len(n.Attr))
	for _, a := range n.Attr {
		name := a.Key
		if a.Namespace != "" {
			name = a.Namespace + ":" + name
		}
		value := strings.Join(strings.Fields(a.Val), " ")
		pairs = append(pairs, fmt.Sprintf("%s=%q", name, value))
	}
	sort.Strings(pairs)

	return " " + strings.Join(pairs, " ")
}

func indent(depth int) string {
	return strings.Repeat("  ", depth)
}
