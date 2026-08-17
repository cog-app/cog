// Package recipes implements a recipe book plugin with NextCloud Cookbook API compatibility.
package recipes

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "recipes"

// Plugin is the recipe book plugin.
type Plugin struct {
	ctx    plugin.PluginContext
	log    *slog.Logger
	client *http.Client
}

// New creates a new Recipes plugin instance.
func New() *Plugin {
	return &Plugin{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return fmt.Errorf("non-HTTP redirect")
				}
				return nil
			},
		},
	}
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
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{
		Label: "Recipes",
		URL:   "/apps/recipes",
		Icon:  `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z"></path></svg>`,
	})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Recipes",
		Priority: 35,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "recipes",
		EnrichSQL: `SELECT r.rowid, r.name,
			r.category || CASE WHEN k.tags != '' THEN ' · ' || k.tags ELSE '' END AS subtext,
			r.description
		FROM recipes r
		LEFT JOIN (
			SELECT recipe_id, GROUP_CONCAT(keyword, ', ') AS tags
			FROM keywords GROUP BY recipe_id
		) k ON k.recipe_id = r.id
		WHERE r.rowid IN (?)`,
		ContentFunc: func(filesDir string, cols map[string]string) string {
			// We can't query the DB from here, but cols has the enrichment columns.
			// The description is already in content. For ingredient search, we rely
			// on the enrichment SQL including description. To get ingredients and
			// instructions indexed, we use a richer EnrichSQL that includes them.
			return ""
		},
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "recipe",
		Table:    "recipes",
		IDColumn: "id",
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("category/{name}", http.HandlerFunc(pages.CategoryView))
	ctx.RegisterPage("keyword/{name}", http.HandlerFunc(pages.KeywordView))
	ctx.RegisterPage("recipe/{id}", http.HandlerFunc(pages.RecipeView))
	ctx.RegisterPage("recipe/{id}/edit", http.HandlerFunc(pages.RecipeEdit))
	ctx.RegisterPage("new", http.HandlerFunc(pages.NewRecipe))
	ctx.RegisterPage("import", http.HandlerFunc(pages.ImportPage))
	ctx.RegisterAction("POST", "recipes", http.HandlerFunc(pages.CreateRecipe))
	ctx.RegisterAction("POST", "recipes/{id}", http.HandlerFunc(pages.UpdateRecipe))
	ctx.RegisterAction("DELETE", "recipes/{id}", http.HandlerFunc(pages.DeleteRecipe))
	ctx.RegisterAction("POST", "recipes/import-url", http.HandlerFunc(pages.ImportFromURL))

	// NextCloud Cookbook API compatibility
	api := NewCookbookAPI(p)
	ctx.RegisterAPI("api/recipes", http.HandlerFunc(api.ListRecipes))
	ctx.RegisterAPI("api/recipes/{id}", http.HandlerFunc(api.GetRecipe))
	ctx.RegisterAPI("api/recipes/{id}/image", http.HandlerFunc(api.GetRecipeImage))
	ctx.RegisterAPI("api/categories", http.HandlerFunc(api.ListCategories))
	ctx.RegisterAPI("api/keywords", http.HandlerFunc(api.ListKeywords))
	ctx.RegisterAction("POST", "api/recipes", http.HandlerFunc(api.CreateRecipe))
	ctx.RegisterAction("PUT", "api/recipes/{id}", http.HandlerFunc(api.UpdateRecipe))
	ctx.RegisterAction("DELETE", "api/recipes/{id}", http.HandlerFunc(api.DeleteRecipe))
	ctx.RegisterAction("POST", "api/import", http.HandlerFunc(api.ImportFromURL))

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// imagesDir returns the path to the recipe images directory for a user.
func (p *Plugin) imagesDir(username string) string {
	return p.ctx.DataDir() + "/" + username + "/app_data/recipes/images"
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	recipes, _ := RecentRecipes(db, 5)
	return recentRecipesWidget(recipes).Render(ctx, w)
}

// formatDuration converts an ISO 8601 duration like "PT1H30M" to a human-readable string.
func formatDuration(iso string) string {
	iso = strings.TrimPrefix(iso, "PT")
	iso = strings.TrimPrefix(iso, "P")
	if iso == "" {
		return ""
	}
	var parts []string
	// Parse hours
	if i := strings.Index(iso, "H"); i >= 0 {
		parts = append(parts, iso[:i]+"h")
		iso = iso[i+1:]
	}
	// Parse minutes
	if i := strings.Index(iso, "M"); i >= 0 {
		parts = append(parts, iso[:i]+"m")
		iso = iso[i+1:]
	}
	if len(parts) == 0 {
		return iso
	}
	return strings.Join(parts, " ")
}
