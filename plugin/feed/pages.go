package feed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
	"git.duckfam.us/jj-software/cog/ui"
)

const flashCookieName = "cog_flash"

// feedViewLimit is the max items rendered per view. resolveViewItems must use
// the same value so prev/next neighbors match exactly what the user saw on screen.
const feedViewLimit = 500

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

// setFlash leaves a message for the host to show on the page the response
// redirects to. The cookie is written directly, not through plugin.SetFlash,
// because some of these handlers (the admin settings page) are not plugin
// actions and so have no one to carry the header across.
func (h *Pages) setFlash(w http.ResponseWriter, kind, message string) {
	data, err := json.Marshal(pageFlash{Kind: kind, Message: message})
	if err != nil {
		slog.Error("feed: encode flash", "err", err)
		return
	}
	h.plugin.ctx.Cookies().Set(w, &http.Cookie{
		Name:   flashCookieName,
		Value:  url.QueryEscape(string(data)),
		MaxAge: 60,
	})
}

// lookupError answers a failed lookup of the thing a request names: 404 when
// it does not exist, 500 for anything else.
func lookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	slog.Error("feed: lookup", "path", r.URL.Path, "err", err)
	httperr.Internal(w, r, err)
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

// SettingsRedirect sends the bare /settings path to the default tab.
func (h *Pages) SettingsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

func (h *Pages) SettingsSubscriptions(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	subs, err := store.ListSubscriptions()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	folders, err := store.ListFolders()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/settings/subscriptions")
	if !ok {
		return
	}
	plugin.SetTitle(w, "Subscriptions")
	ui.Render(w, r, feedSettingsSubscriptionsPage(sb, subs, folders, plugin.CSRFToken(r)))
}

func (h *Pages) SettingsFilters(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	filters, err := store.ListFilters()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/settings/filters")
	if !ok {
		return
	}
	plugin.SetTitle(w, "Filters")
	ui.Render(w, r, feedSettingsFiltersPage(sb, filters, plugin.CSRFToken(r)))
}

