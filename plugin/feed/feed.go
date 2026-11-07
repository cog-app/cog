// Package feed implements a unified feed reader plugin.
package feed

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os/exec"
	"time"

	"git.duckfam.us/jj-software/cog/internal/assets"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/ncapi"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

//go:embed static
var staticFS embed.FS

const PluginID = "feed"

// pkgAssets serves the plugin's embedded static assets with content-hash cache
// busting. Set in Register via the registrar's RegisterAssets helper.
var pkgAssets *assets.Assets

// playerAssetURL returns the versioned URL for player.js (used by the templ).
// Falls back to the unversioned path if assets weren't registered (e.g. a
// non-privileged context).
func playerAssetURL() string {
	if pkgAssets != nil {
		return pkgAssets.URL("player.js")
	}
	return "/apps/feed/static/player.js"
}

// OrchestratorRunner is a function that drives background workers until ctx is
// canceled. It is swappable so the orchestrator sub-package can be injected
// without an import cycle (the orchestrator imports feed, not vice-versa).
type OrchestratorRunner func(ctx context.Context, plug *Plugin, profPlugin *profanity.Plugin, tick time.Duration, log *slog.Logger)

// PokeRequest asks the orchestrator to do an out-of-cycle fetch.
// If SubID is 0, all subscriptions for Username are checked.
type PokeRequest struct {
	Username string
	SubID    int64
}

// Plugin is the feed reader plugin.
type Plugin struct {
	plugin.Protected
	ctx          plugin.PluginContext
	log          *slog.Logger
	cancel       context.CancelFunc
	done         chan struct{}
	tickInterval time.Duration
	profanity    *profanity.Plugin // discovered in Start; may be nil
	runOrchestra OrchestratorRunner
	pokes        chan PokeRequest
}

// New creates a new feed plugin instance.
func New() *Plugin {
	return &Plugin{tickInterval: 60 * time.Second, pokes: make(chan PokeRequest, 32)}
}

// Pokes returns the receive end of the channel for the orchestrator.
func (p *Plugin) Pokes() <-chan PokeRequest { return p.pokes }

// WithTickInterval overrides the background-poll interval. Returns the receiver
// for convenient chaining. Ignored if d <= 0.
func (p *Plugin) WithTickInterval(d time.Duration) *Plugin {
	if d > 0 {
		p.tickInterval = d
	}
	return p
}

