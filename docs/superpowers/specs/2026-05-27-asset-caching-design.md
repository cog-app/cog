# General Static-Asset Caching — Design

**Date:** 2026-05-27
**Status:** Approved (design)
**Scope:** Core web server (`internal/web`, `ui`) + plugin registrar (`internal/plugin`) + feed plugin migration.

## Problem

Static assets are served without cache-busting, so browsers serve stale copies after a deploy:

- Core assets (`/static/style.css`, `htmx.min.js`, `cog-share.js`, `vendor/*`) are served by `http.FileServerFS` (`internal/web/routes.go:58`) with only `ETag`/`Last-Modified` (heuristic ~1h caching), no `Cache-Control`, no versioning. A CSS/JS change can be invisible to returning users for up to an hour.
- The feed plugin recently hit exactly this with `player.js` and worked around it with a bespoke content-hash query string (`plugin/feed/feed.go`: `playerJSVersion`, `playerAssetURL`, `serveStatic`). That fix is correct but local — every other asset, and every future plugin, has the same gap.

There is no shared mechanism: each asset path is hardcoded in templates, and only feed does versioning.

## Goals

- One reusable way to serve embedded assets with content-hash cache-busting.
- Versioned URLs (`/path?v=<hash>`) emitted from templates instead of hardcoded paths.
- `Cache-Control: public, max-age=31536000, immutable` for hash-matched requests; safe revalidation otherwise.
- Apply it to core assets and provide a registrar helper plugins use.
- Migrate feed off its bespoke hash to the shared mechanism (proves the API, removes duplication).

## Non-Goals

- No hashed *filenames* (`style.<hash>.css`) — query-string versioning only.
- No global build-version stamp — per-asset content hashing only.
- No special dev-mode cache disabling — a rebuild changes embedded bytes → new hash → new URL, which already busts the cache in `go tool dev`.
- No CDN/upstream-proxy configuration changes.
- No conversion of inlined plugin `<style>`/`<script>` blocks to served files (out of scope; only assets already served as files are covered, plus anything a plugin opts to serve later).

## Decisions (from brainstorming)

- **Scope:** core + shared plugin helper.
- **Versioning:** content-hash query string (`?v=<sha256[:12]>`), same file path.
- **Cache policy:** immutable 1-year for hash-matched requests.
- **Dev:** rely on hashing (no separate dev path).
- **Code location:** new `internal/assets` package (implementation); plugins obtain it via a registrar convenience on `PrivilegedContext`. Not placed in `ui` (keeps presentation vs. infra layering clean).

## Architecture

```
internal/assets        ← reusable Assets type (hashing + cache-aware handler)
   ▲            ▲
   │            │
ui (var Static) │       ← core assets, used by ui/layout.templ
   ▲            │
internal/web    internal/plugin (RegisterAssets on PrivilegedContext)
(mounts /static)        ▲
                        │
                   plugin/feed (migrates off bespoke hash)
```

One implementation, three consumers: core shell, the registrar, and (via the registrar) the feed plugin.

## Section 1 — `internal/assets` package

A small, self-contained unit.

```go
package assets

// Assets serves files from an embedded (or any) fs.FS with content-hash-based
// cache busting. Hashes are computed once at construction.
type Assets struct {
	fsys       fs.FS
	mountPrefix string            // e.g. "/static" or "/apps/feed/static" (no trailing slash)
	hashes     map[string]string  // relpath -> sha256 hex (full); URL uses a 12-char prefix
	modTime    time.Time          // construction time, for Last-Modified
}

// New walks fsys, computing a SHA-256 for every file. mountPrefix is the URL
// path the handler is mounted under (used only to build URLs). Returns an error
// if the walk or any read fails.
func New(fsys fs.FS, mountPrefix string) (*Assets, error)

// URL returns the versioned URL for name (a path relative to fsys root), e.g.
// "/static/style.css?v=1a2b3c4d5e6f". For an unknown name it returns the bare
// path (mountPrefix/name) and logs a warning — a typo degrades to "uncached",
// never a panic.
func (a *Assets) URL(name string) string

// ServeHTTP serves the asset named by r.URL.Path (i.e. mount the handler under
// http.StripPrefix(mountPrefix+"/", a)). Path may include subdirectories
// (e.g. "vendor/fullcalendar.min.js").
func (a *Assets) ServeHTTP(w http.ResponseWriter, r *http.Request)
```

