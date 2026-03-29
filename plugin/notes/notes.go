// Package notes implements a markdown notes plugin with NextCloud Notes API compatibility.
package notes

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"
)

//go:generate go tool migratesql .

//go:embed sql
var sqlFS embed.FS

//go:embed notes_folder.svg
var notesFolderIcon string

const PluginID = "notes"

// Plugin is the notes plugin.
type Plugin struct {
	ctx   plugin.PluginContext
	log   *slog.Logger
	files *files.Plugin
}

// New creates a new Notes plugin instance.
func New() *Plugin {
	return &Plugin{}
}

func (p *Plugin) ID() string { return PluginID }

func (p *Plugin) Register(ctx plugin.PluginContext) error {
	p.ctx = ctx
	p.log = ctx.Logger()

	ctx.RegisterNavItem(plugin.NavItem{Label: "Notes", URL: "/apps/notes", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Notes",
		Priority: 30,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "notes",
		EnrichSQL:    "SELECT rowid, filename FROM notes WHERE rowid IN (?)",
		ContentFunc: func(filesDir string, cols map[string]string) string {
			data, err := os.ReadFile(filepath.Join(filesDir, "Notes", cols["filename"]))
			if err != nil {
				return ""
			}
			return StripMarkdown(string(data))
		},
	})
	ctx.RegisterResource(plugin.ResourceDef{
		Type:     "note",
		Table:    "notes",
		IDColumn: "id",
	})

	// Web UI routes
	pages := NewPages(p)
	ctx.RegisterPage("{$}", http.HandlerFunc(pages.Index))
	ctx.RegisterPage("category/{path...}", http.HandlerFunc(pages.CategoryView))
	ctx.RegisterPage("starred", http.HandlerFunc(pages.Starred))
	ctx.RegisterPage("note/{id}", http.HandlerFunc(pages.NoteView))
	ctx.RegisterPage("note/{id}/edit", http.HandlerFunc(pages.NoteEdit))
	ctx.RegisterPage("new", http.HandlerFunc(pages.NewNote))
	ctx.RegisterAction("POST", "notes", http.HandlerFunc(pages.CreateNote))
	ctx.RegisterAction("POST", "notes/{id}", http.HandlerFunc(pages.UpdateNote))
	ctx.RegisterAction("DELETE", "notes/{id}", http.HandlerFunc(pages.DeleteNote))
	ctx.RegisterAction("POST", "notes/{id}/star", http.HandlerFunc(pages.ToggleStar))

	// NextCloud Notes API compatibility + cross-plugin access
	notesAPI := NewNotesAPI(p)
	if priv, ok := ctx.(plugin.PrivilegedContext); ok {
		priv.RegisterRoute("/index.php/apps/notes/api/", notesAPI)
		if fp, found := priv.PluginByID("files"); found {
			p.files = fp.(*files.Plugin)
		}
	}

	return nil
}

func (p *Plugin) Start(ctx context.Context) error { return nil }
func (p *Plugin) Stop() error                     { return nil }

// DB returns the notes plugin database for a user, running migrations if needed.
func (p *Plugin) DB(username string) (*sql.DB, error) {
	db, err := p.ctx.PluginDB(username)
	if err != nil {
		return nil, err
	}
	if err := plugin.Migrate(db, sqlFS); err != nil {
		return nil, fmt.Errorf("migrate notes db for %s: %w", username, err)
	}
	return db, nil
}

// NotesDir returns the path to the user's Notes directory on disk.
func (p *Plugin) NotesDir(username string) string {
	return filepath.Join(p.ctx.UserFilesDir(username), "Notes")
}

// userFS returns the VFS for a user, or nil if unavailable.
func (p *Plugin) userFS(username string) vfs.FS {
	return p.ctx.UserVFS(username)
}

// notesPath returns the VFS path for a notes file.
func notesPath(filename string) string {
	return "/Notes/" + filename
}

// ensureNotesDirFor ensures the Notes directory exists for a user.
func (p *Plugin) ensureNotesDirFor(username string) error {
	if fs := p.userFS(username); fs != nil {
		return fs.Mkdir("/Notes")
	}
	return EnsureNotesDir(p.NotesDir(username))
}

// readNoteContent reads a note file's content for a user.
func (p *Plugin) readNoteContent(username, filename string) (string, error) {
	if fs := p.userFS(username); fs != nil {
		data, err := fs.ReadFile(notesPath(filename))
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return ReadContent(p.NotesDir(username), filename)
}

// writeNoteContent writes content to a note file for a user.
func (p *Plugin) writeNoteContent(username, filename, content string) error {
	if fs := p.userFS(username); fs != nil {
		return fs.Put(notesPath(filename), strings.NewReader(content))
	}
	return WriteContent(p.NotesDir(username), filename, content)
}

// deleteNoteFile deletes a note file for a user.
func (p *Plugin) deleteNoteFile(username, filename string) error {
	if fs := p.userFS(username); fs != nil {
		return fs.Delete(notesPath(filename))
	}
	return DeleteFile(p.NotesDir(username), filename)
}

// renameNoteFile renames a note file for a user.
func (p *Plugin) renameNoteFile(username, oldName, newName string) error {
	if fs := p.userFS(username); fs != nil {
		return fs.Rename(notesPath(oldName), notesPath(newName))
	}
	return RenameFile(p.NotesDir(username), oldName, newName)
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return nil
	}
	notes, _ := RecentNotes(db, 5)
	return recentNotesWidget(notes).Render(ctx, w)
}

// FolderIcon implements plugin.FolderIconProvider.
func (p *Plugin) FolderIcon(_, folderPath string) (string, bool) {
	if folderPath == "Notes" || folderPath == "Notes/" {
		return notesFolderIcon, true
	}
	return "", false
}
