package fetcher

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/mmcdole/gofeed"
	ext "github.com/mmcdole/gofeed/extensions"
	"golang.org/x/net/html"
)

// parseITunesDuration reads an itunes:duration: seconds, MM:SS or HH:MM:SS.
// Anything else is 0.
func parseITunesDuration(s string) int64 {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) > 3 {
		return 0
	}
	var total int64
	for _, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return 0
		}
		total = total*60 + int64(n)
	}
	return total
}

// resolveImage resolves ref against base and answers it only when it is an
// http(s) URL.
func resolveImage(ref, base string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(strings.ToLower(ref), "data:") {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if b, err := url.Parse(base); err == nil {
		r = b.ResolveReference(r)
	}
	if r.Scheme != "http" && r.Scheme != "https" {
		return ""
	}
	return r.String()
}

// firstContentImage is the first usable <img> in raw HTML: a non-data src and
// not a 1-pixel tracker.
func firstContentImage(markup, base string) string {
	z := html.NewTokenizer(strings.NewReader(markup))
	for {
		switch z.Next() {
		case html.ErrorToken:
			// io.EOF ends the document; a malformed tail just stops the scan.
			return ""
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			if string(name) != "img" || !hasAttr {
				continue
			}
			var src string
			tracker := false
			for {
				k, v, more := z.TagAttr()
				switch string(k) {
				case "src":
					src = string(v)
				case "width", "height":
					if strings.TrimSpace(string(v)) == "1" {
						tracker = true
					}
				}
				if !more {
					break
				}
			}
			if tracker {
				continue
			}
			if got := resolveImage(src, base); got != "" {
				return got
			}
		}
	}
}

// mediaImage finds an image among media:content elements, looking one level
// down into media:group.
func mediaImage(exts map[string][]ext.Extension, base string) string {
	for _, c := range exts["content"] {
		if c.Attrs["medium"] == "image" || strings.HasPrefix(c.Attrs["type"], "image/") {
			if got := resolveImage(c.Attrs["url"], base); got != "" {
				return got
			}
		}
	}
	return ""
}

// itemImage picks an entry's thumbnail: itunes:image, media:thumbnail, an
// image media:content, then the first image in the body. Relative URLs resolve
// against base.
func itemImage(entry *gofeed.Item, base string) string {
	if entry.ITunesExt != nil {
		if got := resolveImage(entry.ITunesExt.Image, base); got != "" {
			return got
		}
	}
	media := entry.Extensions["media"]
	for _, th := range media["thumbnail"] {
		if got := resolveImage(th.Attrs["url"], base); got != "" {
			return got
		}
	}
	if got := mediaImage(media, base); got != "" {
		return got
	}
	for _, g := range media["group"] {
		if got := mediaImage(g.Children, base); got != "" {
			return got
		}
	}
	if got := firstContentImage(entry.Content, base); got != "" {
		return got
	}
	return firstContentImage(entry.Description, base)
}
