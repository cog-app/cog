# Feed HLS, Phase 6a: M3U for External Players — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Cog playlist or view can be played in mpv from a signed `.m3u` link: one entry per item, each an absolute signed HLS master URL (with `skip=silence` when the link asks for it). The view pages offer the links and a `.desktop` launcher.

**Architecture:** A second unauthenticated route, `GET /apps/feed/m3u/{user}/{kind}/{file}`, sits beside the HLS route. Its HMAC uses the same per-user key, with its own domain prefix. It resolves the view with `ResolveView` and lists up to 500 items in the view's order with `ViewPage`. Downloads and subscription kinds are batched, with no query per item. It writes each entry as an absolute URL: a signed master URL (`HLSMasterURL` with the link's options) for a playable HLS item, or the origin URL otherwise. View pages render both links (plain and `skip=silence`) with a copy button. An authenticated API route serves a `.desktop` file that runs `mpv` on the skip link.

**Tech Stack:** Go 1.26, SQLite (per-user plugin DB), templ, cogtest, webtest; mpv for the manual check.

**Spec:** `DESIGN/plugins/feed_hls.md` ("Signed URLs", "Clients", "Phases" 6). **Roadmap (binding):** `docs/superpowers/plans/2026-11-15-feed-hls-roadmap.md`, decision 9 and "Options and signing".

## Global Constraints

- Work in the worktree `cog/tmp/feed-hls` on branch `feed-hls`. Do not push, merge, or open an MR without asking.
- Errors are never swallowed: wrap and return, or log (`CLAUDE.md`). PRG for any POST (this phase adds none).
- Tests go through `cogtest`, with `internal/testing/require` and `assert`, never testify.
- Check builds with `go install ./...`, not `go build`. Format with `go fmt ./...` and `go tool templ fmt .`. Regenerate templ with `go tool templ generate -log-level warn` from the repo root (`go generate ./plugin/feed` does not run templ). Commit the regenerated `pages_templ.go` in the same commit as the `.templ` change.
- Links do not expire. Revoking the key (`RotateHLSKey`) ends HLS and M3U links alike; there is one key.
- The m3u route never writes to the database. It reads the key with `ExistingHLSKey`, and a missing key is a 403, as phase 1's HLS route does after its fix.
- An M3U lists at most 500 items (`m3uLimit = 500`, the same value as `feedViewLimit`).
- Options are exactly phase 4's: `skip=silence`, and nothing else. Any other key or value is a 400.
- Content types: the M3U is `audio/x-mpegurl` with `Cache-Control: private, no-cache`. The launcher is `application/x-desktop`, sent as an attachment.
- Decided, not to reopen: external players get pre-signed links that do not expire; `skip=silence` is a query parameter; a player that can skip silence does it itself, so Cog's own clients never use these links.

## Decisions this plan makes (recorded in the design in Task 6)

- **`file` is `<id>.m3u`, or `all.m3u` for `starred` and `unread`.** The `.m3u` suffix is what makes mpv treat the response as a playlist. A Go mux wildcard cannot be `{id}.m3u`, so the handler parses `{file}`.
  - For `tag`, the id is the tag name, path-escaped in the URL and signed unescaped.
  - For `playlist`, `folder` and `sub`, the id is decimal.
- **The signed message** is `"cog-feed-m3u\x00" + user + "\x00" + kind + "\x00" + id`, plus `"\x00" + canonical` when there are options. The prefix differs from HLS's `"cog-feed-hls\x00"`, so neither signature opens the other route.
- **Entries.** Each entry is decided once, by these rules:
  - Text items are left out.
  - An item playable over HLS gets its signed master URL, absolute, with the link's options.
    - "Playable over HLS" means `PackageState == "done"` once phases 1–4 have landed.
    - After phase 5, it also means an undownloaded YouTube item, whose master is the remote presentation.
  - Any other audio or video item gets `Item.MediaURL` as it is.
    - For a podcast, that is the enclosure, which mpv plays directly, unskipped.
    - For an undownloaded YouTube item before phase 5, it is the watch URL, which mpv's ytdl hook plays.
  - An item with an empty `MediaURL` that is not playable over HLS is left out.
