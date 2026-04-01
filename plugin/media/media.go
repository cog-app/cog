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
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

const PluginID = "media"

// ScanStatus tracks the state of background source polling.
type ScanStatus struct {
	Running   bool
	LastRun   time.Time
	LastError string
	Warnings  []string
	Progress  string // e.g. "3/10 sources"
}

// Plugin is the media queue plugin.
type Plugin struct {
	ctx            plugin.PluginContext
	log            *slog.Logger
	cancel         context.CancelFunc
	done           chan struct{}
	client         *http.Client
	contentFilters []plugin.ContentFilter
	scanMu         sync.Mutex
	scanStatus     ScanStatus
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
	ctx.RegisterPage("source/{id}/settings", http.HandlerFunc(pages.SourceSettings))
	ctx.RegisterPage("queue/tag/{name}", http.HandlerFunc(pages.QueueByTag))

	// Action routes (POST/DELETE)
	ctx.RegisterAction("POST", "sources", http.HandlerFunc(pages.AddSource))
	ctx.RegisterAction("DELETE", "sources/{id}", http.HandlerFunc(pages.DeleteSource))
	ctx.RegisterAction("POST", "sources/{id}/refresh", http.HandlerFunc(pages.RefreshSource))
	ctx.RegisterAction("POST", "filters", http.HandlerFunc(pages.AddFilter))
	ctx.RegisterAction("DELETE", "filters/{id}", http.HandlerFunc(pages.DeleteFilterAction))
	ctx.RegisterAction("POST", "import", http.HandlerFunc(pages.Import))
	ctx.RegisterAction("POST", "groups", http.HandlerFunc(pages.AddGroup))
	ctx.RegisterAction("DELETE", "groups/{id}", http.HandlerFunc(pages.DeleteGroup))
	ctx.RegisterAction("POST", "queue/{episodeID}", http.HandlerFunc(pages.QueueAdd))
	ctx.RegisterAction("DELETE", "queue/{episodeID}", http.HandlerFunc(pages.QueueRemove))
	ctx.RegisterAction("POST", "queue/{episodeID}/move", http.HandlerFunc(pages.QueueMove))
	ctx.RegisterAction("POST", "episodes/{id}/progress", http.HandlerFunc(pages.UpdateProgress))
	ctx.RegisterAction("POST", "episodes/{id}/complete", http.HandlerFunc(pages.MarkComplete))
	ctx.RegisterAction("POST", "episodes/{id}/tag", http.HandlerFunc(pages.AddTag))
	ctx.RegisterAction("DELETE", "episodes/{id}/tag/{name}", http.HandlerFunc(pages.RemoveTag))

	ctx.RegisterAction("POST", "scan", http.HandlerFunc(pages.TriggerScan))

	// API routes (GET)
	ctx.RegisterAPI("api/stream-url/{id}", http.HandlerFunc(pages.StreamURL))
	ctx.RegisterAPI("api/filter-preview", http.HandlerFunc(pages.FilterPreview))
	ctx.RegisterAPI("api/clean-audio/{id}", http.HandlerFunc(pages.CleanAudio))
	ctx.RegisterAPI("api/clean-file/{id}", http.HandlerFunc(pages.ServeCleanFile))
	ctx.RegisterAPI("api/scan-status", http.HandlerFunc(pages.ScanStatusPartial))
	ctx.RegisterAPI("api/user-scan-status", http.HandlerFunc(pages.UserScanStatusPartial))

	// Admin settings pages.
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Handler: http.HandlerFunc(pages.AdminCacheSettings),
		Action:  http.HandlerFunc(pages.SaveAdminCacheSettings),
	})
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Slug:   "scan",
		Action: http.HandlerFunc(pages.TriggerSystemScan),
	})
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Slug:   "purge",
		Action: http.HandlerFunc(pages.PurgeCache),
	})

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	// Discover content filter plugins.
	if priv, ok := p.ctx.(plugin.PrivilegedContext); ok {
		for plug := range priv.Plugins() {
			if cf, ok := plug.(plugin.ContentFilter); ok {
				p.contentFilters = append(p.contentFilters, cf)
			}
		}
	}

	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go p.poll(ctx)
	return nil
}

// filterHTML applies all content filters to HTML content.
func (p *Plugin) filterHTML(username, html string) string {
	for _, cf := range p.contentFilters {
		html = cf.FilterHTML(username, html)
	}
	return html
}

