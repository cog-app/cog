// Package notes implements a markdown notes plugin with NextCloud Notes API compatibility.
package notes

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
	"path"
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

	ctx.RegisterNavItem(plugin.NavItem{Label: "Notes", URL: "/apps/notes", Icon: `<svg class="nav-icon" viewBox="0 0 24 24"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"></path><polyline points="14 2 14 8 20 8"></polyline><line x1="16" y1="13" x2="8" y2="13"></line><line x1="16" y1="17" x2="8" y2="17"></line></svg>`})
	ctx.RegisterWidget(plugin.Widget{
		Title:    "Recent Notes",
		URL:      "/apps/notes/",
		Priority: 30,
		Render:   p.renderWidget,
	})
	ctx.RegisterSearchSource(plugin.SearchSourceDef{
		PrimaryTable: "notes",
		EnrichSQL:    "SELECT rowid, filename FROM notes WHERE rowid IN (?)",
		ContentFunc: func(files vfs.FS, cols map[string]string) string {
			data, err := files.ReadFile(path.Join("/Notes", cols["filename"]))
			if err != nil {
				// Gone since the change was queued: nothing to index.
				if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, vfs.ErrNotFound) {
					p.log.Error("index note content", "file", cols["filename"], "err", err)
				}
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
	ctx.RegisterAction("POST", "preview", http.HandlerFunc(pages.Preview))

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

// userFS returns the VFS for a user.
func (p *Plugin) userFS(username string) (vfs.FS, error) {
	return p.ctx.UserVFS(username)
}

// notesPath returns the VFS path for a notes file.
func notesPath(filename string) string {
	return "/Notes/" + filename
}

// ensureNotesDirFor ensures the Notes directory exists for a user.
func (p *Plugin) ensureNotesDirFor(username string) error {
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	return fs.Mkdir("/Notes")
}

// readNoteContent reads a note file's content for a user.
func (p *Plugin) readNoteContent(username, filename string) (string, error) {
	fs, err := p.userFS(username)
	if err != nil {
		return "", err
	}
	data, err := fs.ReadFile(notesPath(filename))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// writeNoteContent writes content to a note file for a user.
func (p *Plugin) writeNoteContent(username, filename, content string) error {
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	return fs.Put(notesPath(filename), strings.NewReader(content))
}

// deleteNoteFile deletes a note file for a user.
func (p *Plugin) deleteNoteFile(username, filename string) error {
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	return fs.Delete(notesPath(filename))
}

// deleteNote removes a note's file and its metadata. A file that is already
// gone (deleted through the files app) is not an error: the metadata is what
// still needs removing.
func (p *Plugin) deleteNote(db *sql.DB, username string, note *Note) error {
	if err := p.deleteNoteFile(username, note.Filename); err != nil && !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete %s: %w", note.Filename, err)
	}
	if err := DeleteNoteMeta(db, note.ID); err != nil {
		return fmt.Errorf("delete note %d: %w", note.ID, err)
	}
	return nil
}

// renameNoteFile renames a note file for a user.
func (p *Plugin) renameNoteFile(username, oldName, newName string) error {
	fs, err := p.userFS(username)
	if err != nil {
		return err
	}
	return fs.Rename(notesPath(oldName), notesPath(newName))
}

func (p *Plugin) renderWidget(ctx context.Context, w io.Writer, userID string) error {
	db, err := p.DB(userID)
	if err != nil {
		return fmt.Errorf("open notes database: %w", err)
	}
	notes, err := RecentNotes(db, 5)
	if err != nil {
		return fmt.Errorf("recent notes: %w", err)
	}
	return recentNotesWidget(notes).Render(ctx, w)
}

// FolderIcon implements plugin.FolderIconProvider.
func (p *Plugin) FolderIcon(_, folderPath string) (string, bool) {
	if folderPath == "Notes" || folderPath == "Notes/" {
		return notesFolderIcon, true
	}
	return "", false
}

// freeNoteFilename returns a filename for title that no other note uses,
// in the database or on disk: "Title.md", then "Title (2).md", and so on.
// Taking an existing name would overwrite that note's file. self is the
// note being renamed, whose own name is not a clash; nil for a new note.
func (p *Plugin) freeNoteFilename(db *sql.DB, username, title string, self *Note) (string, error) {
	base := strings.TrimSuffix(FilenameFromTitle(title), ".md")
	for n := 1; ; n++ {
		name := base + ".md"
		if n > 1 {
			name = fmt.Sprintf("%s (%d).md", base, n)
		}
		if self != nil && name == self.Filename {
			return name, nil
		}
		taken, err := p.noteNameTaken(db, username, name)
		if err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
	}
}

func (p *Plugin) noteNameTaken(db *sql.DB, username, filename string) (bool, error) {
	if _, err := GetNoteByFilename(db, filename); err == nil {
		return true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("look up %s: %w", filename, err)
	}
	vf, err := p.userFS(username)
	if err != nil {
		return false, err
	}
	_, err = vf.Stat(notesPath(filename))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, vfs.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s: %w", filename, err)
	}
}
