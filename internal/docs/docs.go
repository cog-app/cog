// Package docs renders Cog's documentation (the DOCS package's Markdown):
// served at /docs/ by every Cog, and built into the static website by
// `cog docs`. Both use one layout and one search, so the website is the
// docs a Cog serves, minus what only makes sense in one place.
package docs

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
	texttemplate "text/template"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// SiteURL is where the website is published.
const SiteURL = "https://cog-app.github.io/"

// Data is what a page's template sees.
type Data struct {
	// Site is true when building the website, false in a running Cog.
	Site bool
	// Version is the Cog version the docs are for.
	Version string
}

// ErrSkip, returned by a page's {{ skip }}, leaves the page out.
var ErrSkip = errors.New("skip this page")

func skip() (string, error) { return "", ErrSkip }

// Page is one rendered page.
type Page struct {
	// Path is the page's URL relative to the docs root: "admin/https.html".
	Path string
	// Source is the Markdown file: "admin/https.md".
	Source  string
	Title   string
	Section string // the section's label, "" for the overview
	dir     string // the section's directory
	Body    template.HTML

	headings []string
	text     string
	// sections are the page's parts by heading ID, "" for its
	// introduction: what Help popovers show.
	sections map[string]template.HTML
}

// Section is a sidebar group of pages.
type Section struct {
	Dir, Label string
	Pages      []*Page
}

// AdminSection is the section only administrators need.
const AdminSection = "admin"

// sections are the sidebar's groups, in order: a directory of DOCS each.
var sections = []struct{ Dir, Label string }{
	{"getting-started", "Getting started"},
	{"using-cog", "Using Cog"},
	{"apps", "Apps"},
	{"admin", "Administration"},
	{"nextcloud", "NextCloud"},
	{"plugins", "Plugin development"},
}

// Docs is the rendered documentation.
type Docs struct {
	Data  Data
	Pages []*Page // in sidebar order, the overview first
	Nav   []Section

	byPath map[string]*Page
	index  []byte // search.json
}

var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(highlighting.WithStyle("github")),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// Load renders every page of fsys (the DOCS package's FS) for data.
func Load(fsys fs.FS, data Data) (*Docs, error) {
	// Each page's template first, so links know which pages are left out.
	srcs := map[string][]byte{}
	skipped := map[string]bool{}
	err := fs.WalkDir(fsys, ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || path.Ext(name) != ".md" {
			return err
		}
		src, err := execute(fsys, name, data)
		if errors.Is(err, ErrSkip) {
			skipped[name] = true
			return nil
		}
		srcs[name] = src
		return err
	})
	if err != nil {
		return nil, err
	}
	if srcs["index.md"] == nil {
		return nil, errors.New("docs: no index.md")
	}
	// A link to a page left out of Cog goes to the website; a link to no
	// page at all is a mistake.
	link := func(target string) (string, error) {
		switch {
		case srcs[target] != nil:
			return "", nil
		case skipped[target] && !data.Site:
			return SiteURL, nil
		}
		return "", fmt.Errorf("links to %s, which does not exist", target)
	}
	d := &Docs{Data: data, byPath: map[string]*Page{}}
	for name, src := range srcs {
		p, err := render(name, src, link)
		if err != nil {
			return nil, fmt.Errorf("docs: %s: %w", name, err)
		}
		d.Pages = append(d.Pages, p)
		d.byPath[name] = p
	}
	d.order()
	d.index, err = d.searchIndex()
	if err != nil {
		return nil, err
	}
	return d, nil
}

// execute runs page name's template: its Markdown for data.
func execute(fsys fs.FS, name string, data Data) ([]byte, error) {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, err
	}
	t, err := texttemplate.New(name).Funcs(texttemplate.FuncMap{"skip": skip}).Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("docs: %w", err)
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		if errors.Is(err, ErrSkip) {
			return nil, ErrSkip
		}
		return nil, fmt.Errorf("docs: %w", err)
	}
	return out.Bytes(), nil
}

