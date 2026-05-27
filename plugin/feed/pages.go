package feed

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
	"git.duckfam.us/jj-software/cog/ui"
)

const flashCookieName = "cog_flash"

// Pages holds the web UI handlers for the feed plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type pageFlash struct {
	Kind    string `json:"k"`
	Message string `json:"m"`
}

func (h *Pages) setFlash(w http.ResponseWriter, kind, message string) {
	data, _ := json.Marshal(pageFlash{Kind: kind, Message: message})
	h.plugin.ctx.Cookies().Set(w, &http.Cookie{
		Name:   flashCookieName,
		Value:  url.QueryEscape(string(data)),
		MaxAge: 60,
	})
}

func (h *Pages) getFlash(w http.ResponseWriter, r *http.Request) *pageFlash {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	h.plugin.ctx.Cookies().Clear(w, flashCookieName)
	val, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return nil
	}
	var f pageFlash
	if err := json.Unmarshal([]byte(val), &f); err != nil {
		return nil
	}
	return &f
}

func (h *Pages) userStore(r *http.Request) (*Store, error) {
	u := plugin.UserFromContext(r.Context())
	if u == nil {
		return nil, fmt.Errorf("no authenticated user")
	}
	db, err := h.plugin.DB(u.Username)
	if err != nil {
		return nil, err
	}
	return NewStore(db), nil
}

