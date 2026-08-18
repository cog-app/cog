// Package dashboard implements the dashboard plugin which provides the root page.
package dashboard

import (
	"context"
	"database/sql"
	"embed"
	"io"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

//go:embed sql
var sqlFS embed.FS

const PluginID = "dashboard"

// Plugin is the dashboard plugin. Embedding Protected makes the
// registry auto-detect it as always-on.
type Plugin struct {
	plugin.Protected
	ctx plugin.PluginContext
}

// New creates a new dashboard plugin.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

// DB returns the user's database for this plugin.
//
// The host owns the database: where it lives, how it is opened, and applying
// the schema registered in Register. This is a name for it, not a place that
// does any of that work.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	return p.ctx.PluginDB(username)
}

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	ctx.RegisterSchema(sqlFS)
	ctx.RegisterNavItem(plugin.NavItem{Label: "Dashboard", URL: "/", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M3 9l9-7 9 7v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"></path><polyline points="9 22 9 12 15 12 15 22"></polyline></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Your Groups",
		Priority: 50,
		Render:   p.renderWidget,
	})

	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		csrf := auth.CSRF{Secret: p.ctx.CSRFSecret(), Cookies: p.ctx.Cookies(), Log: p.ctx.Logger()}
		csrfWrap := func(h http.Handler) http.Handler {
			return auth.RequireAuthWeb(csrf.Require(h))
		}

		// Dashboard (root route from dashboard plugin)
		priv.RegisterRoute("GET /{$}", auth.RequireAuthWeb(p.Handler()))
		priv.RegisterRoute("GET /dashboard/edit", auth.RequireAuthWeb(p.EditHandler()))
		// Dashboard layout save (needs csrfWeb)
		priv.RegisterRoute("POST /dashboard/layout", csrfWrap(p.SaveLayoutHandler()))
	}
	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, username string) error {
	userLookup := p.ctx.UserLookup()
	groupStore := p.ctx.GroupStore()
	if userLookup == nil || groupStore == nil {
		return nil
	}
	u, err := userLookup.GetByUsername(username)
	if err != nil {
		return nil
	}
	var groups []plugin.GroupInfo
	for g, err := range groupStore.ListGroupsForUser(u.ID) {
		if err != nil {
			break
		}
		groups = append(groups, g)
		if len(groups) >= 5 {
			break
		}
	}
	return groupsWidget(groups).Render(ctx, w)
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// pageData builds the common PageData for dashboard pages.
func (p *Plugin) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, p.ctx.CSRFSecret())
	}

	var nav []ui.NavItem
	if p.ctx != nil {
		for _, item := range p.ctx.NavItems() {
			active := r.URL.Path == item.URL
			nav = append(nav, ui.NavItem{
				Label:  item.Label,
				URL:    item.URL,
				Active: active,
				Icon:   item.Icon,
			})
		}
	}

	var favicon string
	for _, item := range nav {
		if item.Active {
			favicon = ui.IconToFavicon(item.Icon)
			break
		}
	}

	return ui.PageData{
		Title:     title,
		User:      u,
		CSRFToken: csrf,
		Nav:       nav,
		PluginID:  "dashboard",
		Favicon:   favicon,
	}
}

// Handler returns an http.Handler for the dashboard page.
func (p *Plugin) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		pd := p.pageData(r, "Dashboard")

		// Collect app links from nav items (skip dashboard itself).
		var apps []AppLink
		if p.ctx != nil {
			for _, item := range p.ctx.NavItems() {
				if item.URL == "/" {
					continue
				}
				apps = append(apps, AppLink{Label: item.Label, URL: item.URL, Icon: item.Icon})
			}
		}

		// Collect widgets.
		var widgets []plugin.Widget
		if p.ctx != nil {
			widgets = p.ctx.Widgets()
		}

		// Apply user layout preferences.
		var layout *Layout
		db, err := p.DB(u.Username)
		if err == nil {
			layout, _ = GetLayout(db)
		}
		if layout == nil {
			layout = &Layout{Columns: 2}
		}

		if len(layout.WidgetOrder) > 0 {
			widgetMap := make(map[string]plugin.Widget, len(widgets))
			for _, w := range widgets {
				widgetMap[w.Title] = w
			}
			ordered := make([]plugin.Widget, 0, len(widgets))
			for _, title := range layout.WidgetOrder {
				if w, ok := widgetMap[title]; ok {
					ordered = append(ordered, w)
					delete(widgetMap, title)
				}
			}
			// Widgets not in the saved order are excluded (user unchecked them).
			widgets = ordered
		}

		dashboardPage(pd, apps, widgets, u.Username, layout.Columns).Render(r.Context(), w)
	})
}

// EditHandler returns an http.Handler for the dashboard customization page.
func (p *Plugin) EditHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		pd := p.pageData(r, "Customize Dashboard")

		var allWidgets []plugin.Widget
		if p.ctx != nil {
			allWidgets = p.ctx.Widgets()
		}

		var layout *Layout
		db, err := p.DB(u.Username)
		if err == nil {
			layout, _ = GetLayout(db)
		}
		if layout == nil {
			layout = &Layout{Columns: 2}
		}

		// Build enabled set from saved order (if any).
		enabledSet := make(map[string]bool)
		if len(layout.WidgetOrder) > 0 {
			for _, title := range layout.WidgetOrder {
				enabledSet[title] = true
			}
		} else {
			// Default: all enabled.
			for _, w := range allWidgets {
				enabledSet[w.Title] = true
			}
		}

		// Build ordered list: saved order first, then any new widgets.
		var orderedWidgets []plugin.Widget
		seen := make(map[string]bool)
		if len(layout.WidgetOrder) > 0 {
			widgetMap := make(map[string]plugin.Widget, len(allWidgets))
			for _, w := range allWidgets {
				widgetMap[w.Title] = w
			}
			for _, title := range layout.WidgetOrder {
				if w, ok := widgetMap[title]; ok {
					orderedWidgets = append(orderedWidgets, w)
					seen[title] = true
				}
			}
		}
		// Append any widgets not yet in saved order.
		for _, w := range allWidgets {
			if !seen[w.Title] {
				orderedWidgets = append(orderedWidgets, w)
			}
		}

		dashboardEditPage(pd, orderedWidgets, layout, enabledSet).Render(r.Context(), w)
	})
}

// SaveLayoutHandler returns an http.Handler that persists dashboard layout preferences.
func (p *Plugin) SaveLayoutHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		db, err := p.DB(u.Username)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		cols, _ := strconv.Atoi(r.FormValue("columns"))
		if cols < 1 || cols > 3 {
			cols = 2
		}

		widgetOrder := r.Form["widget_order"]

		if err := SaveLayout(db, &Layout{Columns: cols, WidgetOrder: widgetOrder}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
}

// AppLink is a navigation link displayed as a card on the dashboard.
type AppLink struct {
	Label string
	URL   string
	Icon  string
}
