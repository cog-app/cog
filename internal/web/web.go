// Package web provides the browser-facing HTTP handlers for Cog.
package web

import (
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/ui"
)

type handler struct{ p *services.Providers }

// pageData builds a PageData from the request context.
func pageData(r *http.Request, title string, secret []byte) ui.PageData {
	return pageDataWithNav(r, title, secret, nil)
}

// pageDataWithNav builds a PageData with plugin nav items from the registry.
func pageDataWithNav(r *http.Request, title string, secret []byte, registry *plugin.Registry) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, secret)
	}
	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         buildNav(r.URL.Path, registry),
		RequestPath: r.URL.Path,
	}
}

func buildNav(path string, registry *plugin.Registry) []ui.NavItem {
	if registry == nil {
		return nil
	}
	pluginNav := registry.NavItems()
	nav := make([]ui.NavItem, 0, len(pluginNav))
	for _, item := range pluginNav {
		active := path == item.URL || (item.URL != "/" && strings.HasPrefix(path, item.URL))
		nav = append(nav, ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		})
	}
	return nav
}
