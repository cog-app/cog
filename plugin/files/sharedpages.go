package files

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
	"git.duckfam.us/jj-software/cog/vfs"
)

// sharedURL is where rel, inside what owner shared as name, is browsed.
func sharedURL(owner, name, rel string) string {
	u := "/apps/files/shared/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	if rel = path.Clean("/" + rel); rel != "/" {
		u += (&url.URL{Path: rel}).EscapedPath()
	}
	return u
}

// sharedItem finds the entry a /apps/files/shared/{owner}/{rest...} request
// names, and the path inside it. It answers the request itself (false) when
// there is none.
func (h *Pages) sharedItem(w http.ResponseWriter, r *http.Request) (*user.User, *SharedEntry, string, bool) {
	u := auth.UserFromContext(r.Context())
	if h.plugin.Shared == nil {
		http.NotFound(w, r)
		return nil, nil, "", false
	}
	name, rel, _ := strings.Cut(r.PathValue("rest"), "/")
	e, err := h.plugin.Shared.Lookup(u, r.PathValue("owner"), name)
	if errors.Is(err, ErrNotShared) {
		http.NotFound(w, r)
		return nil, nil, "", false
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return nil, nil, "", false
	}
	return u, e, path.Clean("/" + rel), true
}

// ServeShared lists what others shared with the user.
func (h *Pages) ServeShared(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var owners []SharedOwner
	if h.plugin.Shared != nil {
		var err error
		if owners, err = h.plugin.Shared.List(u); err != nil {
			httperr.Internal(w, r, err)
			return
		}
	}
	pd := h.pageData(w, r, "Shared with me")
	ui.Render(w, r, sharedPage(pd, owners))
}

// ServeSharedItem shows a shared folder, or a folder in it, or a volume, or
// downloads a shared file.
func (h *Pages) ServeSharedItem(w http.ResponseWriter, r *http.Request) {
	_, e, rel, ok := h.sharedItem(w, r)
	if !ok {
		return
	}
	switch e.Kind {
	case SharedVolume:
		if rel != "/" {
			http.NotFound(w, r)
			return
		}
		pd := h.pageData(w, r, e.Name)
		ui.Render(w, r, sharedVolumePage(pd, e))
		return
	case SharedFile:
		if rel != "/" {
			http.NotFound(w, r)
			return
		}
		h.serveDownload(w, r, e.Owner.Username, e.Path, e.File)
		return
	case SharedCollection:
		h.serveCollection(w, r, e, rel)
		return
	}

	owner := e.Owner.Username
	full := e.Within(rel)
	meta, err := h.plugin.GetByPath(owner, full)
	if errors.Is(err, ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if !meta.IsDir {
		if r.URL.Query().Get("dl") == "1" {
			h.serveDownload(w, r, owner, full, meta)
		} else {
			h.serveRaw(w, r, owner, full, meta)
		}
		return
	}
	items, err := h.plugin.List(owner, full)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	pd := h.pageData(w, r, e.Name)
	ui.Render(w, r, sharedFolderPage(pd, e, rel, items, h.plugin.ctx.Now()))
}

// ServeSharedPost changes what is shared, as the share allows, or mounts it
// in the user's files.
func (h *Pages) ServeSharedPost(w http.ResponseWriter, r *http.Request) {
	u, e, rel, ok := h.sharedItem(w, r)
	if !ok {
		return
	}
	back := sharedURL(e.Owner.Username, e.Name, rel)

	switch r.FormValue("action") {
	case "mount":
		m, err := h.plugin.Shared.MountFolder(u, e, r.FormValue("path"))
		if err != nil {
			h.mountError(w, r, err, back)
			return
		}
		h.flashRedirect(w, r, "success", "“"+e.Name+"” is now in your files at "+m.UserPath+".", fileURL(m.UserPath))
		return
	case "area":
		if e.Kind != SharedVolume {
			http.NotFound(w, r)
			return
		}
		m, err := h.plugin.Shared.AddArea(u, e.Volume, r.FormValue("path"))
		if err != nil {
			h.mountError(w, r, err, back)
			return
		}
		h.flashRedirect(w, r, "success", "Added storage on “"+e.Name+"” at "+m.UserPath+".", fileURL(m.UserPath))
		return
	}

	if e.Kind != SharedFolder {
		http.NotFound(w, r)
		return
	}
	if !e.Writable() {
		h.flashRedirect(w, r, "error", "“"+e.Name+"” is shared with you read-only.", back)
		return
	}
	owner := e.Owner.Username
	full := e.Within(rel)
	switch r.FormValue("action") {
	case "mkdir":
		name := path.Base(path.Clean("/" + r.FormValue("name")))
		if name == "/" || name == "." {
			h.flashRedirect(w, r, "error", "Give the folder a name.", back)
			return
		}
		if _, err := h.plugin.CreateDir(owner, path.Join(full, name)); err != nil {
			h.flashRedirect(w, r, "error", "The folder could not be created: "+err.Error(), back)
			return
		}
		h.flashRedirect(w, r, "success", "Created “"+name+"”.", back)
	case "delete":
		target := e.Within(path.Join(rel, path.Base(path.Clean("/"+r.FormValue("name")))))
		if target == e.Path {
			h.flashRedirect(w, r, "error", "The shared folder itself is not yours to delete.", back)
			return
		}
		if err := h.plugin.Trash(owner, target); err != nil {
			h.flashRedirect(w, r, "error", "It could not be deleted: "+err.Error(), back)
			return
		}
		h.flashRedirect(w, r, "success", "Deleted “"+path.Base(target)+"”.", back)
	default:
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			h.flashRedirect(w, r, "error", "The upload could not be read: "+err.Error(), back)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) == 0 {
			h.flashRedirect(w, r, "error", "Choose a file to upload.", back)
			return
		}
		for _, header := range files {
			name := path.Base(header.Filename)
			file, err := header.Open()
			if err != nil {
				h.flashRedirect(w, r, "error", "“"+name+"” could not be uploaded: "+err.Error(), back)
				return
			}
			_, uploadErr := h.plugin.Upload(owner, path.Join(full, name), file)
			if cerr := file.Close(); cerr != nil {
				h.plugin.log.Error("close upload", "err", cerr)
			}
			if uploadErr != nil {
				h.flashRedirect(w, r, "error", "“"+name+"” could not be uploaded: "+uploadErr.Error(), back)
				return
			}
		}
		h.flashRedirect(w, r, "success", fmt.Sprintf("Uploaded %d file(s).", len(files)), back)
	}
}

