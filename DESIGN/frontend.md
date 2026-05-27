# Frontend Design

## Tech Stack

- **HTMX:** For dynamic interactions without heavy client-side JavaScript.
- **Templ:** For type-safe, compiled Go templates.
- **Vanilla CSS:** Clean, lightweight styling (no heavy frameworks like Bootstrap/Tailwind unless necessary).

## Component Library

Cog exports a Go package `git.duckfam.us/jj-software/cog/ui` containing `templ` components. This ensures plugins look native.

### Usage Example (Go/Templ)

```go
package myplugin

import "git.duckfam.us/jj-software/cog/ui"

templ ShowWidget(data string) {
    @ui.PageLayout("My Widget") {
        @ui.Card() {
            <h1>{ data }</h1>
            @ui.Button("primary", "Click Me")
        }
    }
}
```

## Routing & Security

Cog uses a structured routing system to balance ease of use with security (CSRF protection).

### Route Patterns

- **APIs (`/api/{plugin_id}/*`):**
  - **Purpose:** Data exchange, mobile apps, third-party integrations.
  - **Format:** Typically JSON.
  - **CSRF:** Bypassed. Auth is handled via Bearer tokens or App Passwords.
- **Pages (`/apps/{plugin_id}/*`):**
  - **Purpose:** User-facing HTML interfaces.
  - **Format:** HTML (rendered via `templ` or Go templates).
  - **CSRF:** GET requests are safe; forms within pages must include a CSRF token for the plugin.
  - **Optimization:** Can use server-side Go templates with direct SQLite access to minimize round-trips for dynamic data.
- **Actions:**
  - **Method:** POST, PUT, DELETE.
  - **Purpose:** State-changing operations (form submissions).
  - **Format:** HTMX fragments.
  - **CSRF:** Strictly enforced. Tokens are namespaced per plugin.
- **Resources (`/apps/{plugin_id}/{resource_name}/{resource_id}`):**
  - **Purpose:** Direct linking to specific plugin objects.
  - **Use Cases:** Global search results, public shares, notifications.
- **Compatibility/Global Routes (`/remote.php/*`, `/index.php/*`, `/.well-known/*`):**
  - **Purpose:** NextCloud protocol/client compatibility.
  - **Binding:** Host or protected/privileged plugins; not the default plugin route model.
  - **CSRF:** Token-authenticated non-browser requests are exempt.

### CSRF Implementation

1.  **Namespacing:** CSRF tokens are scoped per-plugin via `HMAC-SHA256(secret, sessionID || 0x00 || pluginID)`. A single global CSRF secret produces unique tokens for each plugin, so a token leaked from one plugin cannot be used against another.
2.  **Middleware:** `RegisterAction()` automatically wraps handlers with plugin-scoped CSRF validation. Non-plugin routes (settings, admin, logout) use the global CSRF token (`HMAC(secret, sessionID)` without a plugin ID).
3.  **HTMX Integration:** HTMX requests automatically include the CSRF token in the `X-CSRF-Token` header via a global event listener configured in the main layout. Plugin pages must use `auth.CSRFTokenFor(sessionID, pluginID, secret)` to generate their scoped token.

## NextCloud Compatibility

To support NextCloud apps (which often expect specific DOM structures or endpoints), Cog provides a "Legacy/Compat" layer.

- **OCS API:** Implements Open Collaboration Services API responses.
- **WebDAV:** Standard implementation for file browsing.
- **Theme:** CSS variables will attempt to match standard NC variables where possible to allow simple NC apps to render decently, though perfect visual parity is secondary to Cog's own design.

## Shared Concepts & Types

UI and route handlers should use the canonical architecture terms and shared data shape outlines documented in `DESIGN/concepts_and_types.md`.

## Routing

- `/`: Dashboard (plugin, shows app grid + widgets)
- `/apps/{plugin_id}/*`: Plugin routes (pages, actions, resources)
- `/settings`: User settings (profile, avatar, password, storage)
- `/admin/`: Admin area (users, groups, volumes, plugins)
- `/avatar/{id}`: User avatar image (falls back to SVG initials)

## Navigation

The navbar is dynamically built from plugin-registered nav items via `Registry.NavItems()`. Plugins call `RegisterNavItem()` during registration to add entries. The default setup produces:

- **Dashboard** — registered by the dashboard plugin
- **Files** — registered by the files plugin

Settings and Admin links appear only in the user dropdown menu (top-right), not in the main navbar.

### User Dropdown

The dropdown toggle displays the user's avatar (small circle) and display name. The menu contains:
- Settings link
- Admin link (if user is admin)
- Logout form

### Avatars

- Uploaded via `/settings/avatar` (JPEG or PNG)
- Served from `/avatar/{user_id}` — the handler returns the uploaded image or generates an SVG with the user's initials and a deterministic background color.
- Stored on disk at `{dataDir}/avatars/{user_id}.ext`
- Filename tracked in `users.avatar` column

## Dashboard & Widgets

The dashboard plugin renders a grid of app cards (one per plugin nav item, excluding dashboard itself) followed by widget sections. Plugins register widgets during `Register()`:

```go
registrar.RegisterWidget(plugin.Widget{
    Title:    "Recent Files",
    Priority: 10, // lower = higher on page
    Render:   func(ctx context.Context, w io.Writer, userID string) error { ... },
})
```

Widgets are sorted by priority and rendered server-side inside `ui.Card` wrappers.

## Plugin Settings Pages

Plugins register settings pages via `RegisterSettingsPage()`. Each page is automatically wrapped in the standard settings layout (sidebar + content area).

**Sidebar navigation rules:**
- The sidebar entry always prefixes with the plugin's name.
- **Single page, no name**: Shows as just "Plugin Name" (e.g., "RSS").
- **Single page, with name**: Shows as "Plugin Name: Page Name" (e.g., "RSS: Feeds").
- **Multiple pages**: Nested group under the plugin name.
  - A page with an empty name becomes the overview page (shown when clicking the parent).
  - If no page has an empty name, the parent item is not clickable (only expands to show children).

## Plugin Enable/Disable

Admins manage plugins at `/admin/plugins`. Disabling a plugin:
- Removes its nav items from the navbar
- Excludes its widgets from the dashboard
- Routes automatically 404 via `RequirePluginEnabled()` middleware (applied by all `Register*` methods)

Protected plugins (dashboard, files) cannot be disabled.

## Static Assets & Caching

Static files (CSS, JS, fonts, images) are served with **content-hash cache
busting** via the `internal/assets` package. Asset URLs carry a `?v=<hash>`
query string; matching requests get `Cache-Control: public, max-age=31536000,
immutable`, while unversioned/stale requests get `no-cache` (revalidated via
ETag). A rebuild changes the embedded bytes → new hash → new URL, so deploys are
never served stale.

**Rules:**
- **Never hardcode `/static/...` paths in templates.** Reference assets through a
  helper's `URL(name)` method so they get versioned.
- **Core assets** (`ui/static/`): use `ui.Static.URL("style.css")` in templates.
  The handler is mounted at `GET /static/` in `internal/web/routes.go`.
- **Plugin assets**: embed a `static/` dir, then in `Register` (privileged
  context) call `priv.RegisterAssets(subFS)` — it mounts `/apps/{id}/static/`
  and returns an `*assets.Assets`. Reference files via that helper's `URL(name)`.
  See `plugin/feed` for the reference implementation.
