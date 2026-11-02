// Docs tool generates a static HTML site from the DOCS/ directory.
// Usage: go tool docs [-out dir] [-serve addr]
package main

import (
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		highlighting.NewHighlighting(
			highlighting.WithStyle("monokai"),
		),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
	),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),
	),
)

// section defines a navigation section with its display name and directory.
type section struct {
	Dir   string
	Label string
}

var sections = []section{
	{"getting-started", "Getting Started"},
	{"admin", "Administration"},
	{"using-cog", "Using Cog"},
	{"apps", "Apps"},
	{"nextcloud", "NextCloud"},
	{"plugins", "Plugin Development"},
}

// page holds metadata for a single documentation page.
type page struct {
	// RelPath is the path relative to DOCS/ (e.g. "admin/cli.md").
	RelPath string
	// HTMLPath is the output path relative to output dir (e.g. "admin/cli.html").
	HTMLPath string
	// Title extracted from the first # heading.
	Title string
	// Section directory (e.g. "admin"), empty for root pages.
	Section string
	// Body is the rendered HTML content.
	Body template.HTML
}

// navSection is a section in the sidebar navigation.
type navSection struct {
	Label string
	Pages []page
}

func main() {
	outDir := flag.String("out", "public", "output directory")
	serve := flag.String("serve", "", "serve the built site on this address (e.g. :8080)")
	docsDir := flag.String("docs", "DOCS", "documentation source directory")
	flag.Parse()

	pages, err := loadPages(*docsDir)
	if err != nil {
		log.Fatal(err)
	}

	nav := buildNav(pages)

	// Clean output directory to remove stale files.
	if err := os.RemoveAll(*outDir); err != nil {
		log.Fatal(err)
	}

	if err := renderAll(pages, nav, *outDir); err != nil {
		log.Fatal(err)
	}

	log.Printf("Built %d pages in %s/", len(pages), *outDir)

	if *serve != "" {
		log.Printf("Serving on http://localhost%s", *serve)
		log.Fatal(http.ListenAndServe(*serve, http.FileServer(http.Dir(*outDir))))
	}
}

func loadPages(docsDir string) ([]page, error) {
	var pages []page
	err := filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, _ := filepath.Rel(docsDir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		title := extractTitle(data)
		htmlPath := strings.TrimSuffix(rel, ".md") + ".html"
		if rel == "index.md" {
			htmlPath = "index.html"
		}

		var buf bytes.Buffer
		if err := md.Convert(data, &buf); err != nil {
			return fmt.Errorf("convert %s: %w", rel, err)
		}

		body := rewriteLinks(buf.String())

		sec := ""
		if dir := filepath.Dir(rel); dir != "." {
			sec = dir
		}

		pages = append(pages, page{
			RelPath:  rel,
			HTMLPath: htmlPath,
			Title:    title,
			Section:  sec,
			Body:     template.HTML(body),
		})
		return nil
	})
	return pages, err
}

var titleRe = regexp.MustCompile(`(?m)^#\s+(.+)$`)

func extractTitle(data []byte) string {
	m := titleRe.Find(data)
	if m == nil {
		return "Untitled"
	}
	return strings.TrimPrefix(strings.TrimSpace(string(m)), "# ")
}

var linkRe = regexp.MustCompile(`(href=")([^"]*\.md)(")`)

func rewriteLinks(html string) string {
	return linkRe.ReplaceAllStringFunc(html, func(match string) string {
		parts := linkRe.FindStringSubmatch(match)
		href := parts[2]
		href = strings.TrimSuffix(href, ".md") + ".html"
		return parts[1] + href + parts[3]
	})
}

func buildNav(pages []page) []navSection {
	// Index page is not in a section.
	bySection := make(map[string][]page)
	for _, p := range pages {
		if p.Section != "" {
			bySection[p.Section] = append(bySection[p.Section], p)
		}
	}
	// Sort pages within each section alphabetically by title.
	for k := range bySection {
		slices.SortFunc(bySection[k], func(a, b page) int {
			return strings.Compare(a.Title, b.Title)
		})
	}

	var nav []navSection
	for _, s := range sections {
		if ps, ok := bySection[s.Dir]; ok {
			nav = append(nav, navSection{Label: s.Label, Pages: ps})
		}
	}
	return nav
}

//go:embed page.html
var pageTmpl string

func renderAll(pages []page, nav []navSection, outDir string) error {
	tmpl, err := template.New("page").Parse(pageTmpl)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	for _, p := range pages {
		outPath := filepath.Join(outDir, p.HTMLPath)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return err
		}

		// Calculate relative path prefix for links (e.g. "../" for nested pages).
		depth := strings.Count(p.HTMLPath, "/")
		prefix := strings.Repeat("../", depth)

		var buf bytes.Buffer
		err := tmpl.Execute(&buf, map[string]any{
			"Title":   p.Title,
			"Body":    p.Body,
			"Nav":     nav,
			"Current": p.HTMLPath,
			"Prefix":  prefix,
		})
		if err != nil {
			return fmt.Errorf("render %s: %w", p.RelPath, err)
		}
		if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}
