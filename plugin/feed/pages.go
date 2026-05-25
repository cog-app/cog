package feed

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/query"
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

	kind := r.FormValue("kind")
	if !allowedKinds[kind] {
		h.setFlash(w, "error", "Invalid subscription kind.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
	}

	rawURL := r.FormValue("url")
	if rawURL == "" {
		h.setFlash(w, "error", "URL is required.")
		http.Redirect(w, r, "/apps/feed/settings", http.StatusSeeOther)
		return
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

// Index shows the main feed item list with optional query filtering.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	store, err := h.userStore(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	q := r.URL.Query().Get("q")

	items, err := store.ListItems(200)
	if err != nil {
		http.Error(w, "could not list items: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if q != "" {
		expr, perr := query.Parse(q)
		if perr == nil {
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
				rec := BuildRecord(it, sub, folder, tagsList, !it.IsRead, it.IsStarred, dl.State == "ready")
				if expr.Match(rec) {
					filtered = append(filtered, it)
				}
			}
			items = filtered
		}
	}

	folders, _ := store.ListFolders()
	tags, _ := store.ListTags()
	playlists, _ := store.ListPlaylists()

	plugin.SetTitle(w, "Feed")
	feedIndexPage(folders, tags, playlists, items, q).Render(r.Context(), w)
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
	it, err := store.GetItem(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := store.MarkRead(id, true); err != nil {
		slog.Error("feed: mark read", "id", id, "err", err)
	}
	tags, _ := store.ItemTags(id)
	plugin.SetTitle(w, it.Title)
	feedItemDetail(it, tags).Render(r.Context(), w)
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

// ToggleRead toggles the read state of an item then redirects back.
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
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/apps/feed/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
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

// ToggleStar toggles the starred state of an item then redirects back.
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
	ref := r.Header.Get("Referer")
	if ref == "" {
		ref = "/apps/feed/"
	}
	http.Redirect(w, r, ref, http.StatusSeeOther)
}
