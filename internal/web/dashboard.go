package web

import "net/http"

type DashboardHandler struct {
	Secret []byte
}

func (h *DashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Dashboard", h.Secret)
	dashboardPage(pd).Render(r.Context(), w)
}
