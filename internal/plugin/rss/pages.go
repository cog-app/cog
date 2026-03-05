package rss

import (
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

type navProvider interface {
	NavItems() []plugin.NavItem
}

// Pages holds the web UI handlers for the RSS plugin.
type Pages struct {
	plugin *Plugin
	secret []byte
	nav    navProvider
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin, secret []byte, nav navProvider) *Pages {
	return &Pages{plugin: p, secret: secret, nav: nav}
}

func (h *Pages) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, h.secret)
	}
	var isAdmin bool
	if u != nil {
		isAdmin = u.IsAdmin
	}

	var nav []ui.NavItem
	for _, item := range h.nav.NavItems() {
		active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
		nav = append(nav, ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		})
	}

	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         nav,
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
	}
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := auth.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

type sidebarData struct {
	Folders      []Folder
	Feeds        []Feed
	UnreadCounts map[int64]int
	TotalUnread  int
	StarredCount int
	ActivePath   string
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) sidebarData {
	folders, _ := ListFolders(db)
	feeds, _ := ListFeeds(db)
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
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pd := h.pageData(r, "News")
	sb := h.buildSidebar(db, "/apps/rss/")
	items, _ := UnreadItems(db, 200)
	rssIndexPage(pd, sb, items).Render(r.Context(), w)
}

// Feed shows items for a single feed.
func (h *Pages) Feed(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	pd := h.pageData(r, feed.Title)
	sb := h.buildSidebar(db, "/apps/rss/feed/"+idStr)
	items, _ := FeedItems(db, id, 200)
	rssFeedPage(pd, sb, feed, items).Render(r.Context(), w)
}

// Starred shows starred items.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pd := h.pageData(r, "Starred")
	sb := h.buildSidebar(db, "/apps/rss/starred")
	items, _ := StarredItems(db, 200)
	rssStarredPage(pd, sb, items).Render(r.Context(), w)
}

// Settings shows feed/folder management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pd := h.pageData(r, "RSS Settings")
	sb := h.buildSidebar(db, "/apps/rss/settings")
	feeds, _ := ListFeeds(db)
	folders, _ := ListFolders(db)
	errMsg := r.URL.Query().Get("err")
	rssSettingsPage(pd, sb, feeds, folders, errMsg).Render(r.Context(), w)
}

// AddFeed handles POST to create a new feed subscription.
func (h *Pages) AddFeed(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	u := auth.UserFromContext(r.Context())
	go h.plugin.FetchNow(u.Username, feed.ID)

	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// DeleteFeed handles DELETE to remove a feed.
func (h *Pages) DeleteFeed(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteFeed(db, id)
	w.WriteHeader(http.StatusOK)
}

// MarkFeedRead handles POST to mark all items in a feed as read.
func (h *Pages) MarkFeedRead(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	MarkFeedRead(db, id)
	http.Redirect(w, r, "/apps/rss/feed/"+r.PathValue("id"), http.StatusSeeOther)
}

// AddFolder handles POST to create a new folder.
func (h *Pages) AddFolder(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.FormValue("name")
	if name != "" {
		CreateFolder(db, name)
	}
	http.Redirect(w, r, "/apps/rss/settings", http.StatusSeeOther)
}

// DeleteFolder handles DELETE to remove a folder.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteFolder(db, id)
	w.WriteHeader(http.StatusOK)
}

// ToggleRead handles POST to toggle read state of an item.
func (h *Pages) ToggleRead(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ToggleItemRead(db, id)
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
	rssItemRow(iwf, h.pageData(r, "").CSRFToken).Render(r.Context(), w)
}

// ToggleStar handles POST to toggle star state of an item.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ToggleItemStar(db, id)
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
	rssItemRow(iwf, h.pageData(r, "").CSRFToken).Render(r.Context(), w)
}

// MarkAllRead handles POST to mark all items as read.
func (h *Pages) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	MarkAllRead(db)
	http.Redirect(w, r, "/apps/rss/", http.StatusSeeOther)
}

// ImportOPML handles POST to import feeds from an OPML file.
func (h *Pages) ImportOPML(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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

	ImportOPML(db, data)

	// Fetch all newly imported feeds immediately in the background.
	u := auth.UserFromContext(r.Context())
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

// ExportOPML handles GET to export feeds as OPML.
func (h *Pages) ExportOPML(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
