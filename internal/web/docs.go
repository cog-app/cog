package web

import (
	"fmt"
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
// signed out.
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
