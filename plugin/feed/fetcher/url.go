package fetcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

const urlMaxBodyBytes = 5 * 1024 * 1024

func fetchURL(ctx context.Context, sub feed.Subscription) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sub.URL, nil)
	if err != nil {
		return Result{}, fmt.Errorf("url fetch new request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("url fetch %s: %w", sub.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return Result{}, fmt.Errorf("url fetch %s: %s", sub.URL, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, urlMaxBodyBytes))
	if err != nil {
		return Result{}, fmt.Errorf("url fetch read body: %w", err)
	}
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])

	if sub.ETag == hash {
		return Result{ETag: hash, NotModified: true}, nil
	}

	title := sub.Title
	if title == "" {
		if u, perr := url.Parse(sub.URL); perr == nil {
			title = path.Base(u.Path)
			if title == "" || title == "/" || title == "." {
				title = u.Host
			}
		}
	}

	content := string(body)
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
		content = bluemonday.UGCPolicy().Sanitize(content)
	} else {
		content = "<pre>" + html.EscapeString(content) + "</pre>"
	}

	item := feed.Item{
		Type:        "text",
		GUID:        hash,
		Title:       title,
		URL:         sub.URL,
		ContentHTML: content,
		ContentHash: hash,
		PublishedAt: time.Now().Unix(),
	}

	return Result{Items: []feed.Item{item}, ETag: hash}, nil
}
