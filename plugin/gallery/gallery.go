// Package gallery implements a photo and video gallery plugin with EXIF extraction,
// auto-albums, and NextCloud Memories API compatibility.
package gallery

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
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

//go:embed sql/schema.sql
var sqlFS embed.FS

const PluginID = "gallery"

// ScanStatus holds the current state of the background scan job.
type ScanStatus struct {
	Running   bool
	LastRun   time.Time
	LastError error // first non-nil error from the scan
	Warnings  []string
}

// Plugin is the gallery plugin.
type Plugin struct {
	ctx      plugin.PluginContext
	log      *slog.Logger
	files    *files.Plugin
	geocoder *Geocoder
	cancel   context.CancelFunc
	done     chan struct{}

	scanMu     sync.Mutex
	scanStatus ScanStatus
}

// New creates a new Gallery plugin instance.
func New() *Plugin {
	return &Plugin{
		geocoder: NewDefaultGeocoder(),
	}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	// Obtain the concrete files plugin for operations not on FilesAccess
	// (DB, Rename, Preview, Upload).
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		if fp, found := priv.PluginByID("files"); found {
			p.files = fp.(*files.Plugin)
		}
		priv.RegisterAdminCommand(plugin.CommandDef{
			Name:  "scan",
			Short: "scan media files for a user",
			Args:  []string{"username"},
			Run: func(cmdCtx context.Context, args []string) error {
				return p.ScanUser(cmdCtx, args[0])
			},
		})
	}

	ctx.RegisterNavItem(plugin.NavItem{Label: "Gallery", URL: "/apps/gallery", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Photos",
		Priority: 25,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "media",
		EnrichSQL:    "SELECT rowid, file_path, place_name, camera_model, ocr_text FROM media WHERE rowid IN (?)",
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "album",
		Table:    "albums",
		IDColumn: "id",
	})

	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Timeline))
	ctx.RegisterPage("albums", http.HandlerFunc(pages.Albums))
	ctx.RegisterPage("album/{id}", http.HandlerFunc(pages.AlbumView))
	ctx.RegisterPage("photo/{id}", http.HandlerFunc(pages.PhotoView))
	ctx.RegisterPage("people", http.HandlerFunc(pages.People))
	ctx.RegisterPage("person/{id}", http.HandlerFunc(pages.PersonView))
	ctx.RegisterPage("tags", http.HandlerFunc(pages.Tags))
	ctx.RegisterPage("tag/{name}", http.HandlerFunc(pages.TagView))
	ctx.RegisterPage("videos", http.HandlerFunc(pages.Videos))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))

	ctx.RegisterAPI("api/timeline", http.HandlerFunc(pages.TimelineAPI))
	ctx.RegisterAPI("api/media/{id}", http.HandlerFunc(pages.MediaDetailAPI))
	ctx.RegisterAPI("api/media/{id}/thumbnail", http.HandlerFunc(pages.MediaThumbnail))
	ctx.RegisterAPI("api/scan-status", http.HandlerFunc(pages.ScanStatusPartial))

	ctx.RegisterAction("POST", "albums", http.HandlerFunc(pages.CreateAlbum))
	ctx.RegisterAction("POST", "albums/{id}/add", http.HandlerFunc(pages.AddToAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}/media/{media_id}", http.HandlerFunc(pages.RemoveFromAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}", http.HandlerFunc(pages.DeleteAlbum))
	ctx.RegisterAction("POST", "people/{id}/name", http.HandlerFunc(pages.NamePerson))
	ctx.RegisterAction("POST", "people/{id}/merge", http.HandlerFunc(pages.MergePeople))
	ctx.RegisterAction("POST", "people/{id}/hide", http.HandlerFunc(pages.HidePerson))
	ctx.RegisterAction("POST", "settings/scan", http.HandlerFunc(pages.TriggerScan))
	ctx.RegisterAction("POST", "settings/recognition", http.HandlerFunc(pages.SaveRecognitionSettings))

	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Handler: http.HandlerFunc(pages.SystemSettings),
		Action:  http.HandlerFunc(pages.SaveSystemSettings),
	})
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Slug:   "scan",
		Action: http.HandlerFunc(pages.TriggerSystemScan),
	})
	ctx.RegisterSettingsPage(plugin.SettingsPage{
		Slug:   "purge-recognitions",
		Action: http.HandlerFunc(pages.PurgeRecognitions),
	})
	ctx.RegisterAction("POST", "rename/preview", http.HandlerFunc(pages.RenamePreview))
	ctx.RegisterAction("POST", "rename/apply", http.HandlerFunc(pages.RenameApply))
	ctx.RegisterAction("POST", "rename/undo", http.HandlerFunc(pages.RenameUndo))

	// Memories API compatibility
	memoriesAPI := NewMemoriesAPI(p)
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("GET /apps/memories/api/{path...}", memoriesAPI)
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)
	p.done = make(chan struct{})
	go p.scanLoop(ctx)
	return nil
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// DB returns the gallery plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate gallery db for %s: %w", username, err)
	}
	return db, nil
}

