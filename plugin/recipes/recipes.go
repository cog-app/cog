// Package recipes implements a recipe book plugin with NextCloud Cookbook API compatibility.
package recipes

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
		Icon:  `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M3 2v7c0 1.1.9 2 2 2h4a2 2 0 0 0 2-2V2"></path><path d="M7 2v20"></path><path d="M21 15V2a5 5 0 0 0-5 5v6c0 1.1.9 2 2 2h3Zm0 0v7"></path></svg>`,
	})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Recipes",
		URL:      "/apps/recipes/",
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
//
// TODO(host): this is the one place the plugin knows a storage layout. The
// host has no per-plugin file store to hand out yet; when it does, images
// belong there.
func (p *Plugin) imagesDir(username string) string {
	return filepath.Join(p.ctx.DataDir(), username, "app_data", "recipes", "images")
}

// imagePath is where a recipe's stored image file lives. The name comes from
// the database, so only its base is used: it cannot point outside the
// directory.
func (p *Plugin) imagePath(username, name string) string {
	return filepath.Join(p.imagesDir(username), filepath.Base(name))
}

// removeImage deletes a recipe's image file. One that is already gone is not
// an error.
func (p *Plugin) removeImage(username, name string) error {
	if err := os.Remove(p.imagePath(username, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return fmt.Errorf("open recipes database: %w", err)
	}
	recipes, err := RecentRecipes(db, 5)
	if err != nil {
		return fmt.Errorf("recent recipes: %w", err)
	}
	return recentRecipesWidget(recipes).Render(ctx, w)
}

// isoDurationRe matches the ISO 8601 durations schema.org recipes use:
// PT1H30M, PT90M, P0DT1H, PT1H30M0S.
var isoDurationRe = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:\d+(?:\.\d+)?S)?)?$`)

// parseDuration returns an ISO 8601 duration in minutes. ok is false for
// anything else (an imported "about an hour").
func parseDuration(iso string) (minutes int, ok bool) {
	m := isoDurationRe.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(iso)))
	if m == nil || iso == "" {
		return 0, false
	}
	for i, mult := range []int{24 * 60, 60, 1} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return 0, false
		}
		minutes += n * mult
	}
	return minutes, true
}

// isoDuration is the ISO 8601 form of a number of minutes, "" for none.
func isoDuration(minutes int) string {
	if minutes <= 0 {
		return ""
	}
	h, m := minutes/60, minutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("PT%dM", m)
	case m == 0:
		return fmt.Sprintf("PT%dH", h)
	}
	return fmt.Sprintf("PT%dH%dM", h, m)
}

// formatDuration shows an ISO 8601 duration the way a cook reads it:
// "1 hr 30 min". A value that is not ISO 8601 is shown as it is.
func formatDuration(iso string) string {
	minutes, ok := parseDuration(iso)
	if !ok {
		return iso
	}
	h, m := minutes/60, minutes%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d hr", h)
	}
	return fmt.Sprintf("%d hr %d min", h, m)
}

// durationParts splits a stored duration into the hours and minutes the form
// shows. A value that is not ISO 8601 comes back as raw, so the form can keep
// it rather than drop it.
func durationParts(iso string) (hours, minutes, raw string) {
	total, ok := parseDuration(iso)
	if !ok {
		return "", "", iso
	}
	if total == 0 {
		return "", "", ""
	}
	if total >= 60 {
		hours = strconv.Itoa(total / 60)
	}
	if total%60 != 0 || total < 60 {
		minutes = strconv.Itoa(total % 60)
	}
	return hours, minutes, ""
}

// formDuration reads a duration from the form's <name>_h and <name>_m fields
// as ISO 8601. With both empty it keeps <name>_raw, a value the form could
// not show as numbers.
func formDuration(r *http.Request, name string) (string, error) {
	h, m := strings.TrimSpace(r.FormValue(name+"_h")), strings.TrimSpace(r.FormValue(name+"_m"))
	if h == "" && m == "" {
		return r.FormValue(name + "_raw"), nil
	}
	total := 0
	for _, part := range []struct {
		value string
		mult  int
	}{{h, 60}, {m, 1}} {
		if part.value == "" {
			continue
		}
		n, err := strconv.Atoi(part.value)
		if err != nil || n < 0 {
			return "", fmt.Errorf("%s time: %q is not a whole number", name, part.value)
		}
		total += n * part.mult
	}
	return isoDuration(total), nil
}
