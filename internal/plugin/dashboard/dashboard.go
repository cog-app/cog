// Package dashboard implements the dashboard plugin which provides the root page.
package dashboard

import (
	"context"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

const PluginID = "dashboard"

// WidgetProvider gives the dashboard access to registered widgets and nav items.
type WidgetProvider interface {
	Widgets() []plugin.Widget
	NavItems() []plugin.NavItem
	IsEnabled(string) bool
}

// Plugin is the dashboard plugin. Embedding Protected makes the
// registry auto-detect it as always-on.
type Plugin struct {
	plugin.Protected
	Registry WidgetProvider
	Secret   []byte
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar plugin.Registrar) error {
	registrar.RegisterNavItem(plugin.NavItem{Label: "Dashboard", URL: "/"})
	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// Handler returns an http.Handler for the dashboard page.
func (p *Plugin) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		sessID := auth.SessionIDFromContext(r.Context())
		var csrf string
		if sessID != "" {
			csrf = auth.CSRFToken(sessID, p.Secret)
		}

		// Build nav from registry.
		var nav []ui.NavItem
		if p.Registry != nil {
			for _, item := range p.Registry.NavItems() {
				active := r.URL.Path == item.URL
				nav = append(nav, ui.NavItem{
					Label:  item.Label,
					URL:    item.URL,
					Active: active,
					Icon:   item.Icon,
				})
			}
		}

		pd := ui.PageData{
			Title:     "Dashboard",
			User:      u,
			CSRFToken: csrf,
			Nav:       nav,
		}

		// Collect app links from nav items (skip dashboard itself).
		var apps []AppLink
		if p.Registry != nil {
			for _, item := range p.Registry.NavItems() {
				if item.URL == "/" {
					continue
				}
				apps = append(apps, AppLink{Label: item.Label, URL: item.URL, Icon: item.Icon})
			}
		}

		// Collect widgets.
		var widgets []plugin.Widget
		if p.Registry != nil {
			widgets = p.Registry.Widgets()
		}

		dashboardPage(pd, apps, widgets, u.ID).Render(r.Context(), w)
	})
}

// AppLink is a navigation link displayed as a card on the dashboard.
type AppLink struct {
	Label string
	URL   string
	Icon  string
}
