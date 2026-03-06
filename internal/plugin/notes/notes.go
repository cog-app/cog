// Package notes implements a markdown notes plugin with NextCloud Notes API compatibility.
package notes

//go:generate go tool templ generate -log-level warn

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

//go:generate go tool migratesql .

//go:embed sql/schema.sql
var sqlFS embed.FS

const PluginID = "notes"

type dbProvider interface {
	PluginDB(username, pluginID string) (*sql.DB, error)
	UserFilesDir(username string) string
	EnsureUserDirs(username string) error
	NavItems() []plugin.NavItem
}

// Plugin is the notes plugin.
type Plugin struct {
	registry dbProvider
	secret   []byte
}

// New creates a new Notes plugin instance.
func New(registry dbProvider, secret []byte) *Plugin {
	return &Plugin{registry: registry, secret: secret}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(registrar plugin.Registrar) error {
	registrar.RegisterNavItem(plugin.NavItem{Label: "Notes", URL: "/apps/notes", Icon: "notes"})
	registrar.RegisterWidget(plugin.Widget{
		Title:    "Recent Notes",
		Priority: 30,
		Render:   p.renderWidget,
	})
	registrar.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "notes",
		EnrichSQL:    "SELECT rowid, filename FROM notes WHERE rowid IN (?)",
		Fields: []plugin.SearchField{
			{Column: "filename", Field: "name"},
		},
		ContentFunc: func(filesDir string, cols map[string]string) string {
			data, err := os.ReadFile(filepath.Join(filesDir, "Notes", cols["filename"]))
			if err != nil {
				return ""
			}
			return StripMarkdown(string(data))
		},
	})

	// Web UI routes
	pages := NewPages(p, p.secret, p.registry)
	registrar.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	registrar.RegisterPage("category/{path...}", http.HandlerFunc(pages.CategoryView))
	registrar.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	registrar.RegisterPage("note/{id}", http.HandlerFunc(pages.NoteView))
	registrar.RegisterPage("note/{id}/edit", http.HandlerFunc(pages.NoteEdit))
	registrar.RegisterPage("new", http.HandlerFunc(pages.NewNote))
	registrar.RegisterAction("POST", "notes", http.HandlerFunc(pages.CreateNote))
	registrar.RegisterAction("POST", "notes/{id}", http.HandlerFunc(pages.UpdateNote))
	registrar.RegisterAction("DELETE", "notes/{id}", http.HandlerFunc(pages.DeleteNote))
	registrar.RegisterAction("POST", "notes/{id}/star", http.HandlerFunc(pages.ToggleStar))

	// NextCloud Notes API compatibility
	notesAPI := NewNotesAPI(p)
	registrar.RegisterRoute("/index.php/apps/notes/api/", notesAPI)

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the notes plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.registry.PluginDB(username, PluginID)
	if err != nil {
		return nil, err
	}
	if err := cogplugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate notes db for %s: %w", username, err)
	}
	return db, nil
}

// NotesDir returns the path to the user's Notes directory on disk.
func (p *Plugin) NotesDir(username string) string {
	return filepath.Join(p.registry.UserFilesDir(username), "Notes")
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	notes, _ := RecentNotes(db, 5)
	return recentNotesWidget(notes).Render(ctx, w)
}