// render makes page name from its Markdown. link says where a link to
// another page goes: "" for the page here, else the base URL of the page's
// copy elsewhere.
func render(name string, source []byte, link func(target string) (string, error)) (*Page, error) {
	doc := md.Parser().Parse(text.NewReader(source))
	p := &Page{Source: name, Path: strings.TrimSuffix(name, ".md") + ".html"}
	if dir := path.Dir(name); dir != "." {
		for _, s := range sections {
			if s.Dir == dir {
				p.Section, p.dir = s.Label, s.Dir
			}
		}
		if p.Section == "" {
			return nil, errors.New("in no section")
		}
	}
	var words strings.Builder
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			words.WriteByte(' ')
			h := plain(n, source)
			if n.Level == 1 && p.Title == "" {
				p.Title = h
			} else {
				p.headings = append(p.headings, h)
			}
		case *ast.Link:
			target, ok, err := pageLink(name, string(n.Destination))
			if err != nil || !ok {
				return ast.WalkContinue, err
			}
			base, err := link(target)
			if err != nil {
				return ast.WalkStop, err
			}
			dest := rewriteLink(string(n.Destination))
			if base != "" {
				_, frag, _ := strings.Cut(dest, "#")
				dest = base + rewriteLink(target)
				if frag != "" {
					dest += "#" + frag
				}
			}
			n.Destination = []byte(dest)
		case *ast.Text:
			words.Write(n.Segment.Value(source))
			if n.SoftLineBreak() || n.HardLineBreak() {
				words.WriteByte(' ')
			}
		case *ast.Paragraph, *ast.ListItem, *east.TableCell:
			words.WriteByte(' ')
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}
	if p.Title == "" {
		return nil, errors.New("no # title")
	}
	p.text = strings.Join(strings.Fields(words.String()), " ")
	// Each top-level block on its own, so a heading's section is the
	// blocks from it to the next heading as high.
	type block struct {
		level int // a heading's, 0 for any other block
		id    string
		html  string
	}
	var blocks []block
	var body strings.Builder
	for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
		var b bytes.Buffer
		if err := md.Renderer().Render(&b, source, c); err != nil {
			return nil, fmt.Errorf("render: %w", err)
		}
		bl := block{html: b.String()}
		if h, ok := c.(*ast.Heading); ok {
			bl.level = h.Level
			if id, ok := h.AttributeString("id"); ok {
				bl.id = string(id.([]byte))
			}
		}
		blocks = append(blocks, bl)
		body.WriteString(bl.html)
	}
	p.Body = template.HTML(body.String())
	join := func(bs []block) template.HTML {
		var b strings.Builder
		for _, bl := range bs {
			b.WriteString(bl.html)
		}
		return template.HTML(b.String())
	}
	p.sections = map[string]template.HTML{}
	intro := slices.IndexFunc(blocks, func(b block) bool { return b.level > 1 })
	if intro < 0 {
		intro = len(blocks)
	}
	p.sections[""] = join(blocks[:intro])
	for i, bl := range blocks {
		if bl.id == "" || bl.level == 1 {
			continue
		}
		end := len(blocks)
		for j := i + 1; j < len(blocks); j++ {
			if blocks[j].level > 0 && blocks[j].level <= bl.level {
				end = j
				break
			}
		}
		p.sections[bl.id] = join(blocks[i:end])
	}
	return p, nil
}

// Part is the part of the page under the heading with ID id, to the
// next heading as high; for "", the page's introduction. ok is false if
// the page has no such heading.
func (p *Page) Part(id string) (html template.HTML, ok bool) {
	html, ok = p.sections[id]
	return html, ok
}

// pageLink is the page a link in page from points at, if it points at one
// (a relative link to a .md file).
func pageLink(from, dest string) (string, bool, error) {
	u, err := url.Parse(dest)
	if err != nil {
		return "", false, fmt.Errorf("link %q: %w", dest, err)
	}
	if u.Scheme != "" || u.Host != "" || path.Ext(u.Path) != ".md" {
		return "", false, nil
	}
	if strings.HasPrefix(u.Path, "/") {
		return "", false, fmt.Errorf("link %q: link to pages relatively", dest)
	}
	target := path.Join(path.Dir(from), u.Path)
	if strings.HasPrefix(target, "../") {
		return "", false, fmt.Errorf("link %q: outside the docs", dest)
	}
	return target, true, nil
}

// rewriteLink points a link at a .md page to its .html.
func rewriteLink(dest string) string {
	p, frag, _ := strings.Cut(dest, "#")
	p = strings.TrimSuffix(p, ".md") + ".html"
	if frag != "" {
		p += "#" + frag
	}
	return p
}

// plain is n's text without its markup.
func plain(n ast.Node, source []byte) string {
	var b strings.Builder
	// The walker returns no errors, so Walk has none to return.
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(source))
		case *ast.String:
			b.Write(c.Value)
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// order puts the pages in sidebar order and builds the sidebar: the
// overview, then each section's pages by title.
func (d *Docs) order() {
	rank := func(p *Page) int {
		if p.Section == "" {
			return -1
		}
		return slices.IndexFunc(sections, func(s struct{ Dir, Label string }) bool { return s.Label == p.Section })
	}
	slices.SortFunc(d.Pages, func(a, b *Page) int {
		if a.Source == "index.md" {
			return -1
		}
		if b.Source == "index.md" {
			return 1
		}
		return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(a.Title, b.Title))
	})
	d.Nav = nil
	for _, p := range d.Pages {
		if p.Section == "" {
			continue
		}
		if len(d.Nav) == 0 || d.Nav[len(d.Nav)-1].Label != p.Section {
			d.Nav = append(d.Nav, Section{Dir: p.dir, Label: p.Section})
		}
		d.Nav[len(d.Nav)-1].Pages = append(d.Nav[len(d.Nav)-1].Pages, p)
	}
}

// Page is the page at p ("admin/https.html"; "" or "index.html" for the
// overview), nil if there is none.
func (d *Docs) Page(p string) *Page {
	if p == "" {
		p = "index.html"
	}
	src, ok := strings.CutSuffix(p, ".html")
	if !ok {
		return nil
	}
	return d.byPath[src+".md"]
}

// Index is the search index the pages' search box loads (search.json).
func (d *Docs) Index() []byte { return d.index }