- **Absolute URLs** come from `ctx.BaseURL()` when it is set, and otherwise from the request's scheme and host. This is the rule `ncapiBackend.ItemPage` already uses, factored into one helper. `X-Forwarded-*` headers are not trusted.
- **Order and selection** follow the page exactly: the spec and expression that `ResolveView` returns, with the playlist's order and shuffle seed. `HideRead` is applied only where the view is `unread`. The `HideEmpty` web pref is a display pref and does not apply.
- **The launcher** uses the skip link, because mpv cannot skip silence itself. It quotes `Exec` with double quotes and escapes `%` as `%%` (Desktop Entry spec: single quotes are not valid quoting, and `%` is a field code). It is served from an authenticated API route, since the person downloading it is logged in.

## Review Focus

1. **Signature domain separation and scoping.** An HLS signature must not open an M3U, and vice versa. Alice's M3U signature must not open Bob's list or a different kind or id of hers. `skip=silence` must not be addable to an unskipped link (Task 2 `TestM3USignatureScopes`, Task 3 `TestM3URejects`).
2. **Tag names that are hard to escape:** a space, `/`, `%`, unicode, and a name ending in `.m3u`. They must round-trip from page link to route to `ResolveView`. The launcher's `%%` escaping must survive them too (Task 2 `TestParseM3UPath`, Task 3 `TestM3UTag`, Task 5 `TestM3ULauncherEscapes`).
3. **Inner URLs must actually play.** Every master URL in an M3U, with and without `skip=silence`, must answer 200 from the HLS route with no cookie. A URL that only looks right is not enough (Task 3 `TestM3UPlaylist`, `TestM3USkipSilence`).
4. **The order is the page's order**, including a shuffled playlist (same seed) and an `oldest` playlist (Task 3 `TestM3UOrderMatchesPage`).
5. **Revoking media links kills M3U links**, and the route never creates a key: an M3U request for a user with no key writes nothing (Task 3 `TestM3URevoke`, `TestM3UNoKeyWritesNothing`).

---

### Task 1: Reconcile with phases 4 and 5 as they landed

**Files:**
- Read: `plugin/feed/hls_links.go`, `plugin/feed/hls_serve.go`, `plugin/feed/rpc_media.go`, `plugin/feed/views.go`, `plugin/feed/feed.go`
- Modify: none (ledger only)

**Interfaces:**
- Consumes (roadmap, from phase 4):
  ```go
  type hlsOptions struct{ Skip bool }
  func parseHLSOptions(q url.Values) (hlsOptions, error)
  func (o hlsOptions) Values() url.Values // without sig
  func hlsSignature(key []byte, user string, itemID int64, o hlsOptions) string
  func (p *Plugin) HLSMasterURL(store *Store, username string, itemID int64, o hlsOptions) (string, error)
  ```
- Consumes (if phase 5 landed): whatever predicate phase 5 uses to decide that an undownloaded YouTube item has a remote presentation. Record its name.
- Consumes (phase 1, present today): `Store.ExistingHLSKey() ([]byte, error)`, `Store.DownloadsByItems(ids []int64) (map[int64]Download, error)`, `Store.ListSubscriptions() ([]Subscription, error)`, `Store.ResolveView(View) (ViewSpec, query.Expr, error)`, `Store.ViewPage(spec, expr, now, n) ([]ItemWithSubscription, *Cursor, error)`, `ncapiBackend.ItemPage` (`feed.go`).

