// Package fetcher dispatches subscription polls by kind and produces feed.Item lists.
package fetcher

import (
	"context"
	"fmt"

	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Result is what a Fetch call returns for a subscription poll.
type Result struct {
	Items               []feed.Item
	ETag                string
	LastModified        string
	SubscriptionUpdates map[string]string // optional metadata to write back (title, description, site_url, image_url, favicon_url)
	NotModified         bool
}

// Fetch dispatches by subscription kind.
func Fetch(ctx context.Context, sub feed.Subscription) (Result, error) {
	switch sub.Kind {
	case "rss":
		return fetchRSS(ctx, sub)
	case "youtube":
		return fetchYouTube(ctx, sub)
	case "url":
		return Result{}, fmt.Errorf("url fetcher: not implemented (Task 9)")
	default:
		return Result{}, fmt.Errorf("unknown subscription kind: %q", sub.Kind)
	}
}