func (h *Pages) SettingsPlaylists(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	playlists, err := store.ListPlaylists()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/settings/playlists")
	if !ok {
		return
	}
	plugin.SetTitle(w, "Playlists")
	ui.Render(w, r, feedSettingsPlaylistsPage(sb, playlists, plugin.CSRFToken(r)))
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
		httperr.Internal(w, r, err)
		return
	}

	rawURL := r.FormValue("url")
	if rawURL == "" {
		h.setFlash(w, "error", "URL is required.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}

	if err := CheckURL(rawURL); err != nil {
		h.setFlash(w, "error", "Enter a web address starting with http:// or https://.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
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
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}

	u := plugin.UserFromContext(r.Context())
	go h.plugin.TriggerFetch(u.Username, subID)

	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// DeleteSubscription handles POST /subscriptions/{id}/delete.
func (h *Pages) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
	} else {
		// Its downloads count toward the user's quota; have the
		// orchestrator collect them now rather than on its next tick.
		h.plugin.TriggerFetch(plugin.Username(r), 0)
	}

	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// AddFolder handles POST to create a new folder.
func (h *Pages) AddFolder(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}

	if _, err := store.CreateFolder(name); err != nil {
		h.setFlash(w, "error", "Could not create folder: "+err.Error())
		slog.Error("feed: create folder", "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// DeleteFolder handles POST /folders/{id}/delete.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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

	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// AddFilter handles POST to create a new filter.
func (h *Pages) AddFilter(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	queryStr := r.FormValue("query")
	if queryStr == "" {
		queryStr = "*"
	}

	if _, err := ParseFilterQuery(queryStr); err != nil {
		h.setFlash(w, "error", "Invalid query: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings/filters", http.StatusSeeOther)
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

	http.Redirect(w, r, "/apps/feed/settings/filters", http.StatusSeeOther)
}

// DeleteFilter handles POST /filters/{id}/delete.
func (h *Pages) DeleteFilter(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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

	http.Redirect(w, r, "/apps/feed/settings/filters", http.StatusSeeOther)
}

// AddPlaylist handles POST to create a new playlist.
func (h *Pages) AddPlaylist(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	name := r.FormValue("name")
	queryStr := r.FormValue("query")

	if name == "" || queryStr == "" {
		h.setFlash(w, "error", "Name and query are required.")
		http.Redirect(w, r, "/apps/feed/settings/playlists", http.StatusSeeOther)
		return
	}

	if _, err := ParseFilterQuery(queryStr); err != nil {
		h.setFlash(w, "error", "Invalid query: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings/playlists", http.StatusSeeOther)
		return
	}

	if _, err := store.CreatePlaylist(name, queryStr); err != nil {
		h.setFlash(w, "error", "Could not create playlist: "+err.Error())
		slog.Error("feed: create playlist", "err", err)
	}

	http.Redirect(w, r, "/apps/feed/settings/playlists", http.StatusSeeOther)
}

// DeletePlaylist handles POST /playlists/{id}/delete.
func (h *Pages) DeletePlaylist(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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

	http.Redirect(w, r, "/apps/feed/settings/playlists", http.StatusSeeOther)
}

// sortSubscriptionsForSidebar sorts subscriptions so those with errors appear
// first, then alphabetically by display title (case-insensitive).
// buildSidebar assembles sidebar data for the given active path.
func (h *Pages) buildSidebar(store *Store, activePath string) (sidebarData, error) {
	folders, err := store.ListFolders()
	if err != nil {
		return sidebarData{}, fmt.Errorf("list folders: %w", err)
	}
	subs, err := store.ListSubscriptions()
	if err != nil {
		return sidebarData{}, fmt.Errorf("list subscriptions: %w", err)
	}
	tags, err := store.ListTags()
	if err != nil {
		return sidebarData{}, fmt.Errorf("list tags: %w", err)
	}
	playlists, err := store.ListPlaylists()
	if err != nil {
		return sidebarData{}, fmt.Errorf("list playlists: %w", err)
	}
	counts, err := store.LoadSidebarCounts()
	if err != nil {
		return sidebarData{}, fmt.Errorf("sidebar counts: %w", err)
	}
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		return sidebarData{}, fmt.Errorf("load prefs: %w", err)
	}
	return sidebarData{
		Folders:       folders,
		Subscriptions: subs,
		Tags:          tags,
		Playlists:     playlists,
		ActivePath:    activePath,
		TotalUnread:   counts.TotalUnread,
		StarredCount:  counts.StarredCount,
		UnreadCounts:  counts.BySubscription,
		FolderUnread:  counts.ByFolder,
		HideEmpty:     prefs.HideEmpty,
	}, nil
}

// sidebar builds the sidebar for a page. It writes the error response itself
// and returns ok=false when it fails.
func (h *Pages) sidebar(w http.ResponseWriter, r *http.Request, store *Store, activePath string) (sidebarData, bool) {
	sb, err := h.buildSidebar(store, activePath)
	if err != nil {
		httperr.Internal(w, r, err)
		return sidebarData{}, false
	}
	return sb, true
}

// prefs loads the user's preferences for a list page. It writes the error
// response itself and returns ok=false when it fails.
func (h *Pages) prefs(w http.ResponseWriter, r *http.Request, store *Store) (PlayerPrefs, bool) {
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		httperr.Internal(w, r, err)
		return PlayerPrefs{}, false
	}
	return prefs, true
}

// currentPath is the path of the page an HTMX request was made from.
func currentPath(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return ""
	}
	return u.Path
}

// rows is how a list page renders its rows.
func (h *Pages) rows(c viewCtx) rowOpts {
	return rowOpts{Ctx: c.encode(), HideSub: c.Kind == "sub", Now: h.plugin.ctx.Now()}
}

// PlaylistView renders items matching a saved playlist's query.
func (h *Pages) PlaylistView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pl, err := store.GetPlaylist(id)
	if err != nil {
		lookupError(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, fmt.Sprintf("/apps/feed/playlists/%d", id))
	if !ok {
		return
	}
	prefs, ok := h.prefs(w, r, store)
	if !ok {
		return
	}
	var expr query.Expr
	if pl.Query != "" {
		if e, perr := query.Parse(pl.Query); perr == nil {
			expr = e
		} else {
			slog.Warn("feed: playlist query does not parse; showing all items", "playlist_id", pl.ID, "query", pl.Query, "err", perr)
		}
	}
	if expr != nil {
		expr = query.ResolveRelativeDates(expr, *SearchSchema(), h.plugin.ctx.Now())
	}
	items, plErr := store.ListItemsForView(ViewSpec{
		All:      true,
		HideRead: prefs.HideEmpty,
		Query:    expr,
		Schema:   SearchSchema(),
	}, feedViewLimit)
	if plErr != nil {
		http.Error(w, "could not list items: "+plErr.Error(), http.StatusInternalServerError)
		return
	}
	if expr != nil && queryNeedsRefine(expr) {
		items, plErr = h.refineItems(store, items, expr)
		if plErr != nil {
			slog.Error("feed: playlist refine", "err", plErr)
			http.Error(w, "could not refine items: "+plErr.Error(), http.StatusInternalServerError)
			return
		}
	}
	c := viewCtx{Kind: "playlist", ID: id}
	plugin.SetTitle(w, "Feed — "+pl.Name)
	ui.Render(w, r, feedPlaylistPage(sb, pl, items, h.rows(c)))
}

// resolveViewItems returns the same ordered item slice the originating view
// rendered, so prev/next neighbors match exactly what the user saw.
// Navigation must traverse the FULL context list regardless of HideRead so that
// the current item (potentially just marked read) can always be found, and its
// neighbors are stable. List-page handlers apply their own HideRead via their
// own ViewSpec — this function intentionally omits it.
func (h *Pages) resolveViewItems(store *Store, c viewCtx) ([]ItemWithSubscription, error) {
	const limit = feedViewLimit
	var items []ItemWithSubscription
	var err error
	switch c.Kind {
	case "starred":
		items, err = store.ListItemsForView(ViewSpec{Starred: true}, limit)
	case "folder":
		items, err = store.ListItemsForView(ViewSpec{FolderID: validID(c.ID)}, limit)
	case "sub":
		items, err = store.ListItemsForView(ViewSpec{SubscriptionID: validID(c.ID)}, limit)
	case "tag":
		items, err = store.ListItemsForView(ViewSpec{TagName: c.Name}, limit)
	case "playlist":
		pl, perr := store.GetPlaylist(c.ID)
		if perr != nil {
			return nil, fmt.Errorf("resolve playlist %d: %w", c.ID, perr)
		}
		var expr query.Expr
		if pl.Query != "" {
			if e, qerr := query.Parse(pl.Query); qerr == nil {
				expr = e
			} else {
				slog.Warn("feed: playlist query does not parse; showing all items", "playlist_id", pl.ID, "query", pl.Query, "err", qerr)
			}
		}
		if expr != nil {
			expr = query.ResolveRelativeDates(expr, *SearchSchema(), h.plugin.ctx.Now())
		}
		items, err = store.ListItemsForView(ViewSpec{
			All: true, Query: expr, Schema: SearchSchema(),
		}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve playlist items: %w", err)
		}
		if expr != nil && queryNeedsRefine(expr) {
			items, err = h.refineItems(store, items, expr)
			if err != nil {
				return nil, fmt.Errorf("resolve playlist refine: %w", err)
			}
		}
	case "history":
		items, err = store.PlaybackHistoryItems(limit)
		if err != nil {
			return nil, fmt.Errorf("resolve history: %w", err)
		}
	default: // all
		var expr query.Expr
		if c.Q != "" {
			e, qerr := query.Parse(c.Q)
			if qerr != nil {
				// The list this came from showed the parse error and no
				// items, so there is nothing to step through.
				slog.Warn("feed: search query in view context does not parse", "query", c.Q, "err", qerr)
				return nil, nil
			}
			expr = e
		}
		if expr != nil {
			expr = query.ResolveRelativeDates(expr, *SearchSchema(), h.plugin.ctx.Now())
		}
		items, err = store.ListItemsForView(ViewSpec{
			All: true, Query: expr, Schema: SearchSchema(),
		}, limit)
		if err != nil {
			return nil, fmt.Errorf("resolve all items: %w", err)
		}
		if expr != nil && queryNeedsRefine(expr) {
			items, err = h.refineItems(store, items, expr)
			if err != nil {
				return nil, fmt.Errorf("resolve all items refine: %w", err)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve view items: %w", err)
	}
	return items, nil
}

// validID wraps an int64 as a valid sql.NullInt64.
func validID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

// queryNeedsRefine reports whether e contains a TextExpr/PhraseExpr — the only
// nodes the SQL compiler evaluates as a permissive superset. When false, the
// SQL result is already exact and no Go refine is needed.
func queryNeedsRefine(e query.Expr) bool {
	switch x := e.(type) {
	case *query.TextExpr, *query.PhraseExpr:
		return true
	case *query.NotExpr:
		return queryNeedsRefine(x.Inner)
	case *query.AndExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	case *query.OrExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	default:
		return false
	}
}

// refineItems filters the SQL-prefiltered rows down to an exact match of expr
// using bleve-exact semantics. Caller guarantees queryNeedsRefine(expr)==true.
// Builds a full query.Record per row (subscription/folder memoized by id;
// tags + download-state batch-loaded) and keeps rows where search.ExactMatch
// holds. Order preserved.
func (h *Pages) refineItems(store *Store, items []ItemWithSubscription, expr query.Expr) ([]ItemWithSubscription, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	tagsByItem, err := store.TagsByItems(ids)
	if err != nil {
		return nil, fmt.Errorf("refine tags: %w", err)
	}
	dlByItem, err := store.DownloadStateByItems(ids)
	if err != nil {
		return nil, fmt.Errorf("refine downloads: %w", err)
	}
	subCache := map[int64]Subscription{}
	folderCache := map[int64]string{}
	out := items[:0]
	for _, it := range items {
		sub, ok := subCache[it.SubscriptionID]
		if !ok {
			sub, err = store.GetSubscription(it.SubscriptionID)
			if err != nil {
				return nil, fmt.Errorf("refine subscription: %w", err)
			}
			subCache[it.SubscriptionID] = sub
		}
		folderName := ""
		if sub.FolderID.Valid {
			fn, ok := folderCache[sub.FolderID.Int64]
			if !ok {
				f, err := store.GetFolder(sub.FolderID.Int64)
				if err != nil {
					return nil, fmt.Errorf("refine folder: %w", err)
				}
				fn = f.Name
				folderCache[sub.FolderID.Int64] = fn
			}
			folderName = fn
		}
		rec := BuildRecord(it.Item, sub, folderName, tagsByItem[it.ID],
			!it.IsRead, it.IsStarred, dlByItem[it.ID] == "ready")
		if search.ExactMatch(expr, rec, *SearchSchema()) {
			out = append(out, it)
		}
	}
	return out, nil
}

// Index shows the main feed item list with optional query filtering.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	q := r.URL.Query().Get("q")
	prefs, ok := h.prefs(w, r, store)
	if !ok {
		return
	}

	items, qErr, err := h.searchItems(store, q, prefs.HideEmpty)
	if err != nil {
		slog.Error("feed: index items", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	sb, ok := h.sidebar(w, r, store, "/apps/feed/")
	if !ok {
		return
	}
	c := viewCtx{Kind: "all", Q: q}
	plugin.SetTitle(w, "Feed")
	ui.Render(w, r, feedIndexPage(sb, items, q, qErr, plugin.CSRFToken(r), h.rows(c)))
}

// searchItems lists the items the all-articles page shows for the search q.
// A q that does not parse is the user's mistake, not the server's: it comes
// back as qErr, the reason to show beside the search box, with no items —
// showing every item instead would read as "all of these match".
func (h *Pages) searchItems(store *Store, q string, hideRead bool) (items []ItemWithSubscription, qErr string, err error) {
	var expr query.Expr
	if q != "" {
		e, perr := query.Parse(q)
		if perr != nil {
			return nil, perr.Error(), nil
		}
		expr = query.ResolveRelativeDates(e, *SearchSchema(), h.plugin.ctx.Now())
	}
	items, err = store.ListItemsForView(ViewSpec{
		All:      true,
		HideRead: hideRead,
		Query:    expr,
		Schema:   SearchSchema(),
	}, feedViewLimit)
	if err != nil {
		return nil, "", fmt.Errorf("could not list items: %w", err)
	}
	if expr != nil && queryNeedsRefine(expr) {
		if items, err = h.refineItems(store, items, expr); err != nil {
			return nil, "", fmt.Errorf("could not refine items: %w", err)
		}
	}
	return items, "", nil
}

// SubscriptionView shows items for a single subscription.
func (h *Pages) SubscriptionView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		lookupError(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/subscription/"+idStr)
	if !ok {
		return
	}
	prefs, ok := h.prefs(w, r, store)
	if !ok {
		return
	}
	items, err := store.ListItemsForView(ViewSpec{SubscriptionID: sql.NullInt64{Int64: id, Valid: true}, HideRead: prefs.HideEmpty}, feedViewLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	c := viewCtx{Kind: "sub", ID: id}
	plugin.SetTitle(w, subscriptionDisplayTitle(sub))
	ui.Render(w, r, feedSubscriptionPage(sb, &sub, items, plugin.CSRFToken(r), h.rows(c)))
}

// FolderView shows items for all subscriptions in a folder.
func (h *Pages) FolderView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		lookupError(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/folder/"+idStr)
	if !ok {
		return
	}
	prefs, ok := h.prefs(w, r, store)
	if !ok {
		return
	}
	items, err := store.ListItemsForView(ViewSpec{FolderID: sql.NullInt64{Int64: id, Valid: true}, HideRead: prefs.HideEmpty}, feedViewLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	c := viewCtx{Kind: "folder", ID: id}
	plugin.SetTitle(w, folder.Name)
	ui.Render(w, r, feedFolderPage(sb, &folder, items, plugin.CSRFToken(r), h.rows(c)))
}

// StarredView shows all starred items.
func (h *Pages) StarredView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/starred")
	if !ok {
		return
	}
	items, err := store.ListItemsForView(ViewSpec{Starred: true}, feedViewLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	c := viewCtx{Kind: "starred"}
	plugin.SetTitle(w, "Starred")
	ui.Render(w, r, feedStarredPage(sb, items, h.rows(c)))
}

// TagView shows items with a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	tagName := r.PathValue("name")
	sb, ok := h.sidebar(w, r, store, "/apps/feed/tag/"+url.PathEscape(tagName))
	if !ok {
		return
	}
	prefs, ok := h.prefs(w, r, store)
	if !ok {
		return
	}
	items, err := store.ListItemsForView(ViewSpec{TagName: tagName, HideRead: prefs.HideEmpty}, feedViewLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	c := viewCtx{Kind: "tag", Name: tagName}
	plugin.SetTitle(w, "Tag: "+tagName)
	ui.Render(w, r, feedTagPage(sb, tagName, items, h.rows(c)))
}

// RefreshSubscription sets next_check_at=1 on a subscription to trigger immediate re-poll.
func (h *Pages) RefreshSubscription(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	ip, err := store.GetItemPlayback(id)
	if err != nil {
		lookupError(w, r, err)
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
	tags, err := store.ItemTags(id)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	c := parseViewCtx(r)
	items, rerr := h.resolveViewItems(store, c)
	if rerr != nil {
		slog.Error("feed: resolve view items", "err", rerr)
	}
	nav := buildItemNav(store, c, items, id)
	sb, ok := h.sidebar(w, r, store, c.path())
	if !ok {
		return
	}
	plugin.SetTitle(w, ip.Title)
	ui.Render(w, r, feedItemDetail(sb, nav, ip, tags, prefs, artwork, plugin.CSRFToken(r), h.plugin.ctx.Now()))
}

// TalksPreview reads a page of talks and answers, for HTMX, with the talks
// it found to choose from, the talks without media, and the talk pages that
// failed to load.
func (h *Pages) TalksPreview(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	pageURL := strings.TrimSpace(r.FormValue("url"))
	if err := CheckURL(pageURL); err != nil {
		ui.Render(w, r, feedTalksMessage("error", "Enter a web address starting with http:// or https://."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), talksScanTimeout)
	defer cancel()
	scan, err := ScanTalksPage(ctx, pageURL)
	if err != nil {
		slog.Info("feed: scan talks page", "url", pageURL, "err", err)
		ui.Render(w, r, feedTalksMessage("error", "Could not read that page: "+err.Error()))
		return
	}
	folders, err := store.ListFolders()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	ui.Render(w, r, feedTalksPreview(pageURL, scan, folders, plugin.CSRFToken(r)))
}

// AddTalks adds the talks chosen from a preview under a subscription for
// their page and queues them for download.
func (h *Pages) AddTalks(w http.ResponseWriter, r *http.Request) {
	const back = "/apps/feed/settings/subscriptions"
	fail := func(msg string) {
		h.setFlash(w, "error", msg)
		http.Redirect(w, r, back, http.StatusSeeOther)
	}
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		fail("The form could not be read.")
		return
	}
	pageURL := r.FormValue("page_url")
	if err := CheckURL(pageURL); err != nil {
		fail("The page address is missing or invalid.")
		return
	}
	var folderID sql.NullInt64
	if fid := r.FormValue("folder_id"); fid != "" {
		id, err := strconv.ParseInt(fid, 10, 64)
		if err != nil {
			fail("Choose a folder from the list.")
			return
		}
		folderID = sql.NullInt64{Int64: id, Valid: true}
	}

	// Only what was ticked is added; nothing ticked adds nothing.
	var talks []PageTalk
	for _, i := range r.Form["talk"] {
		t := PageTalk{
			PageURL:  r.FormValue("page_" + i),
			Title:    r.FormValue("title_" + i),
			Speaker:  r.FormValue("speaker_" + i),
			MediaURL: r.FormValue("media_" + i),
			Type:     r.FormValue("type_" + i),
		}
		if (t.Type != "audio" && t.Type != "video") || CheckURL(t.PageURL) != nil || CheckURL(t.MediaURL) != nil {
			fail("A chosen talk has no media to download.")
			return
		}
		talks = append(talks, t)
	}
	if len(talks) == 0 {
		fail("Choose at least one talk to add.")
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = pageURL
	}
	subID, added, err := store.AddPageTalks(pageURL, title, folderID, talks, h.plugin.ctx.Now().Unix())
	if errors.Is(err, ErrNotTalksPage) {
		fail("You already subscribe to that page as a feed.")
		return
	}
	if err != nil {
		slog.Error("feed: add talks", "url", pageURL, "err", err)
		fail("Could not add the talks.")
		return
	}
	h.plugin.TriggerFetch(plugin.Username(r), 0)
	msg := fmt.Sprintf("Added %d talks. They will download in the background.", added)
	if again := len(talks) - added; again > 0 {
		msg = fmt.Sprintf("Added %d talks; %d were already here. They will download in the background.", added, again)
	}
	h.setFlash(w, "success", msg)
	http.Redirect(w, r, fmt.Sprintf("/apps/feed/subscription/%d", subID), http.StatusSeeOther)
}

// DownloadItem queues an audio or video item for download and the
// preprocessing its subscription's filters ask for, or retries one that
// failed. The orchestrator does the work under the plugin's lifetime, so it
// outlives this request; the item page shows its progress.
func (h *Pages) DownloadItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	it, err := store.GetItem(id)
	if err != nil {
		lookupError(w, r, err)
		return
	}
	back := itemHref(id, parseViewCtx(r).encode())
	if it.Type == "text" || it.MediaURL == "" {
		h.setFlash(w, "error", "This item has nothing to download.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err := store.EnqueueDownload(id); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	h.plugin.TriggerFetch(plugin.Username(r), 0)
	http.Redirect(w, r, back, http.StatusSeeOther)
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
		httperr.Internal(w, r, err)
		return
	}
	if err := store.SetPlayback(id, progressSec, false, time.Now()); err != nil {
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
		httperr.Internal(w, r, err)
		return
	}
	if err := store.SetPlayback(id, 0, true, time.Now()); err != nil {
		slog.Error("feed: mark complete playback", "id", id, "err", err)
	}
	if err := store.MarkRead(id, true); err != nil {
		slog.Error("feed: mark complete read", "id", id, "err", err)
	}
	// Auto-advance: find the next media item within the current view context.
	c := parseViewCtx(r)
	items, rerr := h.resolveViewItems(store, c)
	if rerr != nil {
		slog.Error("feed: complete resolve", "err", rerr)
	}
	_, next := neighbors(items, id, true)
	if next != nil {
		if renderPlayerFragment(w, r, h, store, next.ID, c) {
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// renderPlayerFragment renders the feedPlayer fragment for itemID, returning
// true on success. On any error it writes nothing and returns false, leaving
// the caller to send a fallback (empty body / 204).
func renderPlayerFragment(w http.ResponseWriter, r *http.Request, h *Pages, store *Store, itemID int64, c viewCtx) bool {
	ip, err := store.GetItemPlayback(itemID)
	if err != nil {
		slog.Error("feed: player fragment playback", "item_id", itemID, "err", err)
		return false
	}
	prefs, perr := store.LoadPlayerPrefs()
	if perr != nil {
		slog.Error("feed: player fragment prefs", "err", perr)
		prefs = PlayerPrefs{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1, AutoplayNext: true}
	}
	items, err := h.resolveViewItems(store, c)
	if err != nil {
		slog.Error("feed: resolve view items", "err", err)
	}
	nav := buildItemNav(store, c, items, itemID) // any-type neighbors for the bar
	ui.Render(w, r, feedPlayer(ip, prefs, ip.ThumbnailURL, plugin.CSRFToken(r), nav))
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
		httperr.Internal(w, r, err)
		return
	}
	c := parseViewCtx(r)
	items, err := h.resolveViewItems(store, c)
	if err != nil {
		slog.Error("feed: adjacent resolve", "err", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	prev, next := neighbors(items, id, true) // media-only for player nav
	target := next
	if dir == "prev" {
		target = prev
	}
	if target == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !renderPlayerFragment(w, r, h, store, target.ID, c) {
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
		httperr.Internal(w, r, err)
		return
	}
	it, err := store.GetItem(id)
	if err != nil {
		lookupError(w, r, err)
		return
	}
	streamURL, err := ResolveYouTubeStreamURL(r.Context(), it.MediaURL)
	if err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": streamURL})
}

// Ranges returns the unified mute (profanity) + skip (silence) ranges for an
// item, and whether the silence ranges are still to be computed.
func (h *Pages) Ranges(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	d, err := store.GetDownload(id)
	if errors.Is(err, sql.ErrNoRows) {
		// No download row yet — nothing to analyze.
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": []Range{}})
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	ranges := []Range{}
	u := plugin.UserFromContext(r.Context())
	space, err := h.plugin.Space(u.Username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	// Mute ranges from profanity captions (when captions + plugin present).
	if d.CaptionsPath != "" {
		if prof := h.plugin.Profanity(); prof != nil {
			if f, ferr := space.Own.Open(Name(d.CaptionsPath)); ferr == nil {
				captions := profanity.ParseVTT(f)
				if err := f.Close(); err != nil {
					slog.Error("feed: close captions", "id", id, "err", err)
				}
				for _, ar := range prof.AudioRanges(u.Username, captions) {
					ranges = append(ranges, Range{Start: ar.Start, End: ar.End, Action: "mute"})
				}
			} else {
				slog.Error("feed: open captions", "id", id, "err", ferr)
			}
		}
	}

	// Skip ranges from silence detection, which the pipeline runs once a
	// download is ready. Until it has, the ranges are pending: running
	// ffmpeg over a whole episode here held the request for as long as
	// the file took to decode.
	pending := false
	if d.LocalPath != "" {
		if d.SilenceState == "done" {
			skips, err := store.GetSilenceRanges(id)
			if err != nil {
				httperr.Internal(w, r, err)
				return
			}
			ranges = append(ranges, skips...)
		} else {
			pending = true
		}
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": ranges, "pending": pending})
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
		httperr.Internal(w, r, err)
		return
	}
	it, err := store.GetItem(id)
	if err != nil {
		lookupError(w, r, err)
		return
	}
	dl, err := store.FindDownload(id)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	for _, stored := range []string{dl.CleanAudioPath, dl.LocalPath} {
		if stored == "" {
			continue
		}
		space, err := h.plugin.Space(plugin.Username(r))
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		f, err := space.Own.Open(Name(stored))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), f)
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
		httperr.Internal(w, r, err)
		return
	}
	it, err := store.GetItem(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := store.MarkRead(id, !it.IsRead); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	it.IsRead = !it.IsRead
	// If HTMX triggered this request, return the updated row for outerHTML
	// swap, and the sidebar with its new unread counts.
	if r.Header.Get("HX-Request") == "true" {
		sub, err := store.GetSubscription(it.SubscriptionID)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		page := currentPath(r)
		o := rowOpts{HideSub: strings.HasPrefix(page, "/apps/feed/subscription/"), Now: h.plugin.ctx.Now()}
		iws := ItemWithSubscription{Item: it, SubscriptionTitle: subscriptionDisplayTitle(sub)}
		h.renderRowUpdate(w, r, store, feedItemRow(iws, o))
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
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
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
		tags, err := store.ItemTags(id)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		ui.Render(w, r, feedItemTagRow(id, tags, plugin.CSRFToken(r)))
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

// RemoveItemTag removes a tag from an item. POST /apps/feed/items/{id}/tag/{name}/delete.
func (h *Pages) RemoveItemTag(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if err := store.RemoveTag(id, name); err != nil {
		h.setFlash(w, "error", "Could not remove tag: "+err.Error())
		http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		tags, err := store.ItemTags(id)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		ui.Render(w, r, feedItemTagRow(id, tags, plugin.CSRFToken(r)))
		return
	}
	http.Redirect(w, r, r.Header.Get("Referer"), http.StatusSeeOther)
}

// FilterPreview returns a preview of items matching a filter query (HTMX partial).
func (h *Pages) FilterPreview(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	q := r.URL.Query().Get("query")
	if len(q) > 200 {
		q = q[:200]
	}
	if q == "" {
		// Empty query: render empty results without error.
		ui.Render(w, r, feedFilterPreviewResults(nil))
		return
	}
	expr, err := query.Parse(q)
	if err != nil {
		http.Error(w, "Parse error: "+err.Error(), http.StatusBadRequest)
		return
	}
	items, err := store.ListItemsForView(ViewSpec{All: true}, 200)
	if err != nil {
		httperr.Internal(w, r, fmt.Errorf("Could not load items: %w", err))
		return
	}
	// refineItems batch-loads each row's tags and download state rather than
	// querying per row.
	matched, err := h.refineItems(store, items, expr)
	if err != nil {
		httperr.Internal(w, r, fmt.Errorf("Could not match items: %w", err))
		return
	}
	if len(matched) > 10 {
		matched = matched[:10]
	}
	ui.Render(w, r, feedFilterPreviewResults(matched))
}

// SystemSettings renders the admin-only feed system settings page.
func (h *Pages) SystemSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	db := store.DB()
	model := string(SettingWhisperModel.MustGet(db))
	modelsDir := SettingWhisperModelsDir.MustGet(db)
	cliPath := SettingWhisperCLIPath.MustGet(db)
	plugin.SetTitle(w, "Feed System Settings")
	ui.Render(w, r, feedSystemSettingsPage(model, modelsDir, cliPath))
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
		httperr.Internal(w, r, err)
		return
	}

	if err := r.ParseMultipartForm(20 << 20); err != nil {
		h.setFlash(w, "error", "Upload too large.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	format := r.FormValue("format")
	file, _, err := r.FormFile("file")
	if err != nil {
		h.setFlash(w, "error", "No file uploaded.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 20<<20))
	if err != nil {
		h.setFlash(w, "error", "Failed to read file: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
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
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	if err != nil {
		h.setFlash(w, "error", "Import failed: "+err.Error())
	} else {
		u := plugin.UserFromContext(r.Context())
		go h.plugin.TriggerFetch(u.Username, 0)
		h.setFlash(w, "success", fmt.Sprintf("Imported %d subscriptions.", n))
	}
	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// ImportOPML handles POST of an OPML file to bulk-add RSS subscriptions.
func (h *Pages) ImportOPML(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	if err := r.ParseMultipartForm(2 << 20); err != nil {
		h.setFlash(w, "error", "Upload too large or malformed.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	file, _, err := r.FormFile("opml")
	if err != nil {
		h.setFlash(w, "error", "No OPML file uploaded.")
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, 2<<20))
	if err != nil {
		h.setFlash(w, "error", "Failed to read OPML: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}

	n, err := store.ImportOPML(data)
	if err != nil {
		h.setFlash(w, "error", "OPML parse error: "+err.Error())
		http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
		return
	}
	u := plugin.UserFromContext(r.Context())
	go h.plugin.TriggerFetch(u.Username, 0)
	h.setFlash(w, "success", fmt.Sprintf("Imported %d subscriptions.", n))
	http.Redirect(w, r, "/apps/feed/settings/subscriptions", http.StatusSeeOther)
}

// ExportOPML returns the user's RSS subscriptions as an OPML file download.
func (h *Pages) ExportOPML(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	data, err := store.ExportOPML()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", `attachment; filename="cog-feeds.opml"`)
	if _, err := w.Write(data); err != nil {
		slog.Warn("feed: write opml export", "err", err)
	}
}

// HistoryView shows items the user has played, ordered by last_played_at DESC.
func (h *Pages) HistoryView(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	items, err := store.ListPlaybackHistory(feedViewLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, "/apps/feed/history")
	if !ok {
		return
	}
	plugin.SetTitle(w, "Feed — History")
	ui.Render(w, r, feedHistoryPage(sb, items, h.plugin.ctx.Now()))
}

// SubscriptionSettings shows per-subscription options.
func (h *Pages) SubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	sub, err := store.GetSubscription(id)
	if err != nil {
		lookupError(w, r, err)
		return
	}
	folders, err := store.ListFolders()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sb, ok := h.sidebar(w, r, store, fmt.Sprintf("/apps/feed/subscription/%d", id))
	if !ok {
		return
	}
	plugin.SetTitle(w, "Feed — "+subscriptionDisplayTitle(sub))
	ui.Render(w, r, feedSubscriptionSettingsPage(sb, sub, folders, plugin.CSRFToken(r)))
}

// SaveSubscriptionSettings handles POST of the per-subscription settings form.
func (h *Pages) SaveSubscriptionSettings(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	it, err := store.GetItem(id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := store.MarkStarred(id, !it.IsStarred); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	// If HTMX triggered this, return the updated star button for outerHTML
	// swap, and the sidebar with its new starred count.
	if r.Header.Get("HX-Request") == "true" {
		h.renderRowUpdate(w, r, store, ui.Star(!it.IsStarred, fmt.Sprintf("/apps/feed/items/%d/star", it.ID)))
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
		httperr.Internal(w, r, err)
		return
	}
	cur, err := store.LoadPlayerPrefs()
	if err != nil {
		httperr.Internal(w, r, err)
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
	if v := r.FormValue("hide_empty"); v != "" {
		cur.HideEmpty = v == "true" || v == "1" || v == "on"
	}
	if err := store.SavePlayerPrefs(cur); err != nil {
		slog.Error("feed: save player prefs", "err", err)
		httperr.Internal(w, r, err)
		return
	}
	if r.Header.Get("HX-Target") == "feed-sidebar" {
		sb, ok := h.sidebar(w, r, store, sidebarPath(currentPath(r)))
		if !ok {
			return
		}
		ui.Render(w, r, feedSidebar(sb))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// renderRowUpdate answers a row action: the swapped element and, out of
// band, the sidebar with its new counts, highlighted for the page the
// request came from.
func (h *Pages) renderRowUpdate(w http.ResponseWriter, r *http.Request, store *Store, swapped templ.Component) {
	sb, ok := h.sidebar(w, r, store, sidebarPath(currentPath(r)))
	if !ok {
		return
	}
	sb.OOB = true
	ui.Render(w, r, feedRowUpdate(swapped, sb))
}

// sidebarPath is the sidebar entry to highlight for a page: the page itself,
// or for an item the list it was opened from.
func sidebarPath(page string) string {
	if page == "" || strings.HasPrefix(page, "/apps/feed/items/") {
		return "/apps/feed/"
	}
	return page
}

// buildItemNav works out an item's place in the list it was opened from.
func buildItemNav(store *Store, c viewCtx, items []ItemWithSubscription, id int64) itemNav {
	prev, next := neighbors(items, id, false)
	nav := itemNav{Ctx: c.encode(), Label: c.label(store), Path: c.path(), Total: len(items)}
	for i := range items {
		if items[i].ID == id {
			nav.Pos = i + 1
		}
	}
	if nav.Pos == 0 {
		nav.Total = 0
	}
	if prev != nil {
		nav.PrevID, nav.PrevTitle = prev.ID, prev.Title
	}
	if next != nil {
		nav.NextID, nav.NextTitle = next.ID, next.Title
	}
	return nav
}