- [ ] **Step 1: Compare every Consumes line with the code.** Run `go doc ./plugin/feed` and read `hls_links.go`. For each mismatch, write `Task 1: Ruling: <roadmap says X, code has Y> — use Y — <cost>` in the ledger. If phase 4 has not landed, stop: this plan needs `hlsOptions`.
- [ ] **Step 2: Decide the YouTube assertion.** Phase 5 decides what the Task 3 test `TestM3UYouTubeNotDownloaded` asserts:
  - Phase 5 landed: the entry is the remote master URL.
  - Phase 5 not landed: the entry is the watch URL.

  Write which one in the ledger.

---

### Task 2: M3U signing, link paths and the absolute base

**Files:**
- Create: `plugin/feed/m3u_links.go`
- Modify: `plugin/feed/feed.go` (`ItemPage` uses the new helper)
- Test: `plugin/feed/m3u_links_internal_test.go` (package `feed`)

**Interfaces:**
- Produces:
  ```go
  // m3uList names a list an M3U link plays.
  type m3uList struct {
      Kind ViewKind // playlist, tag, folder, sub, starred, unread
      ID   string   // decimal id; tag name (unescaped); "all" for starred and unread
  }
  func (l m3uList) View() (View, error) // ID parsed for playlist/folder/sub
  func parseM3UPath(kind, file string) (m3uList, error) // file is PathValue (already unescaped); ".m3u" suffix required; kind not in the six, or id malformed, is an error
  func (l m3uList) path() string // "playlist/7.m3u", "tag/a%20b.m3u", "starred/all.m3u"
  func m3uSignature(key []byte, user string, l m3uList, o hlsOptions) string
  func validM3USignature(key []byte, user string, l m3uList, o hlsOptions, sig string) bool
  // M3UURL is the signed path of a list's M3U: /apps/feed/m3u/<user>/<path>?sig=…[&skip=silence]
  func (p *Plugin) M3UURL(store *Store, username string, l m3uList, o hlsOptions) (string, error)
  // absoluteURL prefixes path with ctx.BaseURL(), or the scheme and host r reached.
  func (p *Plugin) absoluteURL(r *http.Request, path string) string
  ```
  `M3UURL` calls `store.HLSKey()`, which creates the key on first use. It is used from pages only; the route reads the key with `ExistingHLSKey`.

- [ ] **Step 1: Write the failing tests**

```go
func TestParseM3UPath(t *testing.T) {
    for _, c := range []struct{ kind, file string; want m3uList; ok bool }{
        {"playlist", "7.m3u", m3uList{ViewPlaylist, "7"}, true},
        {"folder", "3.m3u", m3uList{ViewFolder, "3"}, true},
        {"sub", "12.m3u", m3uList{ViewSubscription, "12"}, true},
        {"tag", "a b/c%d é.m3u.m3u", m3uList{ViewTag, "a b/c%d é.m3u"}, true},
        {"starred", "all.m3u", m3uList{ViewStarred, "all"}, true},
        {"unread", "all.m3u", m3uList{ViewUnread, "all"}, true},
        {"playlist", "7.m3u8", m3uList{}, false},
        {"playlist", "x.m3u", m3uList{}, false},
        {"playlist", "-1.m3u", m3uList{}, false},
        {"starred", "1.m3u", m3uList{}, false},
        {"history", "all.m3u", m3uList{}, false},
        {"all", "all.m3u", m3uList{}, false},
        {"tag", ".m3u", m3uList{}, false},
    } {
        got, err := parseM3UPath(c.kind, c.file)
        assert.Equal(t, c.ok, err == nil, "%s/%s: %v", c.kind, c.file, err)
        if c.ok { assert.Equal(t, c.want, got) }
    }
    // path() round-trips through URL unescaping
    l := m3uList{ViewTag, "a b/c%d é.m3u"}
    p := l.path()
    kind, file, _ := strings.Cut(p, "/")
    file, err := url.PathUnescape(file)
    require.NoError(t, err)
    got, err := parseM3UPath(kind, file)
    require.NoError(t, err)
    assert.Equal(t, l, got)
}

func TestM3USignatureScopes(t *testing.T) {
    key := bytes.Repeat([]byte{1}, 32)
    pl := m3uList{ViewPlaylist, "7"}
    sig := m3uSignature(key, "alice", pl, hlsOptions{})
    assert.True(t, validM3USignature(key, "alice", pl, hlsOptions{}, sig))
    assert.False(t, validM3USignature(key, "bob", pl, hlsOptions{}, sig))
    assert.False(t, validM3USignature(key, "alice", m3uList{ViewPlaylist, "8"}, hlsOptions{}, sig))
    assert.False(t, validM3USignature(key, "alice", m3uList{ViewFolder, "7"}, hlsOptions{}, sig))
    assert.False(t, validM3USignature(key, "alice", pl, hlsOptions{Skip: true}, sig))
    assert.False(t, validM3USignature(bytes.Repeat([]byte{2}, 32), "alice", pl, hlsOptions{}, sig))
    // domain separation: an HLS signature for item 7 is not an M3U signature for playlist 7
    assert.NotEqual(t, hlsSignature(key, "alice", 7, hlsOptions{}), sig)
}

func TestAbsoluteURL(t *testing.T) // BaseURL "https://cog.example" → "https://cog.example/apps/feed/x"; empty BaseURL, r.Host "h:8580", no TLS → "http://h:8580/apps/feed/x"
```

