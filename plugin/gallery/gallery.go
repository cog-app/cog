// Package gallery implements a photo and video gallery plugin with EXIF extraction,
// auto-albums, and NextCloud Memories API compatibility.
package gallery

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
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
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

//go:embed sql
var sqlFS embed.FS

//go:embed photo_folder.svg
var photoFolderIcon string

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
	contacts *contacts.Plugin
	geocoder *Geocoder
	cancel   context.CancelFunc
	done     chan struct{}

	scanMu     sync.Mutex
	scanClient scannerClient // RPC or in-process scan worker client
	isWorker   bool          // true when running as scan worker subprocess
	workerPID  int           // PID of scan worker subprocess (for RSS monitoring)

	// activeBackends holds recognition backends during a scan so
	// ReleaseMemory can free ONNX sessions under memory pressure.
	activeBackendsMu sync.Mutex
	activeBackends   []RecognitionBackend
}

// New creates a new Gallery plugin instance.
func New() *Plugin {
	return &Plugin{
		geocoder: NewDefaultGeocoder(),
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

	// Obtain the concrete files plugin for operations not on FilesAccess
	// (DB, Rename, Preview, Upload).
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		if fp, found := priv.PluginByID("files"); found {
			p.files = fp.(*files.Plugin)
		}
		if cp, found := priv.PluginByID("contacts"); found {
			p.contacts = cp.(*contacts.Plugin)
		}
		priv.RegisterAdminCommand(plugin.CommandDef{
			Name:  "scan",
			Short: "scan media files",
			Sub: []plugin.CommandDef{
				{
					Name: "user", Short: "scan a specific user", Args: []string{"username"},
					Run: func(cmdCtx context.Context, args []string) error {
						return p.ScanUser(cmdCtx, args[0])
					},
				},
				{
					Name: "worker", Short: "run scan loop as background worker (launched by server)",
					Run: func(cmdCtx context.Context, args []string) error {
						return p.runScanWorker(cmdCtx)
					},
				},
			},
		})
	}

	ctx.RegisterNavItem(plugin.NavItem{Label: "Gallery", URL: "/apps/gallery", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Photos",
		Priority: 25,
		URL:      "/apps/gallery/",
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
	ctx.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	ctx.RegisterPage("duplicates", http.HandlerFunc(pages.Duplicates))
	ctx.RegisterPage("duplicates/{id}", http.HandlerFunc(pages.DuplicateGroupView))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))

	ctx.RegisterAPI("api/timeline", http.HandlerFunc(pages.TimelineAPI))
	ctx.RegisterAPI("api/media/{id}", http.HandlerFunc(pages.MediaDetailAPI))
	ctx.RegisterAPI("api/media/{id}/thumbnail", http.HandlerFunc(pages.MediaThumbnail))
	ctx.RegisterAPI("api/face/{id}/thumbnail", http.HandlerFunc(pages.FaceThumbnail))
	ctx.RegisterAPI("api/scan-status", http.HandlerFunc(pages.ScanStatusPartial))
	ctx.RegisterAPI("api/user-scan-status", http.HandlerFunc(pages.UserScanStatusPartial))
	ctx.RegisterAPI("api/tags/search", http.HandlerFunc(pages.SearchTagsAPI))
	ctx.RegisterAPI("api/people/search", http.HandlerFunc(pages.SearchPeopleAPI))
	// pprof endpoint is unauthenticated for debugging — registered via privileged route.
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("GET /apps/gallery/api/worker/pprof/{profile...}", http.HandlerFunc(p.workerPprof))
	}

	ctx.RegisterAction("POST", "albums", http.HandlerFunc(pages.CreateAlbum))
	ctx.RegisterAction("POST", "albums/{id}/add", http.HandlerFunc(pages.AddToAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}/media/{media_id}", http.HandlerFunc(pages.RemoveFromAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}", http.HandlerFunc(pages.DeleteAlbum))
	ctx.RegisterAction("POST", "albums/{id}/delete", http.HandlerFunc(pages.DeleteAlbum))
	ctx.RegisterAction("POST", "albums/{id}/rename", http.HandlerFunc(pages.RenameAlbum))
	ctx.RegisterAction("POST", "media/{id}/delete", http.HandlerFunc(pages.DeletePhoto))
	ctx.RegisterAction("POST", "people/{id}/name", http.HandlerFunc(pages.NamePerson))
	ctx.RegisterAction("POST", "people/{id}/merge", http.HandlerFunc(pages.MergePeople))
	ctx.RegisterAction("POST", "people/{id}/hide", http.HandlerFunc(pages.HidePerson))
	ctx.RegisterAction("POST", "people/{id}/remove-face", http.HandlerFunc(pages.RemoveFaceFromPerson))
	ctx.RegisterAction("POST", "media/{id}/tags", http.HandlerFunc(pages.AddMediaTagAction))
	ctx.RegisterAction("POST", "media/{id}/remove-tag", http.HandlerFunc(pages.RemoveMediaTagAction))
	ctx.RegisterAction("POST", "media/{id}/delete-face", http.HandlerFunc(pages.DeleteFaceAction))
	ctx.RegisterAction("POST", "media/{id}/star", http.HandlerFunc(pages.TogglePhotoStar))
	ctx.RegisterAction("POST", "faces/{id}/new-person", http.HandlerFunc(pages.CreatePersonFromFaceAction))
	ctx.RegisterAction("POST", "settings/scan", http.HandlerFunc(pages.TriggerScan))
	ctx.RegisterAction("POST", "settings/folders", http.HandlerFunc(pages.SaveScanFolders))
	ctx.RegisterAPI("api/scan-folders-preview", http.HandlerFunc(pages.ScanFoldersPreview))
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
	ctx.RegisterAction("POST", "duplicates/{id}/keep", http.HandlerFunc(pages.SetDuplicateKeep))
	ctx.RegisterAction("POST", "duplicates/{id}/unmark", http.HandlerFunc(pages.UnmarkDuplicate))
	ctx.RegisterAction("POST", "duplicates/{id}/delete", http.HandlerFunc(pages.DeleteNonKeepDuplicates))
	ctx.RegisterAction("POST", "duplicates/delete-all", http.HandlerFunc(pages.DeleteAllNonKeep))
	ctx.RegisterAction("POST", "duplicates/{id}/delete-member", http.HandlerFunc(pages.DeleteDuplicateMember))
	ctx.RegisterAction("POST", "duplicates/{id}/delete-all", http.HandlerFunc(pages.DeleteAllDuplicates))
	ctx.RegisterAction("POST", "duplicates/{id}/dismiss", http.HandlerFunc(pages.DismissDuplicateGroup))
	ctx.RegisterPage("rename/preview", http.HandlerFunc(pages.RenamePreview))
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
	if useSubprocess() {
		go p.manageScanWorker(ctx)
	} else {
		go p.manageInProcessWorker(ctx)
	}
	go p.scanTimer(ctx)
	return nil
}

