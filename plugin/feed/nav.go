package feed

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// viewCtx identifies the view ("playlist") an item was opened from, so the
// item page and player can compute prev/next within that exact list.
type viewCtx struct {
	Kind string // all|starred|history|folder|sub|tag|playlist
	ID   int64  // folder/sub/playlist id
	Name string // tag name
	Q    string // all-view search query
}

// parseViewCtx reads a viewCtx from request query params, defaulting to "all".
func parseViewCtx(r *http.Request) viewCtx {
	q := r.URL.Query()
	c := viewCtx{Kind: q.Get("ctx"), Name: q.Get("cname"), Q: q.Get("cq")}
	if id, err := strconv.ParseInt(q.Get("cid"), 10, 64); err == nil {
		c.ID = id
	}
	switch c.Kind {
	case "starred", "history", "folder", "sub", "tag", "playlist", "all":
	default:
		c.Kind = "all"
	}
	return c
}

// encode renders the ctx as a URL query string (no leading "?"), omitting
// empty fields. Field order is stable: ctx, cid, cname, cq.
func (c viewCtx) encode() string {
	parts := []string{"ctx=" + url.QueryEscape(c.Kind)}
	if c.ID != 0 {
		parts = append(parts, "cid="+strconv.FormatInt(c.ID, 10))
	}
	if c.Name != "" {
		parts = append(parts, "cname="+url.QueryEscape(c.Name))
	}
	if c.Q != "" {
		parts = append(parts, "cq="+url.QueryEscape(c.Q))
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "&" + p
	}
	return out
}

// label returns a human-readable name for the context, doing best-effort store
// lookups for folder/sub/playlist names. Errors degrade to a generic label.
func (c viewCtx) label(store *Store) string {
	switch c.Kind {
	case "starred":
		return "Starred"
	case "history":
		return "History"
	case "folder":
		if f, err := store.GetFolder(c.ID); err == nil {
			return f.Name
		}
		return "Folder"
	case "sub":
		if s, err := store.GetSubscription(c.ID); err == nil {
			return subscriptionDisplayTitle(s)
		}
		return "Subscription"
	case "tag":
		return "Tag: " + c.Name
	case "playlist":
		if pl, err := store.GetPlaylist(c.ID); err == nil {
			return "Playlist: " + pl.Name
		}
		return "Playlist"
	default:
		return "All"
	}
}

// path returns the originating view's URL, used to highlight the right sidebar
// entry while viewing an item.
func (c viewCtx) path() string {
	switch c.Kind {
	case "starred":
		return "/apps/feed/starred"
	case "history":
		return "/apps/feed/history"
	case "folder":
		return fmt.Sprintf("/apps/feed/folder/%d", c.ID)
	case "sub":
		return fmt.Sprintf("/apps/feed/subscription/%d", c.ID)
	case "tag":
		return "/apps/feed/tag/" + url.PathEscape(c.Name)
	case "playlist":
		return fmt.Sprintf("/apps/feed/playlists/%d", c.ID)
	default:
		return "/apps/feed/"
	}
}

// itemNav holds everything the context bar and player need to render prev/next
// links. A zero ID means "no neighbor in that direction".
type itemNav struct {
	Ctx       string // encoded ctx (no leading "?")
	Label     string
	PrevID    int64
	PrevTitle string
	NextID    int64
	NextTitle string
}

// neighbors returns the items immediately before and after id in items. When
// mediaOnly is true, text items are skipped when choosing neighbors. Returns
// nil for a direction with no neighbor (boundary or id absent).
func neighbors(items []ItemWithSubscription, id int64, mediaOnly bool) (prev, next *ItemWithSubscription) {
	idx := -1
	for i := range items {
		if items[i].ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil, nil
	}
	playable := func(it ItemWithSubscription) bool {
		return !mediaOnly || it.Type == "audio" || it.Type == "video"
	}
	for i := idx - 1; i >= 0; i-- {
		if playable(items[i]) {
			prev = &items[i]
			break
		}
	}
	for i := idx + 1; i < len(items); i++ {
		if playable(items[i]) {
			next = &items[i]
			break
		}
	}
	return prev, next
}