`TestAbsoluteURL` needs a `Plugin` with a settable base URL. If `ctx.BaseURL()` cannot be stubbed in an internal test, make it a cogtest test instead: `cogtest` sets `Config.BaseURL = ts.URL`, so assert that prefix. Record a ruling.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestParseM3UPath|TestM3USignatureScopes|TestAbsoluteURL' ./plugin/feed`
Expected: FAIL to compile (`undefined: m3uList`).

- [ ] **Step 3: Implement `m3u_links.go`.**
  - Use the message format from Decisions, HMAC-SHA256, base64 RawURL.
  - `canonical` is `o.Values().Encode()`, exactly as phase 4 does for HLS. Share the helper if phase 4 made one.
  - `parseM3UPath` requires `strings.HasSuffix(file, ".m3u")` and a non-empty stem. Decimal ids go through `strconv.ParseInt` and must be > 0.
  - Change `ncapiBackend.ItemPage` to `return b.p.absoluteURL(r, itemHref(id, ""))`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -run 'TestParseM3UPath|TestM3USignatureScopes|TestAbsoluteURL|TestNC|TestGpodder' ./plugin/feed/...`
Expected: PASS (the ncapi and gpodder tests guard the `ItemPage` refactor).

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/m3u_links.go plugin/feed/m3u_links_internal_test.go plugin/feed/feed.go
git commit -m "feed: sign M3U links to a playlist or view

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
```

---

### Task 3: The M3U route

**Files:**
- Create: `plugin/feed/m3u_serve.go`
- Modify: `plugin/feed/feed.go` (register the route beside `hlsRoute`)
- Test: `plugin/feed/m3u_test.go` (package `feed_test`; reuse `packagedItem`, `feedStore` and `feedPlugin` from `hls_test.go`)

**Interfaces:**
- Consumes: Task 2's `m3uList`, `parseM3UPath`, `validM3USignature`, `absoluteURL`, `M3UURL`; phase 4's `parseHLSOptions`, `HLSMasterURL` and `Store.WantEdges(itemIDs ...int64) (bool, error)`.
- Produces:
  ```go
  const m3uRoute = "GET /apps/feed/m3u/{user}/{kind}/{file}"
  const m3uLimit = 500
  func (p *Plugin) ServeM3U(w http.ResponseWriter, r *http.Request)
  // m3uEntry is one line pair of an M3U.
  type m3uEntry struct {
      Duration int64  // seconds; -1 when unknown
      Title    string // "<subscription> – <title>", newlines folded to spaces
      URL      string // absolute
  }
  func m3uBody(entries []m3uEntry) string // "#EXTM3U\n" then "#EXTINF:<d>,<title>\n<url>\n" each
  // m3uEntries lists l's items as entries; it reads downloads and subscriptions once each.
  func (p *Plugin) m3uEntries(r *http.Request, store *Store, username string, l m3uList, o hlsOptions) ([]m3uEntry, error)
  ```
  Also export `ServeM3U` and the entry rules for tests through `export_test.go` only if a test needs them. The cogtest tests below go through HTTP and need none.

