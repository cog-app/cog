package media

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the media plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type sidebarData struct {
	Sources    []Source
	Groups     []Group
	EpCounts   map[int64]int
	QueueCount int
	Tags       []string
	ActivePath string
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) sidebarData {
	sources, _ := ListSources(db)
	groups, _ := ListGroups(db)
	epCounts, _ := SourceEpisodeCounts(db)
	queueCount, _ := QueueCount(db)
	tags, _ := ListTags(db)
	return sidebarData{
		Sources:    sources,
		Groups:     groups,
		EpCounts:   epCounts,
		QueueCount: queueCount,
		Tags:       tags,
		ActivePath: activePath,
	}
}

// Queue is the landing page showing the playback queue.
func (h *Pages) Queue(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/media/")
	plugin.SetTitle(w, "Media Queue")
	queue, _ := ListQueue(db)
	mediaQueuePage(sb, queue, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Browse shows all sources grouped by group.
func (h *Pages) Browse(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/media/browse")
	plugin.SetTitle(w, "Browse Sources")
	episodes, _ := ListRecentEpisodes(db, 50)
	mediaBrowsePage(sb, episodes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Source shows episodes for a single source.
func (h *Pages) Source(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid source id", http.StatusBadRequest)
		return
	}
	src, err := GetSource(db, id)
	if err != nil {
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, "/apps/media/source/"+idStr)
	plugin.SetTitle(w, src.Title)
	episodes, _ := ListEpisodesBySource(db, id, 200)
	mediaSourcePage(sb, src, episodes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// EpisodeDetail shows a single episode with the player.
func (h *Pages) EpisodeDetail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid episode id", http.StatusBadRequest)
		return
	}
	ep, err := GetEpisode(db, id)
	if err != nil {
		http.Error(w, "episode not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, "")
	plugin.SetTitle(w, ep.Title)
	mediaEpisodePage(sb, ep, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// History shows recently played episodes.
func (h *Pages) History(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/media/history")
	plugin.SetTitle(w, "History")
	episodes, _ := RecentlyPlayed(db, 50)
	mediaHistoryPage(sb, episodes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Settings shows source and group management.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/media/settings")
	plugin.SetTitle(w, "Media Settings")
	sources, _ := ListSources(db)
	groups, _ := ListGroups(db)
	filters, _ := ListFilters(db)
	errMsg := r.URL.Query().Get("err")
	mediaSettingsPage(sb, sources, groups, filters, errMsg, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// TagView shows episodes filtered by tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tag := r.PathValue("name")
	sb := h.buildSidebar(db, "/apps/media/tag/"+tag)
	plugin.SetTitle(w, "Tag: "+tag)
	episodes, _ := ListEpisodesByTag(db, tag, 200)
	mediaTagPage(sb, tag, episodes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// SourceSettings shows the configuration page for a source.
func (h *Pages) SourceSettings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid source id", http.StatusBadRequest)
		return
	}
	src, err := GetSource(db, id)
	if err != nil {
		http.Error(w, "source not found", http.StatusNotFound)
		return
	}
	sb := h.buildSidebar(db, fmt.Sprintf("/apps/media/source/%d/settings", id))
	plugin.SetTitle(w, src.Title+" Settings")
	mediaSourceSettingsPage(sb, src, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// QueueByTag shows the queue filtered to a specific tag.
func (h *Pages) QueueByTag(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tag := r.PathValue("name")
	sb := h.buildSidebar(db, "/apps/media/queue/tag/"+tag)
	plugin.SetTitle(w, "Queue: "+tag)

	// Get full queue then filter to episodes with this tag.
	queue, _ := ListQueue(db)
	var filtered []EpisodeView
	for _, ep := range queue {
		tags, _ := EpisodeTags(db, ep.ID)
		for _, t := range tags {
			if t == tag {
				ep.Tags = tags
				filtered = append(filtered, ep)
				break
			}
		}
	}
	mediaQueueByTagPage(sb, tag, filtered, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// --- Actions ---

// AddSource handles POST to create a new source subscription.
func (h *Pages) AddSource(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawURL := r.FormValue("url")
	if rawURL == "" {
		http.Redirect(w, r, "/apps/media/settings", http.StatusSeeOther)
		return
	}

	kind, resolvedURL, err := DetectSourceKind(h.plugin.client, rawURL)
	if err != nil {
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	var groupID sql.NullInt64
	if gid := r.FormValue("group_id"); gid != "" {
		if id, err := strconv.ParseInt(gid, 10, 64); err == nil {
			groupID = sql.NullInt64{Int64: id, Valid: true}
		}
	}

	src, err := CreateSource(db, kind, resolvedURL, groupID)
	if err != nil {
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape("Source already exists or error: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Fetch immediately in background.
	u := plugin.UserFromContext(r.Context())
	go h.plugin.FetchSourceNow(u.Username, src.ID)

	http.Redirect(w, r, "/apps/media/settings", http.StatusSeeOther)
}

// DeleteSource handles DELETE to remove a source.
func (h *Pages) DeleteSource(w http.ResponseWriter, r *http.Request) {
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
	if err := DeleteSource(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// RefreshSource handles POST to force an immediate source refresh.
func (h *Pages) RefreshSource(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	u := plugin.UserFromContext(r.Context())
	go h.plugin.FetchSourceNow(u.Username, id)
	w.WriteHeader(http.StatusNoContent)
}

// AddGroup handles POST to create a new group.
func (h *Pages) AddGroup(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.FormValue("name")
	if name != "" {
		if _, err := CreateGroup(db, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/apps/media/settings", http.StatusSeeOther)
}

// DeleteGroup handles DELETE to remove a group.
func (h *Pages) DeleteGroup(w http.ResponseWriter, r *http.Request) {
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
	if err := DeleteGroup(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// QueueAdd handles POST to add an episode to the queue.
func (h *Pages) QueueAdd(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("episodeID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := AddToQueue(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return updated episode row for HTMX swap.
	ep, err := GetEpisode(db, id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	mediaEpisodeRow(ep, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// QueueRemove handles DELETE to remove an episode from the queue.
func (h *Pages) QueueRemove(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("episodeID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := RemoveFromQueue(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// QueueMove handles POST to reorder an episode in the queue.
func (h *Pages) QueueMove(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("episodeID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	pos, err := strconv.Atoi(r.FormValue("position"))
	if err != nil {
		http.Error(w, "invalid position", http.StatusBadRequest)
		return
	}
	if err := MoveInQueue(db, id, pos); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UpdateProgress handles POST to save playback position.
func (h *Pages) UpdateProgress(w http.ResponseWriter, r *http.Request) {
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
	progressSec, err := strconv.ParseFloat(r.FormValue("progress"), 64)
	if err != nil {
		http.Error(w, "invalid progress", http.StatusBadRequest)
		return
	}
	now := h.plugin.ctx.Now().Unix()
	SaveProgress(db, id, progressSec, now)
	w.WriteHeader(http.StatusNoContent)
}

// MarkComplete handles POST to mark an episode as completed.
func (h *Pages) MarkComplete(w http.ResponseWriter, r *http.Request) {
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
	now := h.plugin.ctx.Now().Unix()
	MarkCompleted(db, id, now)

	// Return the next queue item for auto-advance.
	queue, _ := ListQueue(db)
	if len(queue) > 0 {
		mediaPlayerComponent(&queue[0], plugin.CSRFToken(r)).Render(r.Context(), w)
	} else {
		mediaPlayerEmpty().Render(r.Context(), w)
	}
}

// AddTag handles POST to add a tag to an episode.
func (h *Pages) AddTag(w http.ResponseWriter, r *http.Request) {
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
	tag := r.FormValue("tag")
	if err := TagEpisode(db, id, tag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, _ := EpisodeTags(db, id)
	mediaTagList(id, tags, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// RemoveTag handles DELETE to remove a tag from an episode.
func (h *Pages) RemoveTag(w http.ResponseWriter, r *http.Request) {
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
	tag := r.PathValue("name")
	if err := UntagEpisode(db, id, tag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, _ := EpisodeTags(db, id)
	mediaTagList(id, tags, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// StreamURL returns the direct stream URL for an episode.
func (h *Pages) StreamURL(w http.ResponseWriter, r *http.Request) {
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
	ep, err := GetEpisode(db, id)
	if err != nil {
		http.Error(w, "episode not found", http.StatusNotFound)
		return
	}

	// For YouTube, resolve the stream URL via yt-dlp.
	src, err := GetSource(db, ep.SourceID)
	if err != nil {
		plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": ep.MediaURL})
		return
	}

	if src.Kind == SourceYouTube {
		streamURL, err := ytdlpStreamURL(r.Context(), ep.MediaURL)
		if err != nil {
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": streamURL})
		return
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": ep.MediaURL})
}

// AddFilter handles POST to create a new filter for a source.
func (h *Pages) AddFilter(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	queryStr := r.FormValue("query")
	if queryStr == "" {
		queryStr = "*"
	}
	action := r.FormValue("action")
	actionValue := r.FormValue("action_value")

	_, err = CreateFilter(db, queryStr, action, actionValue)
	if err != nil {
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/apps/media/settings", http.StatusSeeOther)
}

// DeleteFilterAction handles DELETE to remove a filter.
func (h *Pages) DeleteFilterAction(w http.ResponseWriter, r *http.Request) {
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
	if err := DeleteFilter(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
