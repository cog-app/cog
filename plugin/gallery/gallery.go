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
	"git.duckfam.us/jj-software/cog/plugin/files"
)

//go:embed sql/schema.sql
var sqlFS embed.FS

const PluginID = "gallery"

// Plugin is the gallery plugin.
type Plugin struct {
	ctx      plugin.PluginContext
	files    *files.Plugin
	geocoder *Geocoder
	cancel   context.CancelFunc
	done     chan struct{}
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

	// Obtain the concrete files plugin for operations not on FilesAccess
	// (DB, Rename, Preview, Upload).
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		if fp, found := priv.PluginByID("files"); found {
			p.files = fp.(*files.Plugin)
		}
		priv.RegisterCommand(plugin.CommandDef{
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

	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Timeline))
	ctx.RegisterPage("albums", http.HandlerFunc(pages.Albums))
	ctx.RegisterPage("album/{id}", http.HandlerFunc(pages.AlbumView))
	ctx.RegisterPage("photo/{id}", http.HandlerFunc(pages.PhotoView))
	ctx.RegisterPage("people", http.HandlerFunc(pages.People))
	ctx.RegisterPage("person/{id}", http.HandlerFunc(pages.PersonView))
	ctx.RegisterPage("tags", http.HandlerFunc(pages.Tags))
	ctx.RegisterPage("videos", http.HandlerFunc(pages.Videos))
	ctx.RegisterPage("settings", http.HandlerFunc(pages.Settings))

	ctx.RegisterAPI("api/timeline", http.HandlerFunc(pages.TimelineAPI))
	ctx.RegisterAPI("api/media/{id}", http.HandlerFunc(pages.MediaDetailAPI))
	ctx.RegisterAPI("api/media/{id}/thumbnail", http.HandlerFunc(pages.MediaThumbnail))

	ctx.RegisterAction("POST", "albums", http.HandlerFunc(pages.CreateAlbum))
	ctx.RegisterAction("POST", "albums/{id}/add", http.HandlerFunc(pages.AddToAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}/media/{media_id}", http.HandlerFunc(pages.RemoveFromAlbum))
	ctx.RegisterAction("DELETE", "albums/{id}", http.HandlerFunc(pages.DeleteAlbum))
	ctx.RegisterAction("POST", "people/{id}/name", http.HandlerFunc(pages.NamePerson))
	ctx.RegisterAction("POST", "people/{id}/merge", http.HandlerFunc(pages.MergePeople))
	ctx.RegisterAction("POST", "people/{id}/hide", http.HandlerFunc(pages.HidePerson))
	ctx.RegisterAction("POST", "settings/scan", http.HandlerFunc(pages.TriggerScan))
	ctx.RegisterAction("POST", "settings/recognition", http.HandlerFunc(pages.SaveRecognitionSettings))
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