**Check order in `ServeM3U`**, mirroring `ServeHLS`:
1. Parse options: an error is a 400.
2. Parse the path: an error is a 400.
3. Look up the user: an unknown user is a 403.
4. Read the key with `ExistingHLSKey`: a nil key or a bad signature is a 403.
5. Resolve the view: `sql.ErrNoRows` is a 404, any other error a 500.
6. Write the body: `Content-Length`, `Content-Type: audio/x-mpegurl`, `Cache-Control: private, no-cache`; a HEAD request gets the headers only.

- [ ] **Step 1: Write the failing tests**

```go
// m3uURL signs alice's list l through the plugin, as the pages do.
func m3uURL(t *testing.T, env *cogtest.Env, username, path string, skip bool) string

func TestM3UPlaylist(t *testing.T) {
    // alice: a playlist matching everything; items: packaged video (packagedItem), an undownloaded
    // podcast episode (Type audio, MediaURL https://cdn.example.com/ep.mp3, DurationSec 1800,
    // subscription titled "Pod"), a text item.
    r := env.Get(t, m3uURL(t, env, "alice", "playlist/<id>.m3u", false)).Status(http.StatusOK) // no cookie
    assert.Equal(t, "audio/x-mpegurl", r.Header.Get("Content-Type"))
    assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
    body := r.ReadBody()
    assert.True(t, strings.HasPrefix(body, "#EXTM3U\n"), body)
    // two entries: the text item is left out
    assert.Equal(t, 2, strings.Count(body, "#EXTINF:"))
    assert.True(t, strings.Contains(body, "#EXTINF:1800,Pod – <episode title>\nhttps://cdn.example.com/ep.mp3\n"), body)
    // the packaged item's entry is an absolute master URL that plays with no cookie
    master := lineAfterTitle(body, "Episode")
    assert.True(t, strings.HasPrefix(master, env.Providers.Config.BaseURL+"/apps/feed/hls/alice/"), master)
    env.Get(t, strings.TrimPrefix(master, env.Providers.Config.BaseURL)).Status(http.StatusOK)
}

func TestM3USkipSilence(t *testing.T) {
    // same list, link with skip=silence: the HLS entry carries &skip=silence and answers 200;
    // the podcast entry is unchanged (origin URL, no options);
    // every packaged item listed is now pending for the edge worker (phase 4's WantEdges), so mpv
    // gets full cuts by the time it reaches later entries; the plain (no skip) link marks nothing
}

func TestM3URejects(t *testing.T) {
    // table, each with its status:
    //   kind "history" → 400; file "7.m3u8" → 400; file "x.m3u" → 400
    //   extra=1 → 400; skip=fast → 400
    //   sig flipped → 403; no sig → 403; alice's sig on /m3u/bob/... → 403; user "nobody" → 403
    //   valid sig for playlist 999999 (signed via M3UURL) → 404
    //   unskipped sig with &skip=silence appended → 403
    //   HLS sig of item N on playlist/N.m3u → 403
}

func TestM3UNoKeyWritesNothing(t *testing.T) {
    // a user that never had a key: GET /apps/feed/m3u/carol/starred/all.m3u?sig=x → 403,
    // and ExistingHLSKey is still nil afterwards
}

func TestM3URevoke(t *testing.T) {
    // as TestHLSRevoke: the old M3U link → 403 after POST settings/media-links/revoke;
    // a fresh one → 200, and its inner master URL → 200
}

func TestM3UOrderMatchesPage(t *testing.T) {
    // three packaged items with distinct published_at; playlists "oldest" and "shuffled" (fixed seed):
    // the order of master URLs in the M3U equals ResolveView+ViewPage's order, item by item
}

func TestM3UTag(t *testing.T) {
    // tag "a b/c%d é.m3u" on one packaged item: the link from M3UURL (path-escaped) answers 200 with that item
}

func TestM3UStarredAndUnread(t *testing.T) {
    // starred/all.m3u lists only the starred item; unread/all.m3u leaves out a read one
}

func TestM3ULimit(t *testing.T) {
    // 501 audio items with MediaURL in one subscription: sub/<id>.m3u has 500 #EXTINF lines
}

func TestM3UYouTubeNotDownloaded(t *testing.T) {
    // a youtube-kind subscription's undownloaded video item: the entry is what Task 1 recorded
    // (watch URL before phase 5; remote master URL after)
}

func TestM3UHead(t *testing.T) {
    // HEAD → 200, Content-Length equals the GET body's length, empty body
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestM3U' ./plugin/feed`
Expected: FAIL: the route is not registered (404 where 200, 400 or 403 is expected).

