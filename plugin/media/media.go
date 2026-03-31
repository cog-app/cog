// Package media implements a media queue plugin for podcasts, YouTube channels, and arbitrary media URLs.
package media

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/exec"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "media"

// Plugin is the media queue plugin.
type Plugin struct {
	ctx    plugin.PluginContext
	log    *slog.Logger
	cancel context.CancelFunc
	done   chan struct{}
	client *http.Client
}

// New creates a new Media plugin instance.
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

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{
		Label: "Media",
		URL:   "/apps/media",
		Icon:  `<svg class="nav-icon" viewBox="0 0 24 24"><polygon points="5 3 19 12 5 21 5 3"></polygon></svg>`,
	})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Up Next",
		Priority: 25,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "episodes",
		EnrichSQL:    "SELECT rowid, title, media_type, description FROM episodes WHERE rowid IN (?)",
	})

	pages := NewPages(p)

	// Page routes (GET)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Queue))
	ctx.RegisterPage("browse", http.HandlerFunc(pages.Browse))
	ctx.RegisterPage("source/{id}", http.HandlerFunc(pages.Source))
	ctx.RegisterPage("episode/{id}", http.HandlerFunc(pages.EpisodeDetail))
	ctx.RegisterPage("history", http.HandlerFunc(pages.History))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))
	ctx.RegisterPage("tag/{name}", http.HandlerFunc(pages.TagView))

	// Action routes (POST/DELETE)
	ctx.RegisterAction("POST", "sources", http.HandlerFunc(pages.AddSource))
	ctx.RegisterAction("DELETE", "sources/{id}", http.HandlerFunc(pages.DeleteSource))
	ctx.RegisterAction("POST", "sources/{id}/refresh", http.HandlerFunc(pages.RefreshSource))
	ctx.RegisterAction("POST", "groups", http.HandlerFunc(pages.AddGroup))
	ctx.RegisterAction("DELETE", "groups/{id}", http.HandlerFunc(pages.DeleteGroup))
	ctx.RegisterAction("POST", "queue/{episodeID}", http.HandlerFunc(pages.QueueAdd))
	ctx.RegisterAction("DELETE", "queue/{episodeID}", http.HandlerFunc(pages.QueueRemove))
	ctx.RegisterAction("POST", "queue/{episodeID}/move", http.HandlerFunc(pages.QueueMove))
	ctx.RegisterAction("POST", "episodes/{id}/progress", http.HandlerFunc(pages.UpdateProgress))
	ctx.RegisterAction("POST", "episodes/{id}/complete", http.HandlerFunc(pages.MarkComplete))
	ctx.RegisterAction("POST", "episodes/{id}/tag", http.HandlerFunc(pages.AddTag))
	ctx.RegisterAction("DELETE", "episodes/{id}/tag/{name}", http.HandlerFunc(pages.RemoveTag))

	// API routes (GET)
	ctx.RegisterAPI("api/stream-url/{id}", http.HandlerFunc(pages.StreamURL))

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go p.poll(ctx)
	return nil
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// DB returns the media plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate media db for %s: %w", username, err)
	}
	return db, nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	queue, _ := ListQueue(db)
	if len(queue) > 3 {
		queue = queue[:3]
	}
	return upNextWidget(queue).Render(ctx, w)
}

// DoctorChecks reports whether yt-dlp is available.
func (p *Plugin) DoctorChecks() []string {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return []string{"yt-dlp not found in PATH (YouTube sources will not work)"}
	}
	return nil
}

// FetchSourceNow immediately fetches new episodes for a source.
func (p *Plugin) FetchSourceNow(username string, sourceID int64) {
	db, err := p.DB(username)
	if err != nil {
		return
	}
	src, err := GetSource(db, sourceID)
	if err != nil {
		return
	}
	if err := p.fetchSource(context.Background(), db, src); err != nil {
		p.log.Error("immediate fetch", "url", src.URL, "err", err)
	}
}

func (p *Plugin) fetchSource(ctx context.Context, db *sql.DB, src *Source) error {
	now := p.ctx.Now().Unix()
	switch src.Kind {
	case SourcePodcast:
		return fetchPodcast(p.client, db, src, p.ctx.Now)
	case SourceYouTube:
		return fetchYouTube(ctx, db, src, p.ctx.Now)
	case SourceURL:
		// One-off sources are not polled.
		return RecordSourceSuccess(db, src, "", "", now)
	default:
		return fmt.Errorf("unknown source kind: %s", src.Kind)
	}
}
