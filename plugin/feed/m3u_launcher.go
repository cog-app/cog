package feed

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/plugin"
)

// Launcher serves a .desktop file that plays the list in mpv with silence
// skipped: the user's file manager or app menu runs it, so a list is one
// click from the desktop. It is the M3U's path under /launchers/, and needs
// the session: the M3U link it carries is the grant to play.
func (h *Pages) Launcher(w http.ResponseWriter, r *http.Request) {
	l, err := parseM3UPath(r.PathValue("kind"), r.PathValue("file"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	store, err := h.userStore(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	name, err := launcherName(store, l)
	if err != nil {
		lookupError(w, r, fmt.Errorf("feed launcher: %s: %w", l.path(), err))
		return
	}
	signed, err := h.plugin.M3UURL(store, plugin.Username(r), l, hlsOptions{Skip: true})
	if err != nil {
		httperr.Internal(w, r, fmt.Errorf("feed launcher: sign %s: %w", l.path(), err))
		return
	}
	m3u := h.plugin.absoluteURL(r, signed)
	// Only a configured base URL can carry these: the path and query are
	// escaped. Quoting cannot make a line break safe in an Exec key, and
	// a space is a sign the base URL is not one.
	if i := strings.IndexFunc(m3u, func(c rune) bool { return unicode.IsControl(c) || c == ' ' }); i >= 0 {
		httperr.Internal(w, r, fmt.Errorf("feed launcher: M3U URL has %q in it", m3u[i]))
		return
	}
	body := desktopEntry(name, m3u)
	w.Header().Set("Content-Type", "application/x-desktop")
	w.Header().Set("Content-Disposition", `attachment; filename="cog-`+launcherSlug(name)+`.desktop"`)
	w.Header().Set("Cache-Control", "private, no-store")
	if _, err := w.Write([]byte(body)); err != nil {
		httperr.Log(r, fmt.Errorf("feed launcher: write: %w", err))
	}
}

// launcherName is the name of the list l: its playlist, folder or
// subscription name, its tag, "Starred" or "Unread".
func launcherName(store *Store, l m3uList) (string, error) {
	v, err := l.View()
	if err != nil {
		return "", err
	}
	switch v.Kind {
	case ViewPlaylist:
		p, err := store.GetPlaylist(v.ID)
		return p.Name, err
	case ViewFolder:
		f, err := store.GetFolder(v.ID)
		return f.Name, err
	case ViewSubscription:
		s, err := store.GetSubscription(v.ID)
		return s.Title, err
	case ViewTag:
		return v.Tag, nil
	case ViewStarred:
		return "Starred", nil
	case ViewUnread:
		return "Unread", nil
	}
	return "", fmt.Errorf("no launcher for %s", strconv.Quote(string(v.Kind)))
}

// desktopOneLine folds every control character, line breaks among them, to
// a space: a value of a Desktop Entry is one line, and a second line would
// be a second key.
func desktopOneLine(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return ' '
		}
		return c
	}, crlf.Replace(s))
}

// crlf makes a CRLF one space, not two.
var crlf = strings.NewReplacer("\r\n", "\n")

// desktopEntry is the launcher's text for an M3U URL and a list name.
func desktopEntry(name, m3uURL string) string {
	name = desktopOneLine(name)
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Cog: " + name + "\n")
	b.WriteString("Comment=Play " + name + " from Cog in mpv, silence skipped\n")
	b.WriteString("Exec=mpv --force-window " + desktopExecArg(m3uURL) + "\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("Categories=AudioVideo;Player;\n")
	return b.String()
}

// desktopExecArg quotes s for an Exec key, in the two levels the Desktop
// Entry spec ("The Exec key", and "Basic format of the file" for string
// values) lays down. Inside double quotes, `"`, "`", `$` and `\` each take a
// backslash; then the whole value is a string, where every backslash is
// doubled. So `$` is written `\\$`, and a literal backslash `\\\\`. % becomes
// %% (a field code otherwise). s must be one line; Launcher sees to it.
func desktopExecArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '`', '$', '\\':
			b.WriteString(`\\`)
			if c == '\\' {
				b.WriteRune(c)
			}
		case '%':
			b.WriteByte('%')
		}
		b.WriteRune(c)
	}
	b.WriteByte('"')
	return b.String()
}

// launcherSlug is name lowercased with each run of characters outside
// [a-z0-9] a single "-", and none at the ends: safe in a file name and a
// quoted header, whatever the list is called. "list" when nothing is left.
func launcherSlug(name string) string {
	var b strings.Builder
	dash := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteByte(c)
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "list"
	}
	return b.String()
}