- [ ] **Step 3: Implement `m3u_serve.go` and register `m3uRoute` with `priv.RegisterRoute`** next to `hlsRoute` in `feed.go`.
  - `m3uEntries` does five things, in this order:
    1. `ResolveView(l.View())`.
    2. `ViewPage(spec, expr, ctx.Now(), m3uLimit)`.
    3. `DownloadsByItems(ids)`.
    4. `ListSubscriptions()`, into a `map[int64]string` of kinds.
    5. The entry rules from Decisions, with `HLSMasterURL(store, username, id, o)` passed through `absoluteURL`.
    6. With `o.Skip`, one `WantEdges(packagedIDs...)` call (phase 4), and `TriggerFetch` when it reports a change. These are the only writes the route makes.
  - `HLSMasterURL` calls `HLSKey()`. The key already exists here, since the signature was valid, so nothing is written. If phase 4 left `HLSMasterURL` creating keys, add `hlsMasterURLWithKey(key, …)` and call it with the key already read. Ledger the ruling.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -run 'TestM3U|TestHLS' ./plugin/feed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/m3u_serve.go plugin/feed/m3u_test.go plugin/feed/feed.go
git commit -m "feed: serve a playlist or view as a signed M3U of HLS links

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
```

---

### Task 4: "Play elsewhere" links on view pages

**Files:**
- Modify: `plugin/feed/pages.go` (the `PlaylistView`, `FolderView`, `SubscriptionView`, `TagView`, `StarredView` and `Index` handlers)
- Modify: `plugin/feed/pages.templ` (a `feedPlayElsewhere` component in each of those pages' `page-header`), regenerated as `pages_templ.go`
- Test: `plugin/feed/pages_m3u_test.go`; snapshots under `plugin/feed/testdata/snapshots` if the browser tests cover these pages

**Interfaces:**
- Consumes: Task 2's `M3UURL` and `absoluteURL`.
- Produces:
  ```go
  // playElsewhere is what a view page offers external players.
  type playElsewhere struct {
      M3U, M3USkip string // absolute signed M3U URLs
      Launcher     string // /apps/feed/launchers/<path> (Task 5)
  }
  func (h *Pages) playElsewhere(r *http.Request, store *Store, l m3uList) (playElsewhere, error)
  ```
  ```templ
  templ feedPlayElsewhere(p playElsewhere) // <details class="play-elsewhere"><summary>Play in mpv</summary> …
  ```
  - Each URL is shown in a `secret-box` `<code>` with a "Copy link" button. The button uses a feed-local `script copyText(text string) { cogCopy(event.currentTarget, text); }`, since `cog-share.js` is loaded by `ui/layout.templ`.
  - Labels: "Copy link", and "Copy link (skip silence)".
  - A plain `<a href={ p.Launcher } download>Download launcher</a>`.
  - A help line: "Plays in mpv or any player that opens .m3u links. Revoke media links in Sync settings to stop every link."
  - `Index`, the "all" view, offers the `unread` list. The pages are GETs; `M3UURL` creating the key on first view matches what `ItemDetail` already does with `HLSMasterURL`.

- [ ] **Step 1: Write the failing test**

```go
func TestViewPagesOfferM3U(t *testing.T) {
    // alice with a playlist, a folder, a subscription, a tag, a starred item.
    // For each page (playlists/<id>, folder/<id>, subscription/<id>, tag/<name>, starred, and "/apps/feed/"):
    //   GET with alice's session → 200; the body contains "/apps/feed/m3u/alice/<kind>/<id-or-all>.m3u?sig="
    //   and the same with "&skip=silence"; extract the first M3U URL, strip BaseURL, GET it with no cookie → 200
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -run TestViewPagesOfferM3U ./plugin/feed`
Expected: FAIL (`"/apps/feed/m3u/alice/playlist/…" not in body`).

- [ ] **Step 3: Implement the handlers' `playElsewhere` and the templ component,** then run `go tool templ generate -log-level warn` and `go tool templ fmt .`. An error from `playElsewhere` is a 500 through `httperr.Internal`.

- [ ] **Step 4: Run tests and refresh snapshots**

Run: `go test -run 'TestViewPagesOfferM3U' ./plugin/feed`, then `WEBTEST_UPDATE=1 go test -run TestBrowser ./plugin/feed`. Read the snapshot diff: only the new `<details>` should change.
Expected: PASS; the snapshot changes are only the "Play in mpv" block.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/pages.go plugin/feed/pages.templ plugin/feed/pages_templ.go plugin/feed/pages_m3u_test.go plugin/feed/testdata/snapshots
git commit -m "feed: view pages offer their M3U links for mpv

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
```

---

### Task 5: A `.desktop` launcher per list

**Files:**
- Create: `plugin/feed/m3u_launcher.go`
- Modify: `plugin/feed/feed.go` (`ctx.RegisterAPI("launchers/{kind}/{file}", …)`)
- Test: `plugin/feed/m3u_test.go`

**Interfaces:**
- Consumes: `parseM3UPath` (with `file` ending `.m3u`, so the launcher path mirrors the M3U path), `M3UURL`, `absoluteURL`, `Store.GetPlaylist`, `Store.GetSubscription`, and the folder lookup the folder page uses.
- Produces:
  ```go
  func (h *Pages) Launcher(w http.ResponseWriter, r *http.Request)
  // desktopEntry is the launcher's text for an M3U URL and a list name.
  func desktopEntry(name, m3uURL string) string
  // desktopExecArg quotes s for an Exec key: inside double quotes, `"`, "`", `$` and `\` are backslash-escaped, and % becomes %%.
  func desktopExecArg(s string) string
  ```
- Body:
  ```
  [Desktop Entry]
  Type=Application
  Name=Cog: <name>
  Comment=Play <name> from Cog in mpv, silence skipped
  Exec=mpv --force-window <desktopExecArg(skip M3U URL)>
  Terminal=false
  Categories=AudioVideo;Player;
  ```
  - `<name>` is the playlist, folder or subscription name, the tag, "Starred", or "Unread", with newlines folded to spaces.
  - Headers: `Content-Type: application/x-desktop`, and `Content-Disposition: attachment; filename="cog-<slug>.desktop"`, where `<slug>` is the name lowercased with each run of non-`[a-z0-9]` replaced by `-` (`"list"` if empty).

- [ ] **Step 1: Write the failing tests**

```go
func TestM3ULauncher(t *testing.T) {
    // GET /apps/feed/launchers/playlist/<id>.m3u with alice's session → 200, Content-Type application/x-desktop,
    // Content-Disposition `attachment; filename="cog-morning-news.desktop"` for playlist "Morning News";
    // body has "Name=Cog: Morning News\n" and an Exec line `mpv --force-window "<absolute M3U URL>&skip=silence"`;
    // that URL (unescaped back from %%) answers 200 with no cookie.
    // Without a session → not 200 (RegisterAPI's 401).
}

func TestM3ULauncherEscapes(t *testing.T) {
    assert.Equal(t, `"http://h/a%%20b?sig=x&skip=silence"`, desktopExecArg("http://h/a%20b?sig=x&skip=silence"))
    assert.Equal(t, `"a\"b\$c\\d\`+"`"+`e"`, desktopExecArg("a\"b$c\\d`e"))
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestM3ULauncher' ./plugin/feed`
Expected: FAIL (`undefined: desktopExecArg`, or a 404 for the route).

