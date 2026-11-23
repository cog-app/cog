package docs

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed layout.html
var layoutSrc string

var layout = template.Must(template.New("layout").Funcs(template.FuncMap{
	"item": func(root, path, label, current string) map[string]any {
		return map[string]any{"URL": root + path, "Label": label, "Active": path == current}
	},
}).Parse(layoutSrc))

// Assets are the URLs of what a website page loads.
type Assets struct {
	CSS, JS, SearchJS, Favicon string
}

// StaticFiles are the ui/static files the website copies, for its Assets.
var StaticFiles = []string{"style.css", "cog.js", "docs-search.js", "favicon.svg"}

type sidebarView struct {
	Root, Current string
	Sections      []sidebarSection
}

type sidebarSection struct {
	Label string
	Open  bool
	Pages []*Page
}

func (d *Docs) sidebar(root string, current *Page, admin bool) sidebarView {
	v := sidebarView{Root: root, Current: current.Path}
	for _, s := range d.Nav {
		if s.Dir == AdminSection && !admin && current.dir != AdminSection {
			continue
		}
		v.Sections = append(v.Sections, sidebarSection{Label: s.Label, Open: s.Dir == current.dir, Pages: s.Pages})
	}
	return v
}

// Sidebar is the docs' sidebar on page p, its links under root ("/docs/").
// The section p is in is open, the rest collapsed; the administrators'
// section is left out unless admin, or p is in it.
func (d *Docs) Sidebar(root string, p *Page, admin bool) (template.HTML, error) {
	var buf bytes.Buffer
	if err := layout.ExecuteTemplate(&buf, "sidebar", d.sidebar(root, p, admin)); err != nil {
		return "", fmt.Errorf("docs: sidebar for %s: %w", p.Path, err)
	}
	return template.HTML(buf.String()), nil
}

// Render writes page p as the website has it.
func (d *Docs) Render(w io.Writer, p *Page, a Assets) error {
	// Links between pages are relative, so the site works wherever it is
	// put.
	root := strings.Repeat("../", strings.Count(p.Path, "/"))
	err := layout.Execute(w, map[string]any{
		"Docs": d, "Page": p, "Root": root, "Assets": a, "Sidebar": d.sidebar(root, p, true),
	})
	if err != nil {
		return fmt.Errorf("docs: render %s: %w", p.Path, err)
	}
	return nil
}

// Write builds the website in dir, which it replaces: every page,
// search.json, and static (Cog's ui/static) StaticFiles under static/.
func (d *Docs) Write(dir string, static fs.FS) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	write := func(name string, b []byte) error {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, b, 0o644)
	}
	for _, name := range StaticFiles {
		b, err := fs.ReadFile(static, name)
		if err != nil {
			return fmt.Errorf("docs: %w", err)
		}
		if err := write("static/"+name, b); err != nil {
			return err
		}
	}
	if err := write("search.json", d.index); err != nil {
		return err
	}
	for _, p := range d.Pages {
		root := strings.Repeat("../", strings.Count(p.Path, "/"))
		a := Assets{
			CSS:      root + "static/style.css",
			JS:       root + "static/cog.js",
			SearchJS: root + "static/docs-search.js",
			Favicon:  root + "static/favicon.svg",
		}
		var buf bytes.Buffer
		if err := d.Render(&buf, p, a); err != nil {
			return err
		}
		if err := write(p.Path, buf.Bytes()); err != nil {
			return err
		}
	}
	return nil
}
