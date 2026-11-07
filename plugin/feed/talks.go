package feed

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// A page of talks is a conference or playlist page that links to one page
// per talk, each of which carries the talk's audio or video. ScanTalksPage
// reads such a page so its talks can be added as items of their own.

const (
	talksScanTimeout   = 60 * time.Second
	talksConcurrency   = 6
	talksMaxPages      = 200
	talksMaxPageBytes  = 10 << 20
	talksUserAgent     = "Cog/1.0 (Feed)"
	talksStatePrefixes = "window.__INITIAL_STATE__=|window.__STATE__="
)

// PageTalk is one talk found on a page of talks.
type PageTalk struct {
	PageURL  string // the talk's own page; identifies the talk
	Title    string
	Speaker  string
	MediaURL string
	Type     string // "audio" or "video"; "" when the talk page had no media
}

// TalkPageFailure is a talk page that could not be read.
type TalkPageFailure struct {
	URL string
	Err string
}

// TalksScan is what ScanTalksPage found.
type TalksScan struct {
	Title  string
	Talks  []PageTalk        // in the order the page lists them
	Failed []TalkPageFailure // talk pages that failed to load
}

// ScanTalksPage fetches pageURL, follows its links to talk pages (same host,
// under the page's path) and reads each for its media. A talk page that
// fails is reported in Failed rather than failing the scan.
func ScanTalksPage(ctx context.Context, pageURL string) (*TalksScan, error) {
	if err := CheckURL(pageURL); err != nil {
		return nil, err
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	doc, err := fetchTalksHTML(ctx, pageURL)
	if err != nil {
		return nil, fmt.Errorf("fetch page: %w", err)
	}

	scan := &TalksScan{Title: firstText(doc, "title", "h1")}
	links := talkLinks(doc, base)

	talks := make([]PageTalk, len(links))
	errs := make([]error, len(links))
	sem := make(chan struct{}, talksConcurrency)
	var wg sync.WaitGroup
	for i, link := range links {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			talks[i], errs[i] = scanTalkPage(ctx, link)
		})
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			scan.Failed = append(scan.Failed, TalkPageFailure{URL: links[i], Err: err.Error()})
			continue
		}
		scan.Talks = append(scan.Talks, talks[i])
	}
	return scan, nil
}

// talkLinks returns the page's links to talk pages: on the same host, under
// the page's own path, not the page itself and not a session page, each once
// and in page order. The page's query (a language, say) is carried over.
func talkLinks(doc *goquery.Document, base *url.URL) []string {
	prefix := base.Path
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var links []string
	seen := map[string]bool{}
	doc.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		href, _ := s.Attr("href")
		ref, err := url.Parse(href)
		if href == "" || err != nil {
			return true // malformed hrefs are common in the wild and never talk links
		}
		u := base.ResolveReference(ref)
		u.RawQuery = base.RawQuery
		u.Fragment = ""
		switch {
		case u.Host != base.Host,
			u.Path == base.Path,
			!strings.HasPrefix(u.Path, prefix),
			strings.Contains(u.Path, "session"):
			return true
		}
		if s := u.String(); !seen[s] {
			seen[s] = true
			links = append(links, s)
		}
		return len(links) < talksMaxPages
	})
	return links
}

func fetchTalksHTML(ctx context.Context, pageURL string) (*goquery.Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", talksUserAgent)
	resp, err := HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}
	doc, err := goquery.NewDocumentFromReader(io.LimitReader(resp.Body, talksMaxPageBytes))
	if err != nil {
		return nil, fmt.Errorf("parse HTML: %w", err)
	}
	return doc, nil
}

func scanTalkPage(ctx context.Context, pageURL string) (PageTalk, error) {
	doc, err := fetchTalksHTML(ctx, pageURL)
	if err != nil {
		return PageTalk{}, err
	}
	talk := PageTalk{
		PageURL: pageURL,
		Title:   firstText(doc, "h1", "title"),
		Speaker: firstText(doc, `[class*="author"], [class*="byline"], [class*="speaker"]`),
	}

	audio := firstAttr(doc, pageURL, "src", "audio source, audio[src]")
	if audio == "" {
		audio = firstAttr(doc, pageURL, "href", `a[href*=".mp3"], a[data-file-type="audio"]`)
	}
	video := firstAttr(doc, pageURL, "src", "video source, video[src]")
	if audio == "" || video == "" {
		stateAudio, stateVideo := embeddedMedia(doc)
		audio = cmp.Or(audio, stateAudio)
		video = cmp.Or(video, stateVideo)
	}

	// Audio is preferred: it is smaller, and a talk is listened to.
	switch {
	case CheckURL(audio) == nil:
		talk.MediaURL, talk.Type = audio, "audio"
	case CheckURL(video) == nil:
		talk.MediaURL, talk.Type = video, "video"
	}
	return talk, nil
}

// firstText returns the trimmed text of the first element matching any of
// the selectors, tried in order.
func firstText(doc *goquery.Document, selectors ...string) string {
	for _, sel := range selectors {
		if t := strings.Join(strings.Fields(doc.Find(sel).First().Text()), " "); t != "" {
			return t
		}
	}
	return ""
}

// firstAttr returns the first non-empty attr of an element matching sel,
// resolved against pageURL.
func firstAttr(doc *goquery.Document, pageURL, attr, sel string) string {
	var out string
	doc.Find(sel).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if v, _ := s.Attr(attr); v != "" {
			out = resolveAgainst(pageURL, v)
		}
		return out == ""
	})
	return out
}

func resolveAgainst(pageURL, ref string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	return base.ResolveReference(r).String()
}

// embeddedMedia looks for media in state a page's script assigns to a window
// variable, either as JSON or as a JSON string holding base64-encoded JSON,
// and returns the first audio and video "mediaUrl" it holds.
func embeddedMedia(doc *goquery.Document) (audio, video string) {
	doc.Find("script").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		text := s.Text()
		for prefix := range strings.SplitSeq(talksStatePrefixes, "|") {
			i := strings.Index(text, prefix)
			if i < 0 {
				continue
			}
			state, ok := decodeState(text[i+len(prefix):])
			if !ok {
				continue
			}
			mediaInState(state, &audio, &video)
		}
		return audio == "" || video == ""
	})
	return audio, video
}

func decodeState(raw string) (any, bool) {
	var v any
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&v); err != nil {
		return nil, false
	}
	str, ok := v.(string)
	if !ok {
		return v, true
	}
	b, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(str); err != nil {
			return nil, false
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, false
	}
	return v, true
}

// mediaInState walks decoded state for {"audio": [{"mediaUrl": …}]} and the
// same for "video", filling whichever of audio and video is still empty.
func mediaInState(node any, audio, video *string) {
	switch v := node.(type) {
	case map[string]any:
		for key, dst := range map[string]*string{"audio": audio, "video": video} {
			if *dst != "" {
				continue
			}
			list, _ := v[key].([]any)
			for _, e := range list {
				if m, _ := e.(map[string]any); m != nil {
					if u, _ := m["mediaUrl"].(string); u != "" {
						*dst = u
						break
					}
				}
			}
		}
		for _, child := range v {
			if *audio != "" && *video != "" {
				return
			}
			mediaInState(child, audio, video)
		}
	case []any:
		for _, child := range v {
			if *audio != "" && *video != "" {
				return
			}
			mediaInState(child, audio, video)
		}
	}
}