- [ ] **Step 3: Implement it and register the route.** Task 4's `playElsewhere.Launcher` already points at `/apps/feed/launchers/<l.path()>`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -run 'TestM3U|TestViewPagesOfferM3U' ./plugin/feed`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/feed/m3u_launcher.go plugin/feed/m3u_test.go plugin/feed/feed.go
git commit -m "feed: a desktop launcher plays a list in mpv with silence skipped

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
```

---

### Task 6: mpv check, design and TODO

**Files:**
- Modify: `DESIGN/plugins/feed_hls.md`, `TODO.md`

- [ ] **Step 1: Run the whole suite**

Run: `go tool verify -continue`
Expected: every step passes. Name any failure in the ledger, including flakes such as `cmd/cog TestBrowserCore/Search/Mobile` under load (rerun alone). Revert `go fix` rewrites of files this branch never touched, as in phase 1.

- [ ] **Step 2: Manual mpv check (needs a person at a desktop).**
  1. Start `go tool dev`.
  2. Make a playlist that holds a packaged YouTube video with captions and a podcast episode.
  3. Copy the skip link from its page and run:

     ```bash
     mpv --force-window --msg-level=demuxer=v,ffmpeg/demuxer=v '<copied M3U URL>'
     ```

  4. Check and record each of these:
     - Both entries play in order, and `>` moves to the next.
     - Silence is skipped in the packaged item, provided phase 4's edges exist.
     - The window title shows `<subscription> – <title>`.
     - **Subtitles:** press `j`, or run `mpv --sid=1 '<master URL>'`. Does mpv list and show the HLS WebVTT rendition from phase 2? Record yes or no, with the mpv and ffmpeg versions (`mpv --version`).
     - The downloaded launcher works: run `desktop-file-validate cog-*.desktop`, then `gio launch cog-*.desktop`.

- [ ] **Step 3: Update the design** (`DESIGN/plugins/feed_hls.md`):
  - Set the status line to say phase 6a is done, with the date.
  - In "Signed URLs", replace `/apps/feed/playlists/{id}.m3u?sig=…` with the real route, `/apps/feed/m3u/{user}/{kind}/{id}.m3u?sig=…[&skip=silence]`, and add the M3U signing message.
  - Add this plan's Decisions (entries, order, absolute base, launcher quoting).
  - Under "Open questions", replace the mpv subtitles question with the answer from Step 2. If the answer is no, record the follow-up: the M3U can't carry a per-entry `--sub-file`, so the launcher or an mpv script would have to fetch `subs` itself. That is not built here.
  - Keep "How to get progress back from external players" open.

- [ ] **Step 4: Update `TODO.md`.** Split the phase 6 line into "6a: M3U of a playlist or view for external players" (ticked, citing `TestM3U*` and `TestViewPagesOfferM3U`) and "6b: Android on Media3 HLS with background audio-only" (open). If Step 2's answer was no, add an open item for the mpv subtitle fallback.

- [ ] **Step 5: Commit**

```bash
git add DESIGN/plugins/feed_hls.md TODO.md
git commit -m "feed: HLS phase 6a in the design and TODO

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01R5HtZk5Zu21Yhwh8G6a4rX"
```
