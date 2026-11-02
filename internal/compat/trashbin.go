package compat

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// TrashbinHandler implements NC-compatible WebDAV for the trashbin.
// Routes: /remote.php/dav/trashbin/{userId}/trash/
type TrashbinHandler struct {
	Log   *slog.Logger
	Files *files.Plugin
}

func (h *TrashbinHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Path: /remote.php/dav/trashbin/{userId}/trash/{trashName}
	rest := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/trashbin/")
	parts := strings.SplitN(rest, "/", 3) // [userId, "trash", trashName]

	urlUserID := ""
	trashName := ""
	if len(parts) >= 1 {
		urlUserID = parts[0]
	}
	if urlUserID == "" {
		urlUserID = u.ID
	}
	// parts[1] is "trash" (or empty)
	if len(parts) >= 3 {
		trashName = parts[2]
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, r, u, urlUserID, trashName)
	case http.MethodDelete:
		h.handleDelete(w, u, trashName)
	case "MOVE":
		h.handleRestore(w, r, u, trashName)
	default:
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (h *TrashbinHandler) handlePropfind(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, trashName string) {
	if trashName != "" {
		// Single item PROPFIND
		item, err := h.Files.GetTrashItem(u.Username, trashName)
		if errors.Is(err, files.ErrNotFound) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			h.Log.Error("get trash item", "user", u.Username, "item", trashName, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		hrefPrefix := "/remote.php/dav/trashbin/" + urlUserID + "/trash/"
		entries := []propfindEntry{h.trashToEntry(hrefPrefix, item)}

		h.writeMultiStatus(w, entries)
		return
	}

	// List all trash items
	items, err := h.Files.ListTrash(u.Username)
	if err != nil {
		h.Log.Error("list trash", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	hrefPrefix := "/remote.php/dav/trashbin/" + urlUserID + "/trash/"

	// First entry is the trash root itself
	entries := []propfindEntry{
		{
			Href: hrefPrefix,
			Propstat: propstat{
				Status: "HTTP/1.1 200 OK",
				Prop: prop{
					DisplayName:  "trash",
					ResourceType: &resourceType{Collection: &struct{}{}},
				},
			},
		},
	}

	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	if depth == "1" {
		for i := range items {
			entries = append(entries, h.trashToEntry(hrefPrefix, &items[i]))
		}
	}

	h.writeMultiStatus(w, entries)
}

func (h *TrashbinHandler) trashToEntry(hrefPrefix string, item *files.TrashItem) propfindEntry {
	var rt resourceType
	if item.IsDir {
		rt.Collection = &struct{}{}
	}

	href := hrefPrefix + item.TrashName
	if item.IsDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}

	return propfindEntry{
		Href: href,
		Propstat: propstat{
			Status: "HTTP/1.1 200 OK",
			Prop: prop{
				DisplayName:   item.OriginalName,
				ContentType:   item.MimeType,
				ContentLength: item.Size,
				LastModified:  time.Unix(item.DeletedAt, 0).UTC().Format(http.TimeFormat),
				ResourceType:  &rt,
				OCId:          fmt.Sprintf("trash-%d", item.ID),
				OCFileID:      fmt.Sprintf("%d", item.ID),
				OCSize:        item.Size,
			},
		},
	}
}

func (h *TrashbinHandler) writeMultiStatus(w http.ResponseWriter, entries []propfindEntry) {
	w.Header().Set("DAV", "1, 3, extended-mkcol")
	writeMultiStatus(h.Log, w, entries)
}

func (h *TrashbinHandler) handleDelete(w http.ResponseWriter, u *user.User, trashName string) {
	if trashName == "" {
		// Empty entire trash
		if err := h.Files.EmptyTrash(u.Username); err != nil {
			h.Log.Error("empty trash", "user", u.Username, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if err := h.Files.PermanentDelete(u.Username, trashName); errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	} else if err != nil {
		h.Log.Error("delete trash item", "user", u.Username, "item", trashName, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *TrashbinHandler) handleRestore(w http.ResponseWriter, r *http.Request, u *user.User, trashName string) {
	if trashName == "" {
		http.Error(w, "trash item required", http.StatusBadRequest)
		return
	}

	err := h.Files.RestoreFromTrash(u.Username, trashName)
	if errors.Is(err, files.ErrNotFound) {
		http.Error(w, "trash item not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Warn("restore from trash", "user", u.Username, "item", trashName, "err", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	w.WriteHeader(http.StatusCreated)
}
