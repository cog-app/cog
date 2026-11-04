// Package fetcher dispatches subscription polls by kind and produces feed.Item lists.
package fetcher

import (
	"context"
	"fmt"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Fetch dispatches by subscription kind.
func Fetch(ctx context.Context, sub feed.Subscription) (feed.FetchResult, error) {
	if err := feed.CheckURL(sub.URL); err != nil {
		return feed.FetchResult{}, err
	}
	switch sub.Kind {
	case "rss":
		return fetchRSS(ctx, sub)
	case "youtube":
		return fetchYouTube(ctx, sub)
	case "url":
		return fetchURL(ctx, sub)
	default:
		return feed.FetchResult{}, fmt.Errorf("unknown subscription kind: %q", sub.Kind)
	}
}