// WithOrchestrator injects the background runner (called by the orchestrator
// sub-package to avoid an import cycle). Must be set before Start is called.
func (p *Plugin) WithOrchestrator(fn OrchestratorRunner) *Plugin {
	p.runOrchestra = fn
	return p
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
		Label: "Feed",
		URL:   "/apps/feed/",
		Icon:  `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M4 11a9 9 0 0 1 9 9"></path><path d="M4 4a16 16 0 0 1 16 16"></path><circle cx="5" cy="19" r="1"></circle></svg>`,
	})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Feed",
		URL:      "/apps/feed/",
		Priority: 25,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "items",
		Schema:       searchSchema,
		Enricher:     feedEnricher{},
	})

	// Main app pages and item actions
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("items/{id}", http.HandlerFunc(pages.ItemDetail))
	ctx.RegisterPage("items/{id}/media", http.HandlerFunc(pages.ItemMedia))
	ctx.RegisterAction("POST", "items/{id}/read", http.HandlerFunc(pages.ToggleRead))
	ctx.RegisterAction("POST", "items/{id}/star", http.HandlerFunc(pages.ToggleStar))
	ctx.RegisterAction("POST", "items/{id}/mark-read", http.HandlerFunc(pages.MarkItemRead))
	ctx.RegisterAction("POST", "items/{id}/progress", http.HandlerFunc(pages.UpdateProgress))
	ctx.RegisterAction("POST", "items/{id}/complete", http.HandlerFunc(pages.MarkComplete))
	ctx.RegisterAction("POST", "items/{id}/download", http.HandlerFunc(pages.DownloadItem))
	ctx.RegisterAction("POST", "items/{id}/tag", http.HandlerFunc(pages.AddItemTag))
	ctx.RegisterAction("POST", "items/{id}/tag/{name}/delete", http.HandlerFunc(pages.RemoveItemTag))
	ctx.RegisterAPI("filter-preview", http.HandlerFunc(pages.FilterPreview))
	ctx.RegisterAPI("api/stream-url/{id}", http.HandlerFunc(pages.StreamURL))
	ctx.RegisterAPI("items/{id}/adjacent", http.HandlerFunc(pages.AdjacentPlayer))
	ctx.RegisterAPI("api/ranges/{id}", http.HandlerFunc(pages.Ranges))

	// View pages
	ctx.RegisterPage("subscription/{id}", http.HandlerFunc(pages.SubscriptionView))
	ctx.RegisterPage("subscription/{id}/settings", http.HandlerFunc(pages.SubscriptionSettings))
	ctx.RegisterPage("folder/{id}", http.HandlerFunc(pages.FolderView))
	ctx.RegisterPage("starred", http.HandlerFunc(pages.StarredView))
	ctx.RegisterPage("history", http.HandlerFunc(pages.HistoryView))
	ctx.RegisterPage("tag/{name}", http.HandlerFunc(pages.TagView))
	ctx.RegisterPage("playlists/{id}", http.HandlerFunc(pages.PlaylistView))
	// Bulk-mark and refresh actions
	ctx.RegisterAction("POST", "items/read-all", http.HandlerFunc(pages.MarkAllRead))
	ctx.RegisterAction("POST", "subscriptions/{id}/refresh", http.HandlerFunc(pages.RefreshSubscription))
	ctx.RegisterAction("POST", "subscriptions/{id}/read-all", http.HandlerFunc(pages.MarkSubscriptionRead))
	ctx.RegisterAction("POST", "folders/{id}/read-all", http.HandlerFunc(pages.MarkFolderRead))
	ctx.RegisterAction("POST", "subscription/{id}/settings", http.HandlerFunc(pages.SaveSubscriptionSettings))
	ctx.RegisterAction("POST", "player/prefs", http.HandlerFunc(pages.SavePlayerPrefs))

	// Settings pages and resource actions
	ctx.RegisterPage("settings", http.HandlerFunc(pages.SettingsRedirect))
	ctx.RegisterPage("settings/subscriptions", http.HandlerFunc(pages.SettingsSubscriptions))
	ctx.RegisterPage("settings/filters", http.HandlerFunc(pages.SettingsFilters))
	ctx.RegisterPage("settings/playlists", http.HandlerFunc(pages.SettingsPlaylists))
	ctx.RegisterAction("POST", "subscriptions", http.HandlerFunc(pages.AddSubscription))
	ctx.RegisterAction("POST", "subscriptions/{id}/delete", http.HandlerFunc(pages.DeleteSubscription))
	ctx.RegisterAction("POST", "folders", http.HandlerFunc(pages.AddFolder))
	ctx.RegisterAction("POST", "folders/{id}/delete", http.HandlerFunc(pages.DeleteFolder))
	ctx.RegisterAction("POST", "filters", http.HandlerFunc(pages.AddFilter))
	ctx.RegisterAction("POST", "filters/{id}/delete", http.HandlerFunc(pages.DeleteFilter))
	ctx.RegisterAction("POST", "playlists", http.HandlerFunc(pages.AddPlaylist))
	ctx.RegisterAction("POST", "playlists/{id}/delete", http.HandlerFunc(pages.DeletePlaylist))
	ctx.RegisterAction("POST", "import", http.HandlerFunc(pages.Import))
	ctx.RegisterAction("POST", "opml/import", http.HandlerFunc(pages.ImportOPML))
	ctx.RegisterAction("POST", "talks/preview", http.HandlerFunc(pages.TalksPreview))
	ctx.RegisterAction("POST", "talks", http.HandlerFunc(pages.AddTalks))
	ctx.RegisterAPI("opml/export", http.HandlerFunc(pages.ExportOPML))

	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Handler: http.HandlerFunc(pages.SystemSettings),
		Action:  http.HandlerFunc(pages.SaveSystemSettings),
	})

	// NextCloud News API compatibility + content-hashed static assets.
	newsAPI := ncapi.New(ncapiBackend{p})
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("/index.php/apps/news/api/", newsAPI)

		sub, err := fs.Sub(staticFS, "static")
		if err != nil {
			return fmt.Errorf("feed: sub static: %w", err)
		}
		a, err := priv.RegisterAssets(sub)
		if err != nil {
			return err
		}
		pkgAssets = a
	}

	return nil
}

