package media

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/query"
	cgplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
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

// filterEpisodes applies content filters to episode text in-place.
func (h *Pages) filterEpisodes(username string, episodes []EpisodeView) {
	for i := range episodes {
		episodes[i].Title = h.plugin.filterText(username, episodes[i].Title)
		episodes[i].Description = h.plugin.filterHTML(username, episodes[i].Description)
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
	h.filterEpisodes(plugin.Username(r), queue)
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
	h.filterEpisodes(plugin.Username(r), episodes)
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
	h.filterEpisodes(plugin.Username(r), episodes)
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
	h.filterEpisodes(plugin.Username(r), episodes)
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
	imported := r.URL.Query().Get("imported")
	mediaSettingsPage(sb, sources, groups, filters, errMsg, imported, plugin.CSRFToken(r)).Render(r.Context(), w)
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
	h.filterEpisodes(plugin.Username(r), episodes)
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
		if slices.Contains(tags, tag) {
			ep.Tags = tags
			filtered = append(filtered, ep)
		}
	}
	mediaQueueByTagPage(sb, tag, filtered, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Import handles POST to import sources from various formats.
func (h *Pages) Import(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape("File too large"), http.StatusSeeOther)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape("No file uploaded"), http.StatusSeeOther)
		return
	}
	defer file.Close()

	format := r.FormValue("format")
	var groupID sql.NullInt64
	if gid := r.FormValue("group_id"); gid != "" {
		if id, err := strconv.ParseInt(gid, 10, 64); err == nil {
			groupID = sql.NullInt64{Int64: id, Valid: true}
		}
	}

	now := h.plugin.ctx.Now().Unix()
	var result ImportResult

	switch format {
	case "opml":
		data, err := io.ReadAll(io.LimitReader(file, 2<<20))
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		result, err = ImportOPML(db, data, groupID)
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}

	case "antennapod":
		tmpFile, err := os.CreateTemp("", "antennapod-*.db")
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		io.Copy(tmpFile, file)
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())
		result, err = ImportAntennaPod(db, tmpFile.Name(), groupID, now)
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}

	case "youtube-csv":
		data, err := io.ReadAll(io.LimitReader(file, 2<<20))
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		result, err = ImportYouTubeTakeout(db, data, groupID)
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}

	case "newpipe":
		tmpFile, err := os.CreateTemp("", "newpipe-*.zip")
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		io.Copy(tmpFile, file)
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())
		result, err = ImportNewPipe(db, tmpFile.Name(), groupID, now)
		if err != nil {
			http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}

	default:
		http.Redirect(w, r, "/apps/media/settings?err="+url.QueryEscape("Unknown format"), http.StatusSeeOther)
		return
	}

	// Fetch all new unfetched sources in the background.
	u := plugin.UserFromContext(r.Context())
	sources, _ := ListSources(db)
	go func() {
		for _, src := range sources {
			if src.LastCheckedAt == 0 {
				h.plugin.FetchSourceNow(u.Username, src.ID)
			}
		}
	}()

	http.Redirect(w, r, "/apps/media/settings?imported="+url.QueryEscape(result.String()), http.StatusSeeOther)
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

	// Serve clean audio file URL if available.
	if ep.CleanAudioPath != "" {
		if _, err := os.Stat(ep.CleanAudioPath); err == nil {
			cleanURL := fmt.Sprintf("/apps/media/api/clean-file/%d", ep.ID)
			plugin.WriteJSON(w, http.StatusOK, map[string]string{"url": cleanURL, "clean": "true"})
			return
		}
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

// ServeCleanFile serves the profanity-cleaned audio/video file for an episode.
func (h *Pages) ServeCleanFile(w http.ResponseWriter, r *http.Request) {
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
	if ep.CleanAudioPath == "" {
		http.Error(w, "no clean audio available", http.StatusNotFound)
		return
	}
	http.ServeFile(w, r, ep.CleanAudioPath)
}

// CleanAudio returns mute/replacement ranges for profanity in an episode's captions.
func (h *Pages) CleanAudio(w http.ResponseWriter, r *http.Request) {
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

	if ep.CaptionsPath == "" {
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": []any{}})
		return
	}

	// Parse VTT captions.
	f, err := os.Open(ep.CaptionsPath)
	if err != nil {
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": []any{}})
		return
	}
	defer f.Close()
	captions := profanity.ParseVTT(f)

	// Apply all content filters to get audio ranges.
	username := plugin.Username(r)
	var allRanges []cgplugin.AudioRange
	for _, cf := range h.plugin.contentFilters {
		ranges := cf.AudioRanges(username, captions)
		allRanges = append(allRanges, ranges...)
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{"ranges": allRanges})
}

// FilterPreview returns an HTMX partial showing episodes matching a filter query.
func (h *Pages) FilterPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("query")
	if q == "" {
		w.Header().Set("Content-Type", "text/html")
		return
	}

	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if q == "*" {
		mediaFilterPreview(nil, -1, 0, "").Render(r.Context(), w)
		return
	}

	expr, err := query.Parse(q)
	if err != nil {
		mediaFilterPreview(nil, 0, 0, err.Error()).Render(r.Context(), w)
		return
	}

	episodes, _ := ListRecentEpisodes(db, 200)
	var matches []EpisodeView
	for _, ep := range episodes {
		rec := query.Record{
			"title":       ep.Title,
			"description": ep.Description,
			"type":        string(ep.MediaType),
			"duration":    strconv.Itoa(ep.DurationSec),
			"url":         ep.MediaURL,
			"source":      ep.SourceTitle,
		}
		if expr.Match(rec) {
			matches = append(matches, ep)
		}
	}
	preview := matches
	if len(preview) > 5 {
		preview = preview[:5]
	}
	mediaFilterPreview(preview, len(matches), len(episodes), "").Render(r.Context(), w)
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

// AdminCacheSettings shows the admin media cache configuration page.
func (h *Pages) AdminCacheSettings(w http.ResponseWriter, r *http.Request) {
	currentVolume := h.plugin.CacheVolumeName()

	var volumeNames []string
	if va := h.plugin.ctx.VolumeAccess(); va != nil {
		if vols, err := va.ListAccessible(""); err == nil {
			for _, v := range vols {
				volumeNames = append(volumeNames, v.Name)
			}
		}
	}
	if len(volumeNames) == 0 {
		volumeNames = []string{"default"}
	}

	scan := h.plugin.GetScanStatus()
	flash := r.URL.Query().Get("flash")
	mediaAdminCachePage(currentVolume, volumeNames, scan, flash, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// SaveAdminCacheSettings handles POST to save cache volume.
func (h *Pages) SaveAdminCacheSettings(w http.ResponseWriter, r *http.Request) {
	volume := r.FormValue("cache_volume")
	if volume == "" {
		volume = "default"
	}
	h.plugin.setSetting("cache_volume", volume)
	http.Redirect(w, r, "/admin/plugins/media?flash=Saved", http.StatusSeeOther)
}

// ScanStatusPartial returns the full scan status card (admin HTMX partial).
func (h *Pages) ScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	scan := h.plugin.GetScanStatus()
	mediaScanStatusCard(scan, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// UserScanStatusPartial returns the sidebar scan banner (HTMX partial).
func (h *Pages) UserScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	scan := h.plugin.GetScanStatus()
	mediaScanBanner(scan).Render(r.Context(), w)
}

// TriggerScan handles POST to trigger a scan for the current user.
func (h *Pages) TriggerScan(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	h.plugin.RequestScan(username)
	http.Redirect(w, r, "/apps/media/settings", http.StatusSeeOther)
}

// TriggerSystemScan handles admin POST to scan all users.
func (h *Pages) TriggerSystemScan(w http.ResponseWriter, r *http.Request) {
	h.plugin.RequestScan("*")
	http.Redirect(w, r, "/admin/plugins/media", http.StatusSeeOther)
}

// PurgeCache handles admin POST to delete all cached/clean files.
func (h *Pages) PurgeCache(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(h.plugin.ctx.DataDir())
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		username := entry.Name()
		cleanDir := filepath.Join(h.plugin.ctx.DataDir(), username, "app_data", "media", "clean")
		os.RemoveAll(cleanDir)
		captionsDir := filepath.Join(h.plugin.ctx.DataDir(), username, "app_data", "media", "captions")
		os.RemoveAll(captionsDir)

		if db, err := h.plugin.DB(username); err == nil {
			db.Exec(`UPDATE episodes SET clean_audio_path = '', captions_path = ''`)
		}
	}
	http.Redirect(w, r, "/admin/plugins/media?flash=Cache+purged", http.StatusSeeOther)
}
