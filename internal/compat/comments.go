package compat

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// CommentsHandler serves WebDAV comments at /remote.php/dav/comments/files/{fileId}.
type CommentsHandler struct {
	Log   *slog.Logger
	Files *files.Plugin
}

func (h *CommentsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse path: /remote.php/dav/comments/files/{fileId}[/{commentId}]
	path := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/comments/files/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "file ID required", http.StatusBadRequest)
		return
	}

	fileID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		// NC clients may send hex-encoded file IDs
		fileID, err = strconv.ParseInt(parts[0], 16, 64)
		if err != nil {
			http.Error(w, "invalid file ID", http.StatusBadRequest)
			return
		}
	}

	// Verify file exists
	if _, err := h.Files.GetByID(u.Username, fileID); err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, u.Username, fileID)
	case http.MethodPost:
		h.handleCreate(w, r, u.Username, fileID)
	case http.MethodDelete:
		if len(parts) < 2 {
			http.Error(w, "comment ID required", http.StatusBadRequest)
			return
		}
		commentID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			http.Error(w, "invalid comment ID", http.StatusBadRequest)
			return
		}
		h.handleDelete(w, u.Username, commentID)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *CommentsHandler) handlePropfind(w http.ResponseWriter, username string, fileID int64) {
	comments, err := h.Files.ListComments(username, fileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)

	io.WriteString(w, xml.Header)
	io.WriteString(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	for _, c := range comments {
		fmt.Fprintf(w, `
  <d:response>
    <d:href>/remote.php/dav/comments/files/%d/%d</d:href>
    <d:propstat>
      <d:prop>
        <oc:id>%d</oc:id>
        <oc:actorId>%s</oc:actorId>
        <oc:actorType>users</oc:actorType>
        <oc:message>%s</oc:message>
        <oc:creationDateTime>%s</oc:creationDateTime>
        <oc:verb>%s</oc:verb>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>`,
			fileID, c.ID, c.ID,
			xmlEscape(c.ActorID),
			xmlEscape(c.Message),
			time.Unix(c.CreatedAt, 0).UTC().Format(time.RFC3339),
			xmlEscape(c.Verb))
	}
	io.WriteString(w, "\n</d:multistatus>")
}

func (h *CommentsHandler) handleCreate(w http.ResponseWriter, r *http.Request, username string, fileID int64) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var message, actorID, verb string

	// NC Android app sends JSON: {"actorId":"...","actorType":"users","verb":"comment","message":"..."}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var jb struct {
			Message string `json:"message"`
			ActorID string `json:"actorId"`
			Verb    string `json:"verb"`
		}
		if err := json.Unmarshal(body, &jb); err == nil {
			message = jb.Message
			actorID = jb.ActorID
			verb = jb.Verb
		}
	}

	// Fallback: try XML, then plain text
	if message == "" {
		type commentBody struct {
			Message string `xml:"message"`
		}
		var cb commentBody
		if err := xml.Unmarshal(body, &cb); err == nil {
			message = cb.Message
		} else {
			message = strings.TrimSpace(string(body))
		}
	}

	if message == "" {
		http.Error(w, "message required", http.StatusBadRequest)
		return
	}

	// Use authenticated username as actor; actorID from body is the user's UUID
	// which we store for display purposes.
	if actorID == "" {
		actorID = username
	}
	if verb == "" {
		verb = "comment"
	}

	comment, err := h.Files.CreateComment(username, fileID, actorID, message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Location", fmt.Sprintf("/remote.php/dav/comments/files/%d/%d", fileID, comment.ID))
	w.WriteHeader(http.StatusCreated)
}

func (h *CommentsHandler) handleDelete(w http.ResponseWriter, username string, commentID int64) {
	if err := h.Files.DeleteComment(username, commentID, username); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
