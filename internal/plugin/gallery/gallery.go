// Package gallery implements a photo and video gallery plugin with EXIF extraction,
// auto-albums, and NextCloud Memories API compatibility.
package gallery

//go:generate go tool templ generate -log-level warn

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

//go:embed sql/schema.sql
var sqlFS embed.FS

const PluginID = "gallery"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
	UserFilesDir(username string) string
	EnsureUserDirs(username string) error
	NavItems() []plugin.NavItem
}

// Plugin is the gallery plugin.
type Plugin struct {
	registry  dbProvider
	files     *files.Plugin
	secret    []byte
	dataDir   string
	geocoder  *Geocoder
	cancel    context.CancelFunc
	done      chan struct{}
}

// New creates a new Gallery plugin instance.
func New(registry dbProvider, files *files.Plugin, dataDir string, secret []byte) *Plugin {
	return &Plugin{
		registry: registry,
		files:    files,
		dataDir:  dataDir,
		secret:   secret,
		geocoder: NewDefaultGeocoder(),
	}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar plugin.Registrar) error {
	registrar.RegisterNavItem(plugin.NavItem{Label: "Gallery", URL: "/apps/gallery", Icon: "gallery"})
	registrar.RegisterWidget(plugin.Widget{
		Title:    "Recent Photos",
		Priority: 25,
		Render:   p.renderWidget,
	})
	registrar.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "media",
		EnrichSQL:    "SELECT rowid, file_path, place_name, camera_model, ocr_text FROM media WHERE rowid IN (?)",
		Fields: []plugin.SearchField{
			{Column: "file_path", Field: "name"},
			{Column: "place_name", Field: "subtext"},
			{Column: "ocr_text", Field: "content"},
		},
	})

	pages := NewPages(p, p.secret, p.registry)
	registrar.RegisterPage("{$}", http.HandlerFunc(pages.Timeline))
	registrar.RegisterPage("albums", http.HandlerFunc(pages.Albums))
	registrar.RegisterPage("album/{id}", http.HandlerFunc(pages.AlbumView))
	registrar.RegisterPage("photo/{id}", http.HandlerFunc(pages.PhotoView))
	registrar.RegisterPage("people", http.HandlerFunc(pages.People))
	registrar.RegisterPage("person/{id}", http.HandlerFunc(pages.PersonView))
	registrar.RegisterPage("tags", http.HandlerFunc(pages.Tags))
	registrar.RegisterPage("videos", http.HandlerFunc(pages.Videos))
	registrar.RegisterPage("settings", http.HandlerFunc(pages.Settings))

	registrar.RegisterAPI("api/timeline", http.HandlerFunc(pages.TimelineAPI))
	registrar.RegisterAPI("api/media/{id}", http.HandlerFunc(pages.MediaDetailAPI))
	registrar.RegisterAPI("api/media/{id}/thumbnail", http.HandlerFunc(pages.MediaThumbnail))

	registrar.RegisterAction("POST", "albums", http.HandlerFunc(pages.CreateAlbum))
	registrar.RegisterAction("POST", "albums/{id}/add", http.HandlerFunc(pages.AddToAlbum))
	registrar.RegisterAction("DELETE", "albums/{id}/media/{media_id}", http.HandlerFunc(pages.RemoveFromAlbum))
	registrar.RegisterAction("DELETE", "albums/{id}", http.HandlerFunc(pages.DeleteAlbum))
	registrar.RegisterAction("POST", "people/{id}/name", http.HandlerFunc(pages.NamePerson))
	registrar.RegisterAction("POST", "people/{id}/merge", http.HandlerFunc(pages.MergePeople))
	registrar.RegisterAction("POST", "people/{id}/hide", http.HandlerFunc(pages.HidePerson))
	registrar.RegisterAction("POST", "settings/scan", http.HandlerFunc(pages.TriggerScan))
	registrar.RegisterAction("POST", "settings/recognition", http.HandlerFunc(pages.SaveRecognitionSettings))
	registrar.RegisterAction("POST", "rename/preview", http.HandlerFunc(pages.RenamePreview))
	registrar.RegisterAction("POST", "rename/apply", http.HandlerFunc(pages.RenameApply))
	registrar.RegisterAction("POST", "rename/undo", http.HandlerFunc(pages.RenameUndo))

	// Memories API compatibility
	memoriesAPI := NewMemoriesAPI(p)
	registrar.RegisterRoute("GET /apps/memories/api/{path...}", memoriesAPI)

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
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := cogplugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate gallery db for %s: %w", username, err)
	}
	return db, nil
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	media, _ := RecentMedia(db, 8)
	return recentPhotosWidget(media).Render(ctx, w)
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
			p.scanAllUsers(ctx)
		case <-ticker.C:
			p.scanAllUsers(ctx)
		}
	}
}
