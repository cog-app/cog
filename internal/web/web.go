// Package web provides the browser-facing HTTP handlers for Cog.
package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

// pageData builds a PageData from the request context.
func pageData(r *http.Request, title string, secret []byte) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, secret)
	}
	return ui.PageData{
		Title:     title,
		User:      u,
		CSRFToken: csrf,
		Nav:       defaultNav(r.URL.Path, u),
	}
}

func defaultNav(path string, u *user.User) []ui.NavItem {
	nav := []ui.NavItem{
		{Label: "Dashboard", URL: "/", Active: path == "/"},
		{Label: "Settings", URL: "/settings", Active: path == "/settings"},
	}
	if u != nil && u.IsAdmin {
		nav = append(nav, ui.NavItem{Label: "Admin", URL: "/admin/users", Active: len(path) >= 6 && path[:6] == "/admin"})
	}
	return nav
}
