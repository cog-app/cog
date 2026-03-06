package gallery

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// MemoriesAPI handles NextCloud Memories-compatible API endpoints.
type MemoriesAPI struct {
	plugin *Plugin
}

// NewMemoriesAPI creates a new MemoriesAPI handler.
func NewMemoriesAPI(p *Plugin) *MemoriesAPI {
	return &MemoriesAPI{plugin: p}
}

func (a *MemoriesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")

	switch {
	case len(parts) == 1 && parts[0] == "days":
		a.handleDays(w, r)
	case len(parts) == 2 && parts[0] == "days":
		a.handleDay(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "albums":
		a.handleAlbums(w, r)
	case len(parts) == 2 && parts[0] == "albums":
		a.handleAlbum(w, r, parts[1])
	case len(parts) == 1 && parts[0] == "tags":
		a.handleTags(w, r)
	case len(parts) == 1 && parts[0] == "faces":
		a.handleFaces(w, r)
	case len(parts) == 1 && parts[0] == "places":
		a.handlePlaces(w, r)
	case len(parts) == 3 && parts[0] == "image" && parts[1] == "info":
		a.handleImageInfo(w, r, parts[2])
	case len(parts) == 3 && parts[0] == "image" && parts[1] == "preview":
		a.handleImagePreview(w, r, parts[2])
	default:
		http.NotFound(w, r)
	}
}

func (a *MemoriesAPI) handleDays(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	days, err := TimelineDays(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type dayResp struct {
		DayID int64 `json:"dayid"`
		Count int   `json:"count"`
	}
	result := make([]dayResp, len(days))
	for i, d := range days {
		result[i] = dayResp{DayID: d.DayID, Count: d.Count}
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handleDay(w http.ResponseWriter, r *http.Request, dayIDStr string) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	dayID, err := strconv.ParseInt(dayIDStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	media, err := TimelineMedia(db, dayID, 500, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type photoResp struct {
		ID       int64  `json:"fileid"`
		Path     string `json:"filename"`
		MimeType string `json:"mimetype"`
		Width    int    `json:"w"`
		Height   int    `json:"h"`
		DayID    int64  `json:"dayid"`
	}
	result := make([]photoResp, len(media))
	for i, m := range media {
		ts := m.DateIndexed
		if m.DateTaken != nil {
			ts = *m.DateTaken
		}
		result[i] = photoResp{
			ID:       m.FileID,
			Path:     m.FilePath,
			MimeType: m.MimeType,
			Width:    m.Width,
			Height:   m.Height,
			DayID:    formatDayID(ts),
		}
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handleAlbums(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	albums, err := ListAlbums(db, -1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type albumResp struct {
		ID    int64  `json:"album_id"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	result := make([]albumResp, len(albums))
	for i, a := range albums {
		result[i] = albumResp{ID: a.ID, Name: a.Name, Count: a.MediaCount}
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handleAlbum(w http.ResponseWriter, r *http.Request, idStr string) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	media, err := AlbumMedia(db, id, 500, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, media)
}

func (a *MemoriesAPI) handleTags(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, err := ListTags(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type tagResp struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	result := make([]tagResp, len(tags))
	for i, t := range tags {
		result[i] = tagResp{Name: t.Name, Count: t.Count}
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handleFaces(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	people, err := ListPeople(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type faceResp struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	result := make([]faceResp, len(people))
	for i, p := range people {
		result[i] = faceResp{Name: p.DisplayName(), Count: p.FaceCount}
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handlePlaces(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rows, err := db.Query(`SELECT city, country, COUNT(*) AS cnt FROM media
		WHERE city != '' GROUP BY city, country ORDER BY cnt DESC`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type placeResp struct {
		Name    string `json:"name"`
		Country string `json:"country"`
		Count   int    `json:"count"`
	}
	var result []placeResp
	for rows.Next() {
		var p placeResp
		if err := rows.Scan(&p.Name, &p.Country, &p.Count); err != nil {
			continue
		}
		result = append(result, p)
	}
	writeJSON(w, result)
}

func (a *MemoriesAPI) handleImageInfo(w http.ResponseWriter, r *http.Request, idStr string) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, m)
}

func (a *MemoriesAPI) handleImagePreview(w http.ResponseWriter, r *http.Request, idStr string) {
	u := auth.UserFromContext(r.Context())
	db, err := a.plugin.DB(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	size := 256
	if s := r.URL.Query().Get("x"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			size = v
		}
	}

	f, err := a.plugin.PreviewPath(u.Username, m, size)
	if err != nil {
		http.Error(w, "preview unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "preview.jpg", time.Time{}, f)
}
