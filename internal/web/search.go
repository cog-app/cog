package web

import (
	"encoding/json"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/ui"
)

type searchHandler struct{ handler }

// Search handles GET /api/search?q=...&limit=...&offset=...
// Searches the current user's per-user index.
func (h searchHandler) Search(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if h.p.Search == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"total": 0, "hits": []any{}})
		return
	}

	result, err := h.p.Search.SearchUser(u.Username, q, limit, offset)
	if err != nil {
		h.p.Log.Error("search", "user", u.Username, "err", err)
		httperr.Internal(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// SearchSystem handles GET /api/admin/search?q=...&limit=...&offset=...
// Searches the system index (admin only).
func (h searchHandler) SearchSystem(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if h.p.Search == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"total": 0, "hits": []any{}})
		return
	}

	result, err := h.p.Search.SearchSystem(q, limit, offset)
	if err != nil {
		h.p.Log.Error("system search", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// SearchPartial handles GET /api/search/partial?q=...
// Returns an HTML partial for the navbar search dropdown.
func (h searchHandler) SearchPartial(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	q := r.URL.Query().Get("q")
	if q == "" {
		// Return empty div to clear results.
		w.Header().Set("Content-Type", "text/html")
		return
	}

	pages := ui.MatchPages(q, u.IsAdmin, buildNav(r.URL.Path, h.p.Plugins))

	var userHits []search.Hit
	var systemHits []search.Hit

	if h.p.Search != nil {
		result, err := h.p.Search.SearchUser(u.Username, q, 10, 0)
		if err != nil {
			h.p.Log.Error("search partial: user search", "user", u.Username, "err", err)
		} else if result != nil {
			userHits = result.Hits
		}
		if u.IsAdmin {
			result, err := h.p.Search.SearchSystem(q, 10, 0)
			if err != nil {
				h.p.Log.Error("search partial: system search", "err", err)
			} else if result != nil {
				systemHits = result.Hits
			}
		}
	}

	w.Header().Set("Content-Type", "text/html")
	if err := ui.SearchResults(q, pages, userHits, systemHits, u.IsAdmin).Render(r.Context(), w); err != nil {
		h.p.Log.Error("search partial: render", "err", err)
	}
}
