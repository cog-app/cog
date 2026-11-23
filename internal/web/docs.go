package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	cogdocs "git.duckfam.us/jj-software/cog/DOCS"
	"git.duckfam.us/jj-software/cog/internal/docs"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/version"
	"git.duckfam.us/jj-software/cog/ui"
)

// liveDocs is the documentation as this Cog serves it, and its search
// index: one for everyone, in memory, since the docs are in the binary.
type liveDocs struct {
	docs  *docs.Docs
	index *search.Index
}

var appDocs = sync.OnceValues(func() (*liveDocs, error) {
	d, err := docs.Load(cogdocs.FS, docs.Data{Version: version.Get()})
	if err != nil {
		return nil, err
	}
	idx, err := search.OpenMemory()
	if err != nil {
		return nil, fmt.Errorf("docs: %w", err)
	}
	if err := idx.Batch(d.Documents(), nil); err != nil {
		return nil, fmt.Errorf("docs: index: %w", err)
	}
	return &liveDocs{docs: d, index: idx}, nil
})

type docsHandler struct{ handler }

// Serve handles GET /docs/...: the documentation in Cog's layout, readable
// signed out; with ?part=id, only the page's part under that heading.
func (h docsHandler) Serve(w http.ResponseWriter, r *http.Request) {
	ld, err := appDocs()
	if err != nil {
		h.p.Log.Error("docs", "err", err)
		httperr.Internal(w, r, err)
		return
	}
	p := ld.docs.Page(strings.TrimPrefix(r.URL.Path, "/docs/"))
	if p == nil {
		http.NotFound(w, r)
		return
	}
	if q := r.URL.Query(); q.Has("part") {
		h.part(w, r, p, q.Get("part"))
		return
	}
	pd := pageDataWithNav(r, p.Title, h.p.CSRFSecret, h.p.Plugins)
	sidebar, err := ld.docs.Sidebar("/docs/", p, pd.IsAdmin)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := ui.DocsPage(pd, string(sidebar), string(p.Body), ld.docs.Data.Version).Render(r.Context(), w); err != nil {
		httperr.Log(r, err)
	}
}

// part writes the part of page p under heading id ("" for its
// introduction) on its own: what a Help icon's popover shows.
func (h docsHandler) part(w http.ResponseWriter, r *http.Request, p *docs.Page, id string) {
	html, ok := p.Part(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := io.WriteString(w, string(html)); err != nil {
		httperr.Log(r, err)
	}
}

// docsSearch is the help pages matching q, at most limit.
func (h handler) docsSearch(q string, limit int) []search.Hit {
	if limit <= 0 {
		return nil
	}
	ld, err := appDocs()
	if err != nil {
		h.p.Log.Error("search: docs", "err", err)
		return nil
	}
	res, err := ld.index.Search(q, limit, 0)
	if err != nil {
		h.p.Log.Error("search: docs", "err", err)
		return nil
	}
	return res.Hits
}