func (p *Plugin) Start(parent context.Context) error {
	// Discover the profanity plugin via the privileged context.
	if priv, ok := p.ctx.(plugin.PrivilegedContext); ok {
		for plug := range priv.Plugins() {
			if pp, ok := plug.(*profanity.Plugin); ok {
				p.profanity = pp
				break
			}
		}
	}

	if p.runOrchestra == nil {
		// No orchestrator injected; background polling is disabled.
		return nil
	}

	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel
	p.done = make(chan struct{})

	go func() {
		defer close(p.done)
		p.runOrchestra(ctx, p, p.profanity, p.tickInterval, p.log)
	}()
	return nil
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// Users returns the users who have used the feed, for the orchestrator.
func (p *Plugin) Users() ([]string, error) {
	return p.ctx.UsersOf("")
}

// ncapiBackend adapts the plugin to the narrow interface the NextCloud News
// API needs, so ncapi depends on two methods rather than the whole plugin.
type ncapiBackend struct{ p *Plugin }

func (b ncapiBackend) DB(username string) (*sql.DB, error) { return b.p.DB(username) }

func (b ncapiBackend) TriggerFetch(username string, subID int64) {
	b.p.TriggerFetch(username, subID)
}

// TriggerFetch nudges the orchestrator to process a user (or single subscription) before its next tick.
// SubID=0 means "scan all subscriptions for this user".
func (p *Plugin) TriggerFetch(username string, subID int64) {
	select {
	case p.pokes <- PokeRequest{Username: username, SubID: subID}:
	default:
		// Queue full; orchestrator will catch up on next tick.
		if p.log != nil {
			p.log.Debug("feed: TriggerFetch poke queue full", "username", username, "subID", subID)
		}
	}
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return fmt.Errorf("feed widget: db: %w", err)
	}
	store := NewStore(db)
	unread, err := store.CountUnread()
	if err != nil {
		return fmt.Errorf("feed widget: count unread: %w", err)
	}
	recent, err := store.RecentUnread(5)
	if err != nil {
		return fmt.Errorf("feed widget: recent: %w", err)
	}
	return feedWidget(unread, recent).Render(ctx, w)
}

// RenderWidget is an exported wrapper around renderWidget for tests.
func (p *Plugin) RenderWidget(ctx context.Context, w io.Writer, userID string) error {
	return p.renderWidget(ctx, w, userID)
}

// Profanity returns the discovered profanity plugin, or nil if not registered.
func (p *Plugin) Profanity() *profanity.Plugin { return p.profanity }

// DoctorChecks implements plugin.Doctor, reporting missing external tools.
func (p *Plugin) DoctorChecks(_ context.Context) []plugin.CheckResult {
	var out []plugin.CheckResult
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		out = append(out, plugin.CheckResult{
			Name:    "yt-dlp",
			Status:  plugin.CheckWarn,
			Message: "yt-dlp not found in PATH — required for YouTube subscriptions and auto-captions. Install from https://github.com/yt-dlp/yt-dlp",
		})
	} else {
		out = append(out, plugin.CheckResult{Name: "yt-dlp", Status: plugin.CheckOK})
	}
	if _, err := exec.LookPath("whisper-cli"); err != nil {
		out = append(out, plugin.CheckResult{
			Name:    "whisper-cli",
			Status:  plugin.CheckWarn,
			Message: "whisper-cli not found in PATH — required for transcription of non-YouTube audio. Install whisper.cpp from https://github.com/ggerganov/whisper.cpp",
		})
	} else {
		out = append(out, plugin.CheckResult{Name: "whisper-cli", Status: plugin.CheckOK})
	}
	return out
}
