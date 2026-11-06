package web

import (
	"bytes"
	"crypto/md5"
	"errors"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/storage"
	"git.duckfam.us/jj-software/cog/internal/user"
)

type avatarHandler struct{ handler }

func (h *avatarHandler) avatars() (*storage.Dir, error) {
	return h.p.Plugins.Storage().Dir(storage.Host, "avatars", "")
}

// Serve serves a user's avatar image or falls back to initials SVG.
func (h *avatarHandler) Serve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.p.Users.GetByID(id)
	if err != nil {
		if !errors.Is(err, user.ErrNotFound) {
			h.p.Log.Error("avatar: look up user", "user", id, "err", err)
		}
		h.serveInitials(w, "?", id)
		return
	}

	if u.Avatar != "" {
		// Defense-in-depth: the Avatar column is written by Upload as
		// `<id><ext>`, but a DB-level write (admin shell, future bug,
		// migration mishap) could plant a value containing path separators
		// or "..". The storage refuses any name leading out of it.
		f, err := h.openAvatar(u.Avatar)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			h.p.Log.Error("avatar: open image", "user", u.ID, "err", err)
		}
		if err == nil {
			defer f.Close()
			ext := filepath.Ext(u.Avatar)
			switch ext {
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			default:
				w.Header().Set("Content-Type", "image/jpeg")
			}
			w.Header().Set("Cache-Control", "private, max-age=3600")
			io.Copy(w, f)
			return
		}
	}

	h.serveInitials(w, initials(u.Username, u.DisplayName), u.Username)
}

// ServeInitials serves an SVG with the user's initials.
func (h *avatarHandler) ServeInitials(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.p.Users.GetByID(id)
	if err != nil {
		if !errors.Is(err, user.ErrNotFound) {
			h.p.Log.Error("avatar: look up user", "user", id, "err", err)
		}
		h.serveInitials(w, "?", id)
		return
	}
	h.serveInitials(w, initials(u.Username, u.DisplayName), u.Username)
}

func (h *avatarHandler) serveInitials(w http.ResponseWriter, init string, seed string) {
	colors := []string{
		"#e53935", "#8e24aa", "#3949ab", "#039be5",
		"#00897b", "#43a047", "#f4511e", "#6d4c41",
		"#546e7a", "#d81b60", "#5e35b1", "#1e88e5",
	}
	hash := md5.Sum([]byte(seed))
	color := colors[int(hash[0])%len(colors)]

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <circle cx="32" cy="32" r="32" fill="%s"/>
  <text x="32" y="32" dy=".35em" text-anchor="middle" fill="white" font-family="sans-serif" font-size="24" font-weight="600">%s</text>
</svg>`, color, html.EscapeString(init))
}

// Upload handles avatar image upload.
func (h *avatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	file, header, err := r.FormFile("avatar")
	if err != nil {
		http.Error(w, "no file uploaded", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	var ext string
	switch {
	case strings.HasPrefix(ct, "image/png"):
		ext = ".png"
	case strings.HasPrefix(ct, "image/jpeg"), strings.HasPrefix(ct, "image/jpg"):
		ext = ".jpg"
	default:
		http.Error(w, "only JPEG and PNG are accepted", http.StatusBadRequest)
		return
	}

	// Validate it's actually an image.
	img, _, err := image.Decode(file)
	if err != nil {
		http.Error(w, "invalid image", http.StatusBadRequest)
		return
	}

	avatars, err := h.avatars()
	if err != nil {
		h.p.Log.Error("avatar: storage", "user", u.ID, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	// Remove old avatar if exists.
	if u.Avatar != "" {
		h.removeFile(u.Avatar)
	}

	filename := u.ID + ext
	var buf bytes.Buffer
	switch ext {
	case ".png":
		err = png.Encode(&buf, img)
	default:
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	}
	if err == nil {
		_, err = avatars.Put(filename, &buf)
	}
	if err != nil {
		h.p.Log.Error("avatar: write image", "user", u.ID, "err", err)
		http.Error(w, "failed to encode image", http.StatusInternalServerError)
		return
	}

	if err := h.p.Users.Update(u.ID, user.UpdateOpts{Avatar: &filename}); err != nil {
		h.p.Log.Error("avatar update failed", "user", u.ID, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// Delete removes the user's avatar.
func (h *avatarHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u.Avatar != "" {
		h.removeFile(u.Avatar)
	}
	empty := ""
	if err := h.p.Users.Update(u.ID, user.UpdateOpts{Avatar: &empty}); err != nil {
		h.p.Log.Error("avatar update failed", "user", u.ID, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// openAvatar opens a stored avatar image.
func (h *avatarHandler) openAvatar(name string) (io.ReadCloser, error) {
	avatars, err := h.avatars()
	if err != nil {
		return nil, err
	}
	return avatars.Open(name)
}

// removeFile deletes a stored avatar image. A file that is already gone is
// not an error; any other failure leaves an orphan behind, which is logged
// rather than failing the request.
func (h *avatarHandler) removeFile(name string) {
	avatars, err := h.avatars()
	if err == nil {
		err = avatars.Remove(name)
	}
	if err != nil {
		h.p.Log.Error("avatar: remove old image", "file", name, "err", err)
	}
}

func initials(username, displayName string) string {
	name := displayName
	if name == "" {
		name = username
	}
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	if len(parts) == 1 {
		for _, r := range parts[0] {
			return string(unicode.ToUpper(r))
		}
	}
	var first, last rune
	for _, r := range parts[0] {
		first = unicode.ToUpper(r)
		break
	}
	for _, r := range parts[len(parts)-1] {
		last = unicode.ToUpper(r)
		break
	}
	return string(first) + string(last)
}