// manageInProcessWorker runs the scan worker in-process instead of a subprocess.
func (p *Plugin) manageInProcessWorker(ctx context.Context) {
	defer close(p.done)

	client := newInProcessScanClient(p, ctx)
	p.scanMu.Lock()
	p.scanClient = client
	p.scanMu.Unlock()

	<-ctx.Done()

	p.scanMu.Lock()
	p.scanClient = nil
	p.scanMu.Unlock()
}

func (p *Plugin) Stop() error {
	if p.cancel != nil {
		p.cancel()
		<-p.done
	}
	return nil
}

// directDB opens the gallery database without the search hooks, which would
// otherwise contend with the server for the Bleve/bbolt index files. Used by
// the scan worker, which runs as a separate process alongside the server.
func (p *Plugin) directDB(username string) (*sql.DB, error) {
	return p.ctx.PluginDB(username, plugin.NoHooks)
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

// workerPprof proxies pprof profile requests to the scan worker subprocess.
// Supports: heap, goroutine, allocs, block, mutex, threadcreate, profile (CPU).
func (p *Plugin) workerPprof(w http.ResponseWriter, r *http.Request) {
	p.scanMu.Lock()
	client := p.scanClient
	p.scanMu.Unlock()
	if client == nil {
		http.Error(w, "scan worker not running", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("profile")

	// Index page: show RSS, scan status, memstats, and links to profiles.
	if name == "" {
		stats, err := client.MemStats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		status, _ := client.GetStatus()

		p.scanMu.Lock()
		pid := p.workerPID
		p.scanMu.Unlock()
		rss := readRSS(pid)

		scanState := "Idle"
		if status.Running {
			scanState = "Running"
		}
		lastRun := "never"
		if !status.LastRun.IsZero() {
			lastRun = time.Since(status.LastRun).Truncate(time.Second).String() + " ago"
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		base := "/apps/gallery/api/worker/pprof"
		fmt.Fprintf(w, `<html><head><title>Worker pprof</title><meta http-equiv="refresh" content="5"></head><body>
<h1>Gallery Scan Worker (PID %d)</h1>
<table>
<tr><td><b>Status</b></td><td><b>%s</b></td></tr>
<tr><td>Last Scan</td><td>%s</td></tr>
<tr><td><b>Process RSS</b></td><td><b>%s</b></td></tr>
<tr><td>Heap In-Use</td><td>%s</td></tr>
<tr><td>Heap Alloc</td><td>%s</td></tr>
<tr><td>Heap Sys</td><td>%s</td></tr>
<tr><td>Total Sys (Go)</td><td>%s</td></tr>
<tr><td>GC Cycles</td><td>%d</td></tr>
<tr><td>Goroutines</td><td>%d</td></tr>
</table>
<h2>Profiles</h2>
<ul>
<li><a href="%s/heap?debug=1">heap</a></li>
<li><a href="%s/allocs?debug=1">allocs</a></li>
<li><a href="%s/goroutine?debug=1">goroutine</a></li>
<li><a href="%s/block?debug=1">block</a></li>
<li><a href="%s/mutex?debug=1">mutex</a></li>
<li><a href="%s/threadcreate?debug=1">threadcreate</a></li>
<li><a href="%s/profile?seconds=10">profile (CPU, 10s)</a></li>
</ul>
</body></html>`,
			pid, scanState, lastRun, formatBytes(rss),
			formatBytes(stats.HeapInuse), formatBytes(stats.HeapAlloc),
			formatBytes(stats.HeapSys), formatBytes(stats.Sys),
			stats.NumGC, stats.Goroutines,
			base, base, base, base, base, base, base)
		return
	}

	debug := 1
	if d := r.URL.Query().Get("debug"); d != "" {
		fmt.Sscanf(d, "%d", &debug)
	}
	seconds := 0
	if s := r.URL.Query().Get("seconds"); s != "" {
		fmt.Sscanf(s, "%d", &seconds)
	}

	data, err := client.Profile(name, debug, seconds)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if debug > 0 && name != "profile" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Write(data)
}

// logPipe implements io.Writer and forwards each line to a slog.Logger.
// The subprocess is launched with COG_LOG_FORMAT=json so each line is a JSON
// object with "level", "msg", and "source" fields. Lines that fail to parse
// are logged as-is at INFO level.
type logPipe struct {
	logger *slog.Logger
	buf    []byte
}

func (lp *logPipe) Write(p []byte) (int, error) {
	lp.buf = append(lp.buf, p...)
	for {
		i := bytes.IndexByte(lp.buf, '\n')
		if i < 0 {
			break
		}
		line := lp.buf[:i]
		lp.buf = lp.buf[i+1:]
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Level  string `json:"level"`
			Msg    string `json:"msg"`
			Source string `json:"source"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Msg == "" {
			lp.logger.Info(string(line), "source", "scan-worker")
			continue
		}
		var level slog.Level
		switch rec.Level {
		case "DEBUG":
			level = slog.LevelDebug
		case "WARN":
			level = slog.LevelWarn
		case "ERROR":
			level = slog.LevelError
		default:
			level = slog.LevelInfo
		}
		attrs := []any{"source", "scan-worker"}
		if rec.Source != "" {
			attrs = append(attrs, "subsource", rec.Source)
		}
		lp.logger.Log(context.Background(), level, rec.Msg, attrs...)
	}
	return len(p), nil
}

func formatBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return fmt.Errorf("recent photos widget: %w", err)
	}
	media, err := RecentMedia(db, 8)
	if err != nil {
		return fmt.Errorf("recent photos widget: %w", err)
	}
	return recentPhotosWidget(media).Render(ctx, w)
}

// GetScanStatus returns a snapshot of the current scan job status.
func (p *Plugin) GetScanStatus() ScanStatus {
	p.scanMu.Lock()
	client := p.scanClient
	p.scanMu.Unlock()
	if client == nil {
		return ScanStatus{}
	}
	status, err := client.GetStatus()
	if err != nil {
		return ScanStatus{}
	}
	return status
}

// requestScan asks the scan worker to scan a user (or "*" for all users).
func (p *Plugin) requestScan(username string) {
	p.scanMu.Lock()
	client := p.scanClient
	p.scanMu.Unlock()
	if client == nil {
		return
	}
	if username == "*" {
		client.ScanAllUsers()
	} else {
		client.ScanUser(username)
	}
}

// manageScanWorker spawns and supervises the scan worker subprocess.
func (p *Plugin) manageScanWorker(ctx context.Context) {
	defer close(p.done)

	exePath, err := os.Executable()
	if err != nil {
		p.log.Error("cannot find executable for scan worker", "err", err)
		return
	}

	for {
		if ctx.Err() != nil {
			return
		}

		cmd := exec.CommandContext(ctx, exePath, "gallery", "scan", "worker")
		cmd.Env = append(os.Environ(), "GOMEMLIMIT=512MiB", "COG_LOG_FORMAT=json")
		// Pipe worker stderr through the server's logger so logs appear in the GUI.
		cmd.Stderr = &logPipe{logger: p.log}

		stdin, err := cmd.StdinPipe()
		if err != nil {
			p.log.Error("scan worker stdin pipe", "err", err)
			break
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			p.log.Error("scan worker stdout pipe", "err", err)
			break
		}

		if err := cmd.Start(); err != nil {
			p.log.Error("start scan worker", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
				continue
			}
		}

		pid := cmd.Process.Pid
		p.log.Info("scan worker started", "pid", pid)
		setNice(pid, 10)

		// Create RPC client over the subprocess pipes.
		client := newRPCScanClient(readWriteCloser{stdout, stdin})
		p.scanMu.Lock()
		p.scanClient = client
		p.workerPID = pid
		p.scanMu.Unlock()

		// Start RSS watchdog.
		watchdogDone := make(chan struct{})
		go p.rssWatchdog(ctx, pid, client, watchdogDone)

		// Wait for subprocess to exit.
		err = cmd.Wait()
		close(watchdogDone)

		p.scanMu.Lock()
		p.scanClient = nil
		p.workerPID = 0
		p.scanMu.Unlock()
		client.Close()

		if ctx.Err() != nil {
			return
		}
		p.log.Warn("scan worker exited, restarting in 10s", "err", err)
		time.Sleep(10 * time.Second)
	}
}

// scanTimer triggers periodic scans via the RPC client.
func (p *Plugin) scanTimer(ctx context.Context) {
	// Initial delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Second):
	}
	p.requestScan("*")

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.requestScan("*")
		}
	}
}

// FolderIcon implements plugin.FolderIconProvider. It returns a photo-folder
// icon for directories that match the user's configured scan folder patterns.
func (p *Plugin) FolderIcon(username, folderPath string) (string, bool) {
	db, err := p.DB(username)
	if err != nil {
		return "", false
	}
	patterns := ParseScanFolders(db)
	path := TrimScanPath(folderPath)
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	for _, pat := range patterns {
		pat = TrimScanPath(pat)
		if !strings.HasSuffix(pat, "/") {
			continue // only match folder patterns
		}
		if path == pat || strings.HasPrefix(path, pat) {
			return photoFolderIcon, true
		}
	}
	return "", false
}