// mountError tells the user why a mount was refused, or logs what went wrong.
func (h *Pages) mountError(w http.ResponseWriter, r *http.Request, err error, back string) {
	if errors.Is(err, ErrMountPoint) || errors.Is(err, vfs.ErrReadOnly) {
		h.flashRedirect(w, r, "error", err.Error(), back)
		return
	}
	h.plugin.log.Error("mount shared", "err", err)
	h.flashRedirect(w, r, "error", "It could not be added to your files.", back)
}

// sharedCrumbs leads from Shared with me through the entry to the folder
// holding rel.
func sharedCrumbs(e *SharedEntry, rel string) []ui.Crumb {
	crumbs := []ui.Crumb{{Label: "Shared with me", URL: "/apps/files/shared"}}
	if rel == "/" {
		return crumbs
	}
	crumbs = append(crumbs, ui.Crumb{Label: e.Name, URL: sharedURL(e.Owner.Username, e.Name, "/")})
	for _, seg := range breadcrumbSegments(path.Dir(rel)) {
		crumbs = append(crumbs, ui.Crumb{Label: seg.Name, URL: sharedURL(e.Owner.Username, e.Name, seg.Path)})
	}
	return crumbs
}

// ownerLabel is how an owner is named on the page.
func ownerLabel(u *user.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// suggestedMount is where a folder or area goes by default: the top of the
// user's files, under its name.
func suggestedMount(e *SharedEntry) string {
	return "/" + e.Name
}

// sharedTitle is the heading of a shared folder's page.
func sharedTitle(e *SharedEntry, rel string) string {
	if rel == "/" {
		return e.Name
	}
	return path.Base(rel)
}

// sharedRel is where p, in the owner's files, is inside the shared folder.
func sharedRel(e *SharedEntry, p string) string {
	if e.Path == "/" {
		return p
	}
	return strings.TrimPrefix(p, e.Path)
}

// sharedAccess says what the share lets the user do.
func sharedAccess(e *SharedEntry) string {
	switch {
	case e.IsStorage():
		return "storage"
	case e.Writable():
		return "can edit"
	}
	return "view only"
}

// serveCollection lists a shared collection, or serves a file in it.
func (h *Pages) serveCollection(w http.ResponseWriter, r *http.Request, e *SharedEntry, rel string) {
	entries, err := h.plugin.Shared.CollectionEntries(e)
	if errors.Is(err, ErrNotShared) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	owner := e.Owner.Username
	if rel == "/" {
		items := make([]FileMeta, 0, len(entries))
		for _, c := range entries {
			meta, err := h.plugin.GetByPath(owner, c.Path)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				httperr.Internal(w, r, err)
				return
			}
			if meta.IsDir {
				continue
			}
			item := *meta
			item.Name, item.Path = c.Name, "/"+c.Name
			items = append(items, item)
		}
		pd := h.pageData(w, r, e.Name)
		ui.Render(w, r, sharedFolderPage(pd, e, rel, items, h.plugin.ctx.Now()))
		return
	}
	for _, c := range entries {
		if "/"+c.Name != rel {
			continue
		}
		meta, err := h.plugin.GetByPath(owner, c.Path)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if meta.IsDir {
			break
		}
		if r.URL.Query().Get("dl") == "1" {
			h.serveDownload(w, r, owner, c.Path, meta)
		} else {
			h.serveRaw(w, r, owner, c.Path, meta)
		}
		return
	}
	http.NotFound(w, r)
}