// SystemDB returns the plugin-global database for system-level settings.
func (p *Plugin) SystemDB() (*sql.DB, error) {
	db, err := p.ctx.RootDB()
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS gallery_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`)
	if err != nil {
		return nil, fmt.Errorf("create gallery_settings: %w", err)
	}
	return db, nil
}

// AggregateRecognitionStats sums recognition stats across all user gallery databases.
func (p *Plugin) AggregateRecognitionStats() RecognitionStats {
	var total RecognitionStats
	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		return total
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()
		filesDBPath := filepath.Join(p.ctx.DataDir(), username, "app_data", "files", "plugin.db")
		if _, err := os.Stat(filesDBPath); err != nil {
			continue
		}
		db, err := p.DB(username)
		if err != nil {
			continue
		}
		s, err := GetRecognitionStats(db)
		if err != nil {
			p.log.Warn("recognition stats", "user", username, "err", err)
			continue
		}
		total.TotalImages += s.TotalImages
		total.Pending += s.Pending
		total.Done += s.Done
		total.Errored += s.Errored
		total.Errors = append(total.Errors, s.Errors...)
	}
	return total
}

// DoctorChecks implements plugin.Doctor, checking external dependencies.
func (p *Plugin) DoctorChecks(ctx context.Context) []plugin.CheckResult {
	var results []plugin.CheckResult

	// Check CLI tools.
	for _, name := range []string{"tesseract", "ffmpeg", "ffprobe"} {
		r := plugin.CheckResult{Name: name, Status: plugin.CheckOK}
		if _, err := exec.LookPath(name); err != nil {
			r.Status = plugin.CheckWarn
			r.Message = name + " not found in PATH"
		}
		results = append(results, r)
	}

	// Check ONNX runtime — only verify the shared library is present.
	// We skip InitErr() because it downloads models and initializes the
	// full runtime, which is too heavy for a dependency check.
	r := plugin.CheckResult{Name: "onnxruntime", Status: plugin.CheckOK}
	b := &OnnxBackend{}
	if !b.Available() {
		r.Status = plugin.CheckWarn
		r.Message = "libonnxruntime.so not found; local image recognition disabled"
	}
	results = append(results, r)

	return results
}

// PurgeAllRecognitions resets recognition state across all user databases.
func (p *Plugin) PurgeAllRecognitions() error {
	entries, err := os.ReadDir(p.ctx.DataDir())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		username := entry.Name()
		filesDBPath := filepath.Join(p.ctx.DataDir(), username, "app_data", "files", "plugin.db")
		if _, err := os.Stat(filesDBPath); err != nil {
			continue
		}
		db, err := p.DB(username)
		if err != nil {
			p.log.Warn("purge: open db", "user", username, "err", err)
			continue
		}
		if err := PurgeRecognitions(db); err != nil {
			return fmt.Errorf("purge %s: %w", username, err)
		}
		p.log.Info("purged recognitions", "user", username)
	}
	return nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	media, _ := RecentMedia(db, 8)
	return recentPhotosWidget(media).Render(ctx, w)
}

// GetScanStatus returns a snapshot of the current scan job status.
func (p *Plugin) GetScanStatus() ScanStatus {
	p.scanMu.Lock()
	defer p.scanMu.Unlock()
	return p.scanStatus
}

// runScan executes scanAllUsers with status tracking.
func (p *Plugin) runScan(ctx context.Context) {
	p.scanMu.Lock()
	p.scanStatus.Running = true
	p.scanMu.Unlock()

	p.log.Info("background scan started")
	start := p.ctx.Now()
	warnings, err := p.scanAllUsersErr(ctx)
	elapsed := time.Since(start)

	p.scanMu.Lock()
	p.scanStatus.Running = false
	p.scanStatus.LastRun = p.ctx.Now()
	p.scanStatus.LastError = err
	p.scanStatus.Warnings = warnings
	p.scanMu.Unlock()

	if err != nil {
		p.log.Error("background scan failed", "elapsed", elapsed, "err", err)
	} else {
		p.log.Info("background scan complete", "elapsed", elapsed, "warnings", len(warnings))
	}
}

// scanLoop is the background scanning goroutine.
func (p *Plugin) scanLoop(ctx context.Context) {
	defer close(p.done)

	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.runScan(ctx)
		case <-ticker.C:
			p.runScan(ctx)
		}
	}
}