// Settings shows subscriptions, folders, filters, and playlists management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	subs, _ := store.ListSubscriptions()
	folders, _ := store.ListFolders()
	filters, _ := store.ListFilters()
	playlists, _ := store.ListPlaylists()

	var flashMsg, flashKind string
	if f := h.getFlash(w, r); f != nil {
		flashMsg, flashKind = f.Message, f.Kind
	}

	plugin.SetTitle(w, "Feed Settings")
	feedSettingsPage(subs, folders, filters, playlists, flashMsg, flashKind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// allowedKinds is the set of subscription kinds accepted on the add form.
var allowedKinds = map[string]bool{
	"rss":     true,
	"youtube": true,
	"url":     true,
}

// AddSubscription handles POST to create a new subscription.
func (h *Pages) AddSubscription(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rawURL := r.FormValue("url")
	if rawURL == "" {
		h.setFlash(w, "error", "URL is required.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	// Kind may be supplied (override); otherwise auto-detect from the URL.
	kind := r.FormValue("kind")
	if kind == "" || !allowedKinds[kind] {
		kind = DetectKind(r.Context(), rawURL)
	}

	sub := Subscription{
		Kind: kind,
		URL:  rawURL,
	}

	if fid := r.FormValue("folder_id"); fid != "" {
		if id, err := strconv.ParseInt(fid, 10, 64); err == nil {
			sub.FolderID.Int64 = id
			sub.FolderID.Valid = true
		}
	}

	subID, err := store.CreateSubscription(sub)
	if err != nil {
		h.setFlash(w, "error", "Could not add subscription: "+err.Error())
		slog.Error("feed: create subscription", "err", err)
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	u := plugin.UserFromContext(r.Context())
	go h.plugin.TriggerFetch(u.Username, subID)

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// DeleteSubscription handles POST /subscriptions/{id}/delete.
func (h *Pages) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := store.DeleteSubscription(id); err != nil {
		h.setFlash(w, "error", "Could not delete subscription.")
		slog.Error("feed: delete subscription", "id", id, "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// AddFolder handles POST to create a new folder.
func (h *Pages) AddFolder(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	if _, err := store.CreateFolder(name); err != nil {
		h.setFlash(w, "error", "Could not create folder: "+err.Error())
		slog.Error("feed: create folder", "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// DeleteFolder handles POST /folders/{id}/delete.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := store.DeleteFolder(id); err != nil {
		h.setFlash(w, "error", "Could not delete folder.")
		slog.Error("feed: delete folder", "id", id, "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// AddFilter handles POST to create a new filter.
func (h *Pages) AddFilter(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	queryStr := r.FormValue("query")
	if queryStr == "" {
		queryStr = "*"
	}

	if _, err := ParseFilterQuery(queryStr); err != nil {
		h.setFlash(w, "error", "Invalid query: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	action := r.FormValue("action")
	actionValue := r.FormValue("action_value")

	if _, err := store.CreateFilter(Filter{
		Query:       queryStr,
		Action:      action,
		ActionValue: actionValue,
	}); err != nil {
		h.setFlash(w, "error", "Could not create filter: "+err.Error())
		slog.Error("feed: create filter", "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// DeleteFilter handles POST /filters/{id}/delete.
func (h *Pages) DeleteFilter(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := store.DeleteFilter(id); err != nil {
		h.setFlash(w, "error", "Could not delete filter.")
		slog.Error("feed: delete filter", "id", id, "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// AddPlaylist handles POST to create a new playlist.
func (h *Pages) AddPlaylist(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	name := r.FormValue("name")
	queryStr := r.FormValue("query")

	if name == "" || queryStr == "" {
		h.setFlash(w, "error", "Name and query are required.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	if _, err := ParseFilterQuery(queryStr); err != nil {
		h.setFlash(w, "error", "Invalid query: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	if _, err := store.CreatePlaylist(name, queryStr); err != nil {
		h.setFlash(w, "error", "Could not create playlist: "+err.Error())
		slog.Error("feed: create playlist", "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// DeletePlaylist handles POST /playlists/{id}/delete.
func (h *Pages) DeletePlaylist(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if err := store.DeletePlaylist(id); err != nil {
		h.setFlash(w, "error", "Could not delete playlist.")
		slog.Error("feed: delete playlist", "id", id, "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// sortSubscriptionsForSidebar sorts subscriptions so those with errors appear
// first, then alphabetically by display title (case-insensitive).
func sortSubscriptionsForSidebar(subs []Subscription) {
	sort.SliceStable(subs, func(i, j int) bool {
		si, sj := subs[i], subs[j]
		// Errors first
		if (si.ErrorCount > 0) != (sj.ErrorCount > 0) {
			return si.ErrorCount > 0
		}
		// Then by display title (case-insensitive)
		return strings.ToLower(subscriptionDisplayTitle(si)) < strings.ToLower(subscriptionDisplayTitle(sj))
	})
}

// buildSidebar assembles sidebar data for the given active path.
func (h *Pages) buildSidebar(store *Store, activePath string) sidebarData {
	folders, _ := store.ListFolders()
	subs, _ := store.ListSubscriptions()
	sortSubscriptionsForSidebar(subs)
	tags, _ := store.ListTags()
	playlists, _ := store.ListPlaylists()
	unreadCounts, _ := store.CountUnreadBySubscription()
	folderUnread, _ := store.CountUnreadByFolder()
	totalUnread, _ := store.CountUnread()
	starredCount, _ := store.CountStarred()
	return sidebarData{
		Folders:       folders,
		Subscriptions: subs,
		Tags:          tags,
		Playlists:     playlists,
		ActivePath:    activePath,
		TotalUnread:   int(totalUnread),
		StarredCount:  int(starredCount),
		UnreadCounts:  unreadCounts,
		FolderUnread:  folderUnread,
	}
}

// PlaylistView renders items matching a saved playlist's query.
func (h *Pages) PlaylistView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pl, err := store.GetPlaylist(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sb := h.buildSidebar(store, fmt.Sprintf("/apps/feed/playlists/%d", id))
	items, _ := store.ListItemsForView(ViewSpec{All: true}, 500)
	items = applyQueryFilter(store, items, pl.Query)
	c := viewCtx{Kind: "playlist", ID: id}
	plugin.SetTitle(w, "Feed — "+pl.Name)
	feedPlaylistPage(sb, pl, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// applyQueryFilter filters an ItemWithSubscription slice using the query engine.
func applyQueryFilter(store *Store, items []ItemWithSubscription, q string) []ItemWithSubscription {
	expr, err := query.Parse(q)
	if err != nil {
		return items
	}
	filtered := items[:0]
	for _, it := range items {
		sub, _ := store.GetSubscription(it.SubscriptionID)
		folder := ""
		if sub.FolderID.Valid {
			if f, ferr := store.GetFolder(sub.FolderID.Int64); ferr == nil {
				folder = f.Name
			}
		}
		tagsList, _ := store.ItemTags(it.ID)
		dl, _ := store.GetDownload(it.ID)
		rec := BuildRecord(it.Item, sub, folder, tagsList, !it.IsRead, it.IsStarred, dl.State == "ready")
		if expr.Match(rec) {
			filtered = append(filtered, it)
		}
	}
	return filtered
}

// resolveViewItems returns the same ordered item slice the originating view
// rendered, so prev/next neighbors match exactly what the user saw.
func (h *Pages) resolveViewItems(store *Store, c viewCtx) ([]ItemWithSubscription, error) {
	const limit = 500
	switch c.Kind {
	case "starred":
		return store.ListItemsForView(ViewSpec{Starred: true}, limit)
	case "folder":
		return store.ListItemsForView(ViewSpec{FolderID: validID(c.ID)}, limit)
	case "sub":
		return store.ListItemsForView(ViewSpec{SubscriptionID: validID(c.ID)}, limit)
	case "tag":
		return store.ListItemsForView(ViewSpec{TagName: c.Name}, limit)
	case "playlist":
		pl, err := store.GetPlaylist(c.ID)
		if err != nil {
			return nil, fmt.Errorf("resolve playlist %d: %w", c.ID, err)
		}
		items, err := store.ListItemsForView(ViewSpec{All: true}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve playlist items: %w", err)
		}
		return applyQueryFilter(store, items, pl.Query), nil
	case "history":
		played, err := store.ListPlaybackHistory(limit)
		if err != nil {
			return nil, fmt.Errorf("resolve history: %w", err)
		}
		out := make([]ItemWithSubscription, 0, len(played))
		for _, p := range played {
			out = append(out, ItemWithSubscription{Item: p.Item, SubscriptionTitle: p.SubscriptionTitle})
		}
		return out, nil
	default: // all
		items, err := store.ListItemsForView(ViewSpec{All: true}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve all items: %w", err)
		}
		if c.Q != "" {
			items = applyQueryFilter(store, items, c.Q)
		}
		return items, nil
	}
}

// validID wraps an int64 as a valid sql.NullInt64.
func validID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

// Index shows the main feed item list with optional query filtering.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	q := r.URL.Query().Get("q")
	sb := h.buildSidebar(store, "/apps/feed/")

	items, err := store.ListItemsForView(ViewSpec{All: true}, 200)
	if err != nil {
		http.Error(w, "could not list items: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if q != "" {
		items = applyQueryFilter(store, items, q)
	}

	c := viewCtx{Kind: "all", Q: q}
	plugin.SetTitle(w, "Feed")
	feedIndexPage(sb, items, q, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// SubscriptionView shows items for a single subscription.
func (h *Pages) SubscriptionView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sub, err := store.GetSubscription(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sb := h.buildSidebar(store, "/apps/feed/subscription/"+idStr)
	items, _ := store.ListItemsForView(ViewSpec{SubscriptionID: sql.NullInt64{Int64: id, Valid: true}}, 200)

	c := viewCtx{Kind: "sub", ID: id}
	plugin.SetTitle(w, subscriptionDisplayTitle(sub))
	feedSubscriptionPage(sb, &sub, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// FolderView shows items for all subscriptions in a folder.
func (h *Pages) FolderView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	folder, err := store.GetFolder(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sb := h.buildSidebar(store, "/apps/feed/folder/"+idStr)
	items, _ := store.ListItemsForView(ViewSpec{FolderID: sql.NullInt64{Int64: id, Valid: true}}, 200)

	c := viewCtx{Kind: "folder", ID: id}
	plugin.SetTitle(w, folder.Name)
	feedFolderPage(sb, &folder, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// StarredView shows all starred items.
func (h *Pages) StarredView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(store, "/apps/feed/starred")
	items, _ := store.ListItemsForView(ViewSpec{Starred: true}, 200)

	c := viewCtx{Kind: "starred"}
	plugin.SetTitle(w, "Starred")
	feedStarredPage(sb, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// TagView shows items with a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tagName := r.PathValue("name")
	sb := h.buildSidebar(store, "/apps/feed/tag/"+tagName)
	items, _ := store.ListItemsForView(ViewSpec{TagName: tagName}, 200)

	c := viewCtx{Kind: "tag", Name: tagName}
	plugin.SetTitle(w, "Tag: "+tagName)
	feedTagPage(sb, tagName, items, plugin.CSRFToken(r), c.encode()).Render(r.Context(), w)
}

// RefreshSubscription sets next_check_at=1 on a subscription to trigger immediate re-poll.
func (h *Pages) RefreshSubscription(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := store.UpdateSubscriptionNextCheck(id, 1); err != nil {
		slog.Error("feed: refresh subscription", "id", id, "err", err)
	}
	u := plugin.UserFromContext(r.Context())
	go h.plugin.TriggerFetch(u.Username, id)
	http.Redirect(w, r, fmt.Sprintf("/apps/feed/subscription/%d", id), http.StatusSeeOther)
}

// MarkSubscriptionRead marks all items in a subscription as read.
func (h *Pages) MarkSubscriptionRead(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := store.MarkAllReadBySubscription(id); err != nil {
		slog.Error("feed: mark subscription read", "id", id, "err", err)
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/feed/subscription/%d", id), http.StatusSeeOther)
}

// MarkFolderRead marks all items in a folder's subscriptions as read.
func (h *Pages) MarkFolderRead(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := store.MarkAllReadByFolder(id); err != nil {
		slog.Error("feed: mark folder read", "id", id, "err", err)
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/feed/folder/%d", id), http.StatusSeeOther)
}

// MarkAllRead marks all items as read and redirects to index.
func (h *Pages) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := store.MarkAllRead(); err != nil {
		slog.Error("feed: mark all read", "err", err)
	}
	http.Redirect(w, r, "/apps/feed/", http.StatusSeeOther)
}

// MarkItemRead force-marks a single item as read (non-toggle, used by inline expand).
func (h *Pages) MarkItemRead(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := store.MarkItemRead(id); err != nil {
		slog.Error("feed: mark item read", "id", id, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ItemDetail shows a single feed item, marking it read on first view.
func (h *Pages) ItemDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ip, err := store.GetItemPlayback(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Text items are "read" once viewed. Audio/video are only considered
	// read when fully played (see MarkComplete).
	if ip.Type == "text" {
		if err := store.MarkRead(id, true); err != nil {
			slog.Error("feed: mark read", "id", id, "err", err)
		}
	}
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		slog.Error("feed: load player prefs", "err", err)
		prefs = PlayerPrefs{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1, AutoplayNext: true}
	}
	artwork := ip.ThumbnailURL
	tags, _ := store.ItemTags(id)
	plugin.SetTitle(w, ip.Title)
	feedItemDetail(ip, tags, prefs, artwork, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// UpdateProgress handles POST to save playback position.
func (h *Pages) UpdateProgress(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	progressSec, err := strconv.ParseFloat(r.FormValue("progress"), 64)
	if err != nil {
		http.Error(w, "invalid progress", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := store.SetPlayback(id, progressSec, false); err != nil {
		slog.Error("feed: update progress", "id", id, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// MarkComplete handles POST to mark an item as completed.
func (h *Pages) MarkComplete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := store.SetPlayback(id, 0, true); err != nil {
		slog.Error("feed: mark complete playback", "id", id, "err", err)
	}
	if err := store.MarkRead(id, true); err != nil {
		slog.Error("feed: mark complete read", "id", id, "err", err)
	}
	// Auto-advance: find the next unplayed media item and render its player.
	next, err := store.NextUnplayedMedia(id)
	if err == nil {
		if renderPlayerFragment(w, r, store, next.ID) {
			return
		}
	}
	// No next item — return empty body; HTMX swap removes the player.
	w.WriteHeader(http.StatusOK)
}

// renderPlayerFragment renders the feedPlayer fragment for itemID, returning
// true on success. On any error it writes nothing and returns false, leaving
// the caller to send a fallback (empty body / 204).
func renderPlayerFragment(w http.ResponseWriter, r *http.Request, store *Store, itemID int64) bool {
	ip, err := store.GetItemPlayback(itemID)
	if err != nil {
		return false
	}
	prefs, perr := store.LoadPlayerPrefs()
	if perr != nil {
		prefs = PlayerPrefs{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1, AutoplayNext: true}
	}
	feedPlayer(ip, prefs, ip.ThumbnailURL, plugin.CSRFToken(r)).Render(r.Context(), w)
	return true
}

// AdjacentPlayer renders the player for the media item before/after the given
// one (?dir=prev|next), without changing playback state. Returns 204 when there
// is no neighbor in that direction.
func (h *Pages) AdjacentPlayer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	dir := r.URL.Query().Get("dir")
	if dir != "next" && dir != "prev" {
		http.Error(w, "dir must be next or prev", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	adj, err := store.AdjacentMedia(id, dir)
	if err != nil {
		// No neighbor (sql.ErrNoRows) or query error — nothing to advance to.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !renderPlayerFragment(w, r, store, adj.ID) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// StreamURL returns the direct stream URL for a YouTube item.
func (h *Pages) StreamURL(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, err := store.GetItem(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	streamURL, err := ResolveYouTubeStreamURL(r.Context(), it.MediaURL)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": streamURL})
}

// Ranges returns the unified mute (profanity) + skip (silence) ranges for an item.
// Silence ranges are computed lazily on first request when a local file exists.
func (h *Pages) Ranges(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d, err := store.GetDownload(id)
	if err != nil {
		// No download row yet — nothing to analyze.
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": []Range{}})
		return
	}

	ranges := []Range{}

	// Mute ranges from profanity captions (when captions + plugin present).
	if d.CaptionsPath != "" {
		if prof := h.plugin.Profanity(); prof != nil {
			if f, ferr := os.Open(d.CaptionsPath); ferr == nil {
				u := plugin.UserFromContext(r.Context())
				captions := profanity.ParseVTT(f)
				f.Close()
				for _, ar := range prof.AudioRanges(u.Username, captions) {
					ranges = append(ranges, Range{Start: ar.Start, End: ar.End, Action: "mute"})
				}
			} else {
				slog.Error("feed: open captions", "id", id, "err", ferr)
			}
		}
	}

	// Skip ranges from silence detection (lazy, cached, requires local file).
	if d.LocalPath != "" {
		skips, derr := h.silenceRanges(r.Context(), store, id, d)
		if derr != nil {
			slog.Error("feed: silence ranges", "id", id, "err", derr)
		}
		ranges = append(ranges, skips...)
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": ranges})
}

// silenceRanges returns cached silence ranges, computing them once if not yet done.
func (h *Pages) silenceRanges(ctx context.Context, store *Store, id int64, d Download) ([]Range, error) {
	if d.SilenceState == "done" {
		return store.GetSilenceRanges(id)
	}
	rs, err := detectSilence(ctx, d.LocalPath)
	if err != nil {
		// Cache an empty "done" result so we don't recompute in a tight loop.
		if serr := store.SetSilenceRanges(id, nil); serr != nil {
			slog.Error("feed: cache empty silence ranges", "id", id, "err", serr)
		}
		return nil, err
	}
	if serr := store.SetSilenceRanges(id, rs); serr != nil {
		return rs, serr
	}
	return rs, nil
}

// ItemMedia serves item media: prefers clean_audio_path, then local_path, then 302 to media_url.
func (h *Pages) ItemMedia(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, err := store.GetItem(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dl, _ := store.GetDownload(id)
	if dl.CleanAudioPath != "" {
		http.ServeFile(w, r, dl.CleanAudioPath)
		return
	}
	if dl.LocalPath != "" {
		http.ServeFile(w, r, dl.LocalPath)
		return
	}
	if it.MediaURL != "" {
		http.Redirect(w, r, it.MediaURL, http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

// ToggleRead toggles the read state of an item. For HTMX requests it returns
// the updated item row HTML for outerHTML swap; otherwise it redirects back.
func (h *Pages) ToggleRead(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, err := store.GetItem(id)
	if err == nil {
		if err := store.MarkRead(id, !it.IsRead); err != nil {
			slog.Error("feed: toggle read", "id", id, "err", err)
		}
	}
	// If HTMX triggered this request, return the updated row for outerHTML swap.
	if r.Header.Get("HX-Request") == "true" {
		it, err = store.GetItem(id)
		if err != nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		sub, _ := store.GetSubscription(it.SubscriptionID)
		iws := ItemWithSubscription{Item: it, SubscriptionTitle: subscriptionDisplayTitle(sub)}
		feedItemRow(iws, plugin.CSRFToken(r), "").Render(r.Context(), w)
		return
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/apps/feed/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

// AddItemTag adds a tag to an item. POST /apps/feed/items/{id}/tag with form tag=NAME.
func (h *Pages) AddItemTag(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name := strings.TrimSpace(r.FormValue("tag"))
	if name == "" {
		h.setFlash(w, "error", "Tag name is required.")
		http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
		return
	}
	if err := store.AddTag(id, name); err != nil {
		h.setFlash(w, "error", "Could not add tag: "+err.Error())
		http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		tags, _ := store.ItemTags(id)
		feedItemTagRow(id, tags, plugin.CSRFToken(r)).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

// RemoveItemTag removes a tag from an item. POST /apps/feed/items/{id}/tag/{name}/delete.
func (h *Pages) RemoveItemTag(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	name := r.PathValue("name")
	if err := store.RemoveTag(id, name); err != nil {
		h.setFlash(w, "error", "Could not remove tag: "+err.Error())
		http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		tags, _ := store.ItemTags(id)
		feedItemTagRow(id, tags, plugin.CSRFToken(r)).Render(r.Context(), w)
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

// FilterPreview returns a preview of items matching a filter query (HTMX partial).
func (h *Pages) FilterPreview(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("query")
	if len(q) > 200 {
		q = q[:200]
	}
	if q == "" {
		// Empty query: render empty results without error.
		feedFilterPreviewResults(nil).Render(r.Context(), w)
		return
	}
	expr, err := query.Parse(q)
	if err != nil {
		http.Error(w, "Parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	items, err := store.ListItemsForView(ViewSpec{All: true}, 200)
	if err != nil {
		http.Error(w, "Could not load items: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var matched []ItemWithSubscription
	for _, it := range items {
		if len(matched) >= 10 {
			break
		}
		sub, _ := store.GetSubscription(it.SubscriptionID)
		folder := ""
		if sub.FolderID.Valid {
			if f, ferr := store.GetFolder(sub.FolderID.Int64); ferr == nil {
				folder = f.Name
			}
		}
		tagsList, _ := store.ItemTags(it.ID)
		dl, _ := store.GetDownload(it.ID)
		rec := BuildRecord(it.Item, sub, folder, tagsList, !it.IsRead, it.IsStarred, dl.State == "ready")
		if expr.Match(rec) {
			matched = append(matched, it)
		}
	}
	feedFilterPreviewResults(matched).Render(r.Context(), w)
}

// SystemSettings renders the admin-only feed system settings page.
func (h *Pages) SystemSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	db := store.DB()
	model := string(SettingWhisperModel.MustGet(db))
	modelsDir := SettingWhisperModelsDir.MustGet(db)
	cliPath := SettingWhisperCLIPath.MustGet(db)
	var flashMsg, flashKind string
	if f := h.getFlash(w, r); f != nil {
		flashMsg, flashKind = f.Message, f.Kind
	}
	plugin.SetTitle(w, "Feed System Settings")
	feedSystemSettingsPage(model, modelsDir, cliPath, flashMsg, flashKind).Render(r.Context(), w)
}

// SaveSystemSettings persists model selection + paths.
func (h *Pages) SaveSystemSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		h.setFlash(w, "error", "Failed to open DB: "+err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	db := store.DB()
	model := WhisperModel(r.FormValue("whisper_model"))
	if model != WhisperNone && WhisperModelFile(model) == "" {
		h.setFlash(w, "error", "Unknown whisper model: "+string(model))
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	if err := SettingWhisperModel.Set(db, model); err != nil {
		h.setFlash(w, "error", err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	if err := SettingWhisperModelsDir.Set(db, r.FormValue("whisper_models_dir")); err != nil {
		h.setFlash(w, "error", err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	if err := SettingWhisperCLIPath.Set(db, r.FormValue("whisper_cli_path")); err != nil {
		h.setFlash(w, "error", err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	h.setFlash(w, "success", "Saved.")
	http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
}

// Import handles POST /apps/feed/import with multipart form-data.
// The form must include:
//   - format: "antennapod", "newpipe", or "youtube-csv"
//   - file:   the uploaded file
func (h *Pages) Import(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		h.setFlash(w, "error", "Upload too large.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	format := r.FormValue("format")
	file, _, err := r.FormFile("file")
	if err != nil {
		h.setFlash(w, "error", "No file uploaded.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 20<<20))
	if err != nil {
		h.setFlash(w, "error", "Failed to read file: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	var n int
	switch format {
	case "antennapod":
		n, err = ImportAntennaPod(store, data)
	case "newpipe":
		n, err = ImportNewPipe(store, data)
	case "youtube-csv":
		n, err = ImportYouTubeTakeout(store, data)
	default:
		h.setFlash(w, "error", "Unknown import format: "+format)
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	if err != nil {
		h.setFlash(w, "error", "Import failed: "+err.Error())
	} else {
		u := plugin.UserFromContext(r.Context())
		go h.plugin.TriggerFetch(u.Username, 0)
		h.setFlash(w, "success", fmt.Sprintf("Imported %d subscriptions.", n))
	}
	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// ImportOPML handles POST of an OPML file to bulk-add RSS subscriptions.
func (h *Pages) ImportOPML(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := r.ParseMultipartForm(2 << 20); err != nil {
		h.setFlash(w, "error", "Upload too large or malformed.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	file, _, err := r.FormFile("opml")
	if err != nil {
		h.setFlash(w, "error", "No OPML file uploaded.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		h.setFlash(w, "error", "Failed to read OPML: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	n, err := store.ImportOPML(data)
	if err != nil {
		h.setFlash(w, "error", "OPML parse error: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}
	u := plugin.UserFromContext(r.Context())
	go h.plugin.TriggerFetch(u.Username, 0)
	h.setFlash(w, "success", fmt.Sprintf("Imported %d subscriptions.", n))
	http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
}

// ExportOPML returns the user's RSS subscriptions as an OPML file download.
func (h *Pages) ExportOPML(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := store.ExportOPML()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", `attachment; filename="cog-feeds.opml"`)
	_, _ = w.Write(data)
}

// HistoryView shows items the user has played, ordered by last_played_at DESC.
func (h *Pages) HistoryView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items, _ := store.ListPlaybackHistory(200)
	sb := h.buildSidebar(store, "/apps/feed/history")
	plugin.SetTitle(w, "Feed — History")
	feedHistoryPage(sb, items, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// SubscriptionSettings shows per-subscription options.
func (h *Pages) SubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sub, err := store.GetSubscription(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	folders, _ := store.ListFolders()
	var flashMsg, flashKind string
	if f := h.getFlash(w, r); f != nil {
		flashMsg, flashKind = f.Message, f.Kind
	}
	plugin.SetTitle(w, "Feed — "+subscriptionDisplayTitle(sub))
	feedSubscriptionSettingsPage(sub, folders, flashMsg, flashKind, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// SaveSubscriptionSettings handles POST of the per-subscription settings form.
func (h *Pages) SaveSubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	paused := r.FormValue("paused") == "on"
	if err := store.PauseSubscription(id, paused); err != nil {
		h.setFlash(w, "error", err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	if iv := r.FormValue("poll_interval_sec"); iv != "" {
		if n, parseErr := strconv.ParseInt(iv, 10, 64); parseErr == nil && n > 0 {
			if err := store.UpdateSubscriptionPollInterval(id, n); err != nil {
				h.setFlash(w, "error", err.Error())
				http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
				return
			}
		}
	}
	fv := r.FormValue("folder_id")
	var folderID sql.NullInt64
	if fv != "" {
		if n, parseErr := strconv.ParseInt(fv, 10, 64); parseErr == nil {
			folderID = sql.NullInt64{Int64: n, Valid: true}
		}
	}
	if err := store.UpdateSubscriptionFolder(id, folderID); err != nil {
		h.setFlash(w, "error", err.Error())
		http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
		return
	}
	h.setFlash(w, "success", "Saved.")
	http.Redirect(w, r, r.URL.Path, http.StatusSeeOther)
}

// AdminStatus shows orchestrator activity and per-user queue counts.
func (h *Pages) AdminStatus(w http.ResponseWriter, r *http.Request) {
	status := orchestratorGetStatus()
	store, _ := h.userStore(r)
	var pendingDL, pendingP, pendingT int
	if store != nil {
		if dl, err := store.PendingDownloads(10_000); err == nil {
			pendingDL = len(dl)
		}
		if p, err := store.PendingProfanityReady(10_000); err == nil {
			pendingP = len(p)
		}
		if t, err := store.PendingTranscribeReady(10_000); err == nil {
			pendingT = len(t)
		}
	}
	flash := h.getFlash(w, r)
	plugin.SetTitle(w, "Feed Admin Status")
	feedAdminStatusPage(status, pendingDL, pendingP, pendingT, flash, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// PurgeCache removes all downloaded media and caption files for the current user.
func (h *Pages) PurgeCache(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	root := filepath.Join(h.plugin.DataDir(), u.Username, "app_data", "feed", "media")
	if err := os.RemoveAll(root); err != nil {
		h.setFlash(w, "error", "Purge failed: "+err.Error())
	} else {
		h.setFlash(w, "success", "Purged downloaded media cache.")
	}
	captionsRoot := filepath.Join(h.plugin.DataDir(), u.Username, "app_data", "feed", "captions")
	_ = os.RemoveAll(captionsRoot)
	store, err := h.userStore(r)
	if err == nil {
		_, _ = store.DB().Exec(`UPDATE downloads SET local_path='', clean_audio_path='', captions_path='', state='queued'`)
	}
	http.Redirect(w, r, "/apps/feed/admin/status", http.StatusSeeOther)
}

// ToggleStar toggles the starred state of an item then redirects back.
// ToggleStar toggles the starred state of an item. For HTMX requests it returns
// the updated star button HTML for outerHTML swap; otherwise it redirects back.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	it, err := store.GetItem(id)
	if err == nil {
		if err := store.MarkStarred(id, !it.IsStarred); err != nil {
			slog.Error("feed: toggle star", "id", id, "err", err)
		}
	}
	// If HTMX triggered this, return the updated star button for outerHTML swap.
	if r.Header.Get("HX-Request") == "true" {
		it, err = store.GetItem(id)
		if err != nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		ui.Star(it.IsStarred, fmt.Sprintf("/apps/feed/items/%d/star", it.ID)).Render(r.Context(), w)
		return
	}
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/apps/feed/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}

// SavePlayerPrefs persists per-user player preferences (gear-menu changes).
func (h *Pages) SavePlayerPrefs(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cur, err := store.LoadPlayerPrefs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Only overwrite fields actually present in the form (partial updates).
	if v := r.FormValue("speed"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cur.Speed = min(max(f, 0.25), 4.0)
		}
	}
	if v := r.FormValue("skip_back_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cur.SkipBackSec = min(max(n, 1), 120)
		}
	}
	if v := r.FormValue("skip_fwd_sec"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cur.SkipFwdSec = min(max(n, 1), 120)
		}
	}
	if v := r.FormValue("volume"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cur.Volume = min(max(f, 0), 1)
		}
	}
	if v := r.FormValue("autoplay_next"); v != "" {
		cur.AutoplayNext = v == "true" || v == "1" || v == "on"
	}
	if v := r.FormValue("skip_silence"); v != "" {
		cur.SkipSilence = v == "true" || v == "1" || v == "on"
	}
	if err := store.SavePlayerPrefs(cur); err != nil {
		slog.Error("feed: save player prefs", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
