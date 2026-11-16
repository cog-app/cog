package feed

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// m3uRoute is where a list is served as an M3U for an external player. It
// has no session: the signature in its query is the grant (m3u_links.go).
const m3uRoute = "GET /apps/feed/m3u/{user}/{kind}/{file}"

// m3uLimit is how many items an M3U lists, as a view page does.
const m3uLimit = 500

// ServeM3U serves a playlist or view as an M3U, in the page's order: each
// item's signed master URL where it plays over HLS, its media URL where it
// does not, and nothing for a text item. It writes nothing but, with
// skip=silence, which items' edges are wanted.
func (p *Plugin) ServeM3U(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts, err := parseHLSOptions(q)
	if err == nil && opts.Edit != "" {
		err = errors.New("an M3U takes no edit")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	l, err := parseM3UPath(r.PathValue("kind"), r.PathValue("file"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	username := r.PathValue("user")
	lookup := p.ctx.UserLookup()
	if lookup == nil {
		httperr.Internal(w, r, errors.New("feed m3u: no user lookup"))
		return
	}
	if _, err := lookup.GetByUsername(username); err != nil {
		if errors.Is(err, user.ErrNotFound) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		httperr.Internal(w, r, fmt.Errorf("feed m3u: look up user: %w", err))
		return
	}
	db, err := p.DB(username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	store := NewStore(db)
	key, err := store.ExistingHLSKey()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if key == nil || !validM3USignature(key, username, l, opts, q.Get("sig")) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	entries, err := p.m3uEntries(r, store, key, username, l, opts)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, fmt.Errorf("feed m3u: %s: %w", l.path(), err))
		return
	}
	body := m3uBody(entries)
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write([]byte(body)); err != nil {
		httperr.Log(r, fmt.Errorf("feed m3u: write: %w", err))
	}
}

// m3uEntry is one line pair of an M3U.
type m3uEntry struct {
	Duration int64  // seconds; -1 when unknown
	Title    string // "<subscription> – <title>", newlines folded to spaces
	URL      string // absolute
}

// m3uBody is the M3U of entries: "#EXTM3U" then "#EXTINF:<d>,<title>" and
// the URL for each.
func m3uBody(entries []m3uEntry) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", e.Duration, e.Title, e.URL)
	}
	return b.String()
}

// m3uTitle folds line breaks to spaces: a title is one line of the M3U.
var m3uTitle = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

// m3uEntries lists l's items as entries, signing masters with key, the
// user's existing one. It reads the view, its downloads and the
// subscriptions once each, whatever the number of items. With o.Skip, it
// marks the packaged items' edges wanted, so the worker cuts them before
// the player reaches them; that is the only write.
//
// An item packaged for HLS gets its master with o. One that is not is
// left out when it is to be muted (Download.muted), since only its clean
// audio may play and that is made from a download; an undownloaded YouTube
// video gets its remote master (remoteEligible), which resolves when the
// player fetches it; anything else plays its media URL, unskipped.
func (p *Plugin) m3uEntries(r *http.Request, store *Store, key []byte, username string, l m3uList, o hlsOptions) ([]m3uEntry, error) {
	v, err := l.View()
	if err != nil {
		return nil, err
	}
	spec, expr, err := store.ResolveView(v)
	if err != nil {
		return nil, err
	}
	items, _, err := store.ViewPage(spec, expr, p.ctx.Now(), m3uLimit)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	downloads, err := store.DownloadsByItems(ids)
	if err != nil {
		return nil, err
	}
	subs, err := store.ListSubscriptions()
	if err != nil {
		return nil, err
	}
	kinds := make(map[int64]string, len(subs))
	for _, s := range subs {
		kinds[s.ID] = s.Kind
	}
	entries := make([]m3uEntry, 0, len(items))
	var packaged []int64
	for _, it := range items {
		if it.Type != "audio" && it.Type != "video" {
			continue
		}
		dl := downloads[it.ID]
		var u string
		switch {
		case dl.PackageState == "done":
			u = p.absoluteURL(r, hlsMasterPath(key, username, it.ID, o))
			packaged = append(packaged, it.ID)
		case dl.muted():
			continue
		case remoteEligible(kinds[it.SubscriptionID], it.Type, it.MediaURL, dl):
			u = p.absoluteURL(r, hlsMasterPath(key, username, it.ID, o))
		case it.MediaURL != "":
			u = it.MediaURL
		default:
			continue
		}
		d := it.DurationSec
		if d <= 0 {
			d = -1
		}
		entries = append(entries, m3uEntry{Duration: d, Title: m3uTitle.Replace(it.SubscriptionTitle + " – " + it.Title), URL: u})
	}
	if o.Skip {
		changed, err := store.WantEdges(packaged...)
		if err != nil {
			return nil, err
		}
		if changed {
			p.TriggerFetch(username, 0)
		}
	}
	return entries, nil
}