// filterText applies all content filters to plain text.
func (p *Plugin) filterText(username, text string) string {
	for _, cf := range p.contentFilters {
		text = cf.FilterText(username, text)
	}
	return text
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// rootDB returns the plugin-global database for storing admin settings.
func (p *Plugin) rootDB() (*sql.DB, error) {
	db, err := p.ctx.RootDB()
	if err != nil {
		return nil, err
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`)
	return db, nil
}

func (p *Plugin) getSetting(key, defaultVal string) string {
	db, err := p.rootDB()
	if err != nil {
		return defaultVal
	}
	var val string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val); err != nil {
		return defaultVal
	}
	return val
}

func (p *Plugin) setSetting(key, val string) error {
	db, err := p.rootDB()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, val)
	return err
}

// CacheVolumeName returns the configured cache volume name, or "default".
func (p *Plugin) CacheVolumeName() string {
	return p.getSetting("cache_volume", "default")
}

// GetScanStatus returns the current scan status.
func (p *Plugin) GetScanStatus() ScanStatus {
	p.scanMu.Lock()
	defer p.scanMu.Unlock()
	s := p.scanStatus
	// Copy warnings slice to avoid races.
	if len(s.Warnings) > 0 {
		w := make([]string, len(s.Warnings))
		copy(w, s.Warnings)
		s.Warnings = w
	}
	return s
}

// RequestScan triggers an immediate scan for a user (or "*" for all users).
func (p *Plugin) RequestScan(username string) {
	go func() {
		ctx := context.Background()
		if username == "*" {
			p.pollAllUsers(ctx)
		} else {
			p.pollUser(ctx, username, p.ctx.Now().Unix())
		}
	}()
}

func (p *Plugin) setScanRunning(progress string) {
	p.scanMu.Lock()
	p.scanStatus.Running = true
	p.scanStatus.Progress = progress
	p.scanStatus.Warnings = nil
	p.scanMu.Unlock()
}

func (p *Plugin) setScanProgress(progress string) {
	p.scanMu.Lock()
	p.scanStatus.Progress = progress
	p.scanMu.Unlock()
}

func (p *Plugin) addScanWarning(msg string) {
	p.scanMu.Lock()
	p.scanStatus.Warnings = append(p.scanStatus.Warnings, msg)
	p.scanMu.Unlock()
}

func (p *Plugin) setScanDone(lastErr string) {
	p.scanMu.Lock()
	p.scanStatus.Running = false
	p.scanStatus.LastRun = p.ctx.Now()
	p.scanStatus.LastError = lastErr
	p.scanMu.Unlock()
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
	if err := p.fetchSource(context.Background(), db, username, src); err != nil {
		p.log.Error("immediate fetch", "url", src.URL, "err", err)
	}
}

func (p *Plugin) fetchSource(ctx context.Context, db *sql.DB, username string, src *Source) error {
	now := p.ctx.Now().Unix()
	switch src.Kind {
	case SourcePodcast:
		return fetchPodcast(p.client, db, src, p.ctx.Now)
	case SourceYouTube:
		err := fetchYouTube(ctx, db, src, p.ctx.Now)
		if err == nil {
			p.fetchMissingCaptions(ctx, db, username, src)
		}
		return err
	case SourceURL:
		// One-off sources are not polled.
		return RecordSourceSuccess(db, src, "", "", now)
	default:
		return fmt.Errorf("unknown source kind: %s", src.Kind)
	}
}

// fetchMissingCaptions fetches captions for YouTube episodes that don't have them yet,
// and produces clean audio files when content filters are available.
func (p *Plugin) fetchMissingCaptions(ctx context.Context, db *sql.DB, username string, src *Source) {
	if src.Kind != SourceYouTube {
		return
	}
	episodes, err := ListEpisodesBySource(db, src.ID, 50)
	if err != nil {
		return
	}
	for _, ep := range episodes {
		if ep.CaptionsPath == "" {
			vttPath, err := ytdlpFetchCaptionsForEpisode(ctx, p.ctx.DataDir(), username, ep.ID, ep.MediaURL)
			if err != nil {
				p.log.Debug("caption fetch failed", "episode", ep.ID, "err", err)
				continue
			}
			SetCaptionsPath(db, ep.ID, vttPath)
			ep.CaptionsPath = vttPath
		}

		// Produce clean audio if we have captions and content filters.
		if ep.CaptionsPath != "" && ep.CleanAudioPath == "" && len(p.contentFilters) > 0 {
			p.produceCleanAudio(ctx, db, username, &ep)
		}
	}
}

// produceCleanAudio creates a profanity-cleaned version of an episode's audio.
func (p *Plugin) produceCleanAudio(ctx context.Context, db *sql.DB, username string, ep *EpisodeView) {
	f, err := os.Open(ep.CaptionsPath)
	if err != nil {
		return
	}
	captions := profanity.ParseVTT(f)
	f.Close()

	if len(captions) == 0 {
		return
	}

	cleanDir := filepath.Join(p.ctx.DataDir(), username, "app_data", "media", "clean")
	os.MkdirAll(cleanDir, 0o755)
	cleanPath := filepath.Join(cleanDir, fmt.Sprintf("%d_clean.mp4", ep.ID))

	for _, cf := range p.contentFilters {
		if err := cf.CleanAudioFile(ctx, username, ep.MediaURL, cleanPath, captions); err != nil {
			p.log.Debug("clean audio failed", "episode", ep.ID, "err", err)
			continue
		}
		if _, err := os.Stat(cleanPath); err == nil {
			SetCleanAudioPath(db, ep.ID, cleanPath)
			p.log.Info("produced clean audio", "episode", ep.ID)
			return
		}
	}
}