**Serve logic (`ServeHTTP`):**
1. `name := path.Clean(r.URL.Path)` (already stripped of the mount prefix by the caller). Reject `..`/absolute escapes → 404.
2. Look up `hash, ok := a.hashes[name]`; `!ok` → `http.NotFound`.
3. Read bytes from `fsys` (or a cached `[]byte`; see note). On read error → 500 + slog.
4. `w.Header().Set("Content-Type", contentType(name))` — `mime.TypeByExtension`, with an explicit `text/javascript; charset=utf-8` for `.js` and `text/css; charset=utf-8` for `.css` (don't trust the platform mime table).
5. `w.Header().Set("ETag", strconv.Quote(hash[:12]))`.
6. If `r.URL.Query().Get("v") == hash[:12]` → `Cache-Control: public, max-age=31536000, immutable`. Else → `Cache-Control: no-cache` (revalidate; the ETag still yields a 304).
7. `http.ServeContent(w, r, name, a.modTime, bytes.NewReader(data))` — handles `If-None-Match`/`If-Modified-Since` (304) and range requests.

**Hashing & memory:** `New` reads each file once to hash it. Embedded asset sets are small (core is ~75KB total), so `New` also retains the bytes in a `map[string][]byte` to avoid re-reading `fsys` per request. (If an `Assets` is ever pointed at a large/dynamic FS this would need revisiting; document the assumption.)

**Errors:** `New` wraps and returns walk/read errors. `ServeHTTP` logs read errors via `slog` and returns 500. `URL` logs unknown names. No swallowing.

## Section 2 — Core wiring (`ui` + `internal/web`)

- `ui` package: add `var Static *assets.Assets`, constructed once from the embedded static sub-FS:
  ```go
  // ui/static.go (or a new ui/assets.go)
  var Static = func() *assets.Assets {
  	sub, err := fs.Sub(StaticFS, "static")
  	if err != nil { panic("ui: sub static FS: " + err.Error()) }
  	a, err := assets.New(sub, "/static")
  	if err != nil { panic("ui: build static assets: " + err.Error()) }
  	return a
  }()
  ```
  (Startup-fatal on a broken embed is acceptable — it's a build/packaging error, caught immediately.)
- `internal/web/routes.go:57-58`: replace
  ```go
  staticSub, _ := fs.Sub(ui.StaticFS, "static")
  mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticSub)))
  ```
  with
  ```go
  mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Static))
  ```
  Still no auth wrapper (login page must load CSS unauthenticated — confirmed `/static` and `/login` are mounted without auth).
- `ui/layout.templ` `<head>` (lines ~19–22) and the login/MFA/register `<head>` blocks (~lines 286–289 in generated, plus the auth-page templates): replace hardcoded `href="/static/style.css"` / `src="/static/htmx.min.js"` / `src="/static/cog-share.js"` with `ui.Static.URL("style.css")` etc. Do the same for vendor assets (`vendor/fullcalendar.min.js`, `vendor/sortable.min.js`) wherever referenced. Regenerate templ.

## Section 3 — Plugin integration + feed migration

- `internal/plugin/plugin.go` `PrivilegedContext` interface: add
  ```go
  // RegisterAssets mounts an embedded asset FS at /apps/{plugin_id}/static/ and
  // returns a helper for building versioned, cache-busted URLs to those assets.
  RegisterAssets(fsys fs.FS) (*assets.Assets, error)
  ```
- `internal/plugin/registry.go` `pluginRegistrar`: implement it —
  ```go
  func (pr *pluginRegistrar) RegisterAssets(fsys fs.FS) (*assets.Assets, error) {
  	prefix := "/apps/" + pr.pluginID + "/static"
  	a, err := assets.New(fsys, prefix)
  	if err != nil {
  		return nil, fmt.Errorf("register assets for %s: %w", pr.pluginID, err)
  	}
  	pr.mux.Handle("GET "+prefix+"/", pr.wrap(auth.RequireAuth(http.StripPrefix(prefix+"/", a))))
  	return a, nil
  }
  ```
  (Auth-wrapped, matching how `RegisterAPI` behaves today; plugin assets are referenced from authed pages. `pr.wrap` is the same outer wrapper other registrations use.)
- **Feed migration** (`plugin/feed/feed.go`):
  - Delete `playerJSVersion`, `playerAssetURL`, `serveStatic`, the `crypto/sha256`/`encoding/hex` imports, and the `ctx.RegisterAPI("static/{file}", ...)` registration.
  - Keep `//go:embed static` + `var staticFS embed.FS`.
  - Add a package-level `var pkgAssets *assets.Assets`. In `Register`, where the privileged context is already obtained for the NC News API, also do:
    ```go
    if priv, ok := ctx.(plugin.PrivilegedContext); ok {
    	sub, err := fs.Sub(staticFS, "static")
    	if err != nil { return fmt.Errorf("feed: sub static: %w", err) }
    	a, err := priv.RegisterAssets(sub)
    	if err != nil { return err }
    	pkgAssets = a
    	// ... existing priv.RegisterRoute(newsAPI) ...
    }
    ```
  - Keep the existing `func playerAssetURL() string` signature (the `feedPlayer` templ already calls it — no templ threading needed), but reimplement it:
    ```go
    func playerAssetURL() string {
    	if pkgAssets != nil {
    		return pkgAssets.URL("player.js")
    	}
    	return "/apps/feed/static/player.js" // unversioned fallback if assets weren't registered
    }
    ```
    Resulting URL shape is unchanged (`/apps/feed/static/player.js?v=<hash>`), now from the shared path. Using a package var (not a `Plugin` field) matches the existing `playerAssetURL` being a package func and avoids threading the URL through every `feedPlayer` call site.
  - The fallback covers a non-privileged context (e.g. a test harness that isn't a `PrivilegedContext`). Verify `cogtest` provides a `PrivilegedContext` so the versioned route is actually mounted in feed tests; if it does, tests exercise the real path and the fallback is just defensive.

## Data flow

```
startup:  embed FS ──assets.New──▶ {relpath: hash, bytes}
render:   templ calls a.URL("x.js") ──▶ "/.../x.js?v=<hash>"
request:  GET /.../x.js?v=<hash>
             ├ v matches → Cache-Control: immutable, 1y  + ETag
             └ v missing/stale → Cache-Control: no-cache + ETag (→ 304 on revalidate)
```

## Error handling

- `assets.New` failure at core init → panic (startup-fatal, packaging bug). At plugin `Register` → returned error (plugin fails to load, logged by registry).
- `ServeHTTP` read error → 500 + `slog.Error`. Unknown asset → 404.
- `URL` unknown name → bare path + `slog.Warn` (degrades to uncached, never panics).
- Per project rule, no swallowed errors.

## Testing

- **`internal/assets` unit tests** (use a small `fstest.MapFS`):
  - `New` computes a stable hash; same input → same `URL`.
  - `URL` shape: `<prefix>/<name>?v=<12 hex>`; unknown name → bare path.
  - Handler: `?v` matching hash → `Cache-Control: ...immutable`; `?v` absent/stale → `no-cache`; both set `ETag`.
  - `If-None-Match` with the current ETag → `304`.
  - Unknown file → `404`. Subdirectory asset (`vendor/x.js`) served correctly.
  - `Content-Type` correct for `.css`/`.js`/`.svg`.
- **Core (cogtest):** a rendered shell page's `<head>` contains `/static/style.css?v=`; `GET` that URL → `200` + immutable `Cache-Control`; the unauthenticated `GET /login` page still serves and references the versioned CSS.
- **Feed:** update `TestServePlayerJS` to fetch the registrar-mounted `/apps/feed/static/player.js` (optionally with `?v=`) → `200` + js content-type; player templ tests assert `player.js?v=` appears. Remove assertions tied to the deleted `serveStatic`/`{file}` route if any.
- Full `go tool verify` green (incl. the browser snapshot, which may need `WEBTEST_UPDATE=1` if head asset URLs change the snapshot — update if so).

## Docs

`DESIGN/frontend.md`: add a short "Static assets & caching" section — embed the FS, call `RegisterAssets` (plugins) or use `ui.Static` (core), reference assets via `.URL(name)` in templates, never hardcode `/static/...` paths. Note the immutable-cache + content-hash contract.
