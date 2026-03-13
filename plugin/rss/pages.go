package rss

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the RSS plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type sidebarData struct {
	Folders      []Folder
	Feeds        []Feed
	UnreadCounts map[int64]int
	TotalUnread  int
	StarredCount int
	ActivePath   string
}

func feedSortKey(f Feed) int {
	if f.LastError != "" {
		return 2 // failed
	}
	if f.LastCheckedAt == 0 {
		return 1 // unsynced / pending
	}
	return 0 // synced OK
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) sidebarData {
	folders, _ := ListFolders(db)
	feeds, _ := ListFeeds(db)
	slices.SortStableFunc(feeds, func(a, b Feed) int {
		return feedSortKey(a) - feedSortKey(b)
	})
	unreadCounts, _ := FeedUnreadCounts(db)
	totalUnread, _ := UnreadCount(db)
	starredCount, _ := StarredCount(db)
	return sidebarData{
		Folders:      folders,
		Feeds:        feeds,
		UnreadCounts: unreadCounts,
		TotalUnread:  totalUnread,
		StarredCount: starredCount,
		ActivePath:   activePath,
	}
}

// Index shows unread items across all feeds.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/rss/")
	if r.URL.Query().Get("partial") == "sidebar" {
		rssSidebar(sb).Render(r.Context(), w)
		return
	}
	plugin.SetTitle(w, "News")
	items, _ := UnreadItems(db, 200)
	rssIndexPage(sb, items, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Feed shows items for a single feed.
func (h *Pages) Feed(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid feed id", http.StatusBadRequest)
		return
	}
	feed, err := GetFeed(db, id)
	if err != nil {
		http.Error(w, "feed not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, "/apps/rss/feed/"+idStr)
	if r.URL.Query().Get("partial") == "sidebar" {
		rssSidebar(sb).Render(r.Context(), w)
		return
	}
	plugin.SetTitle(w, feed.Title)
	items, _ := FeedItems(db, id, 200)
	rssFeedPage(sb, feed, items, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Starred shows starred items.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/rss/starred")
	if r.URL.Query().Get("partial") == "sidebar" {
		rssSidebar(sb).Render(r.Context(), w)
		return
	}
	plugin.SetTitle(w, "Starred")
	items, _ := StarredItems(db, 200)
	rssStarredPage(sb, items, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// FolderView shows items for all feeds in a folder.
func (h *Pages) FolderView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	folder, err := GetFolder(db, id)
	if err != nil {
		http.Error(w, "folder not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, "/apps/rss/folder/"+idStr)
	if r.URL.Query().Get("partial") == "sidebar" {
		rssSidebar(sb).Render(r.Context(), w)
		return
	}
	plugin.SetTitle(w, folder.Name)
	items, _ := FolderItems(db, id, 200)
	rssFolderPage(sb, folder, items, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Settings shows feed/folder management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/rss/settings")
	if r.URL.Query().Get("partial") == "sidebar" {
		rssSidebar(sb).Render(r.Context(), w)
		return
	}
	plugin.SetTitle(w, "RSS Settings")
	feeds, _ := ListFeeds(db)
	folders, _ := ListFolders(db)
	errMsg := r.URL.Query().Get("err")
	rssSettingsPage(sb, feeds, folders, errMsg, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// AddFeed handles POST to create a new feed subscription.
func (h *Pages) AddFeed(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawURL := r.FormValue("url")
	if rawURL == "" {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}

	resolvedURL, err := ResolveURL(h.plugin.client, rawURL)
	if err != nil {
		http.Redirect(w, r, "/apps/rss/settings?err="+url.QueryEscape("Could not find a feed: "+err.Error()), http.StatusSeeOther)
		return
	}

	var folderID sql.NullInt64
	if fid := r.FormValue("folder_id"); fid != "" {
		if id, err := strconv.ParseInt(fid, 10, 64); err == nil {
			folderID = sql.NullInt64{Int64: id, Valid: true}
		}
	}

	feed, err := CreateFeed(db, resolvedURL, folderID)
	if err != nil {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}

	// Fetch immediately in background.
	u := plugin.UserFromContext(r.Context())
	go h.plugin.FetchNow(u.Username, feed.ID)

	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// DeleteFeed handles DELETE to remove a feed.
func (h *Pages) DeleteFeed(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := DeleteFeed(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// RefreshFeed handles POST to force an immediate feed refresh.
func (h *Pages) RefreshFeed(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	u := plugin.UserFromContext(r.Context())
	go h.plugin.FetchNow(u.Username, id)
	w.WriteHeader(http.StatusNoContent)
}

// MarkFeedRead handles POST to mark all items in a feed as read.
func (h *Pages) MarkFeedRead(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := MarkFeedRead(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/rss/feed/"+r.PathValue("id"), http.StatusSeeOther)
}

// AddFolder handles POST to create a new folder.
func (h *Pages) AddFolder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.FormValue("name")
	if name != "" {
		if _, err := CreateFolder(db, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// DeleteFolder handles DELETE to remove a folder.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := DeleteFolder(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// ToggleRead handles POST to toggle read state of an item.
func (h *Pages) ToggleRead(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := ToggleItemRead(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return updated item row for HTMX swap.
	item, err := GetItem(db, id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	feed, _ := GetFeed(db, item.FeedID)
	feedTitle := ""
	if feed != nil {
		feedTitle = feed.Title
	}
	iwf := ItemWithFeed{Item: *item, FeedTitle: feedTitle}
	_ = rssItemRow(iwf, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ToggleStar handles POST to toggle star state of an item.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := ToggleItemStar(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	item, err := GetItem(db, id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	feed, _ := GetFeed(db, item.FeedID)
	feedTitle := ""
	if feed != nil {
		feedTitle = feed.Title
	}
	iwf := ItemWithFeed{Item: *item, FeedTitle: feedTitle}
	_ = rssItemRow(iwf, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// MarkRead handles POST to mark an item as read (non-toggle).
func (h *Pages) MarkRead(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	SetItemRead(db, id, true)
	w.WriteHeader(http.StatusNoContent)
}

// MarkAllRead handles POST to mark all items as read.
func (h *Pages) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := MarkAllRead(db); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/rss/", http.StatusSeeOther)
}

// ImportOPML handles POST to import feeds from an OPML file.
func (h *Pages) ImportOPML(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}
	file, _, err := r.FormFile("opml")
	if err != nil {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}

	if _, err := ImportOPML(db, data); err != nil {
		http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
		return
	}

	// Fetch all newly imported feeds immediately in the background.
	u := plugin.UserFromContext(r.Context())
	feeds, _ := ListFeeds(db)
	go func() {
		for i := range feeds {
			if feeds[i].LastCheckedAt == 0 {
				h.plugin.FetchNow(u.Username, feeds[i].ID)
			}
		}
	}()

	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// SetAutoReadPattern handles POST to set the auto-read regex pattern for a feed.
func (h *Pages) SetAutoReadPattern(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pattern := r.FormValue("pattern")
	if err := SetFeedAutoReadPattern(db, id, pattern); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// ExportOPML handles GET to export feeds as OPML.
func (h *Pages) ExportOPML(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, err := ExportOPML(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", `attachment; filename="cog-feeds.opml"`)
	w.Write(data)
}
