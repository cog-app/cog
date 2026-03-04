# Dashboard Plugin

## Overview

The dashboard is a **privileged internal plugin** (`dashboard`) that serves as the root page (`/`). It aggregates navigation links and widgets registered by other plugins into a single landing page.

## Plugin Identity

- **ID:** `dashboard`
- **Kind:** Privileged Internal
- **Protected:** Yes (cannot be disabled by admins)
- **Package:** `internal/plugin/dashboard`

## Registration

During `Register()`, the dashboard plugin adds a single nav item:

```go
registrar.RegisterNavItem(plugin.NavItem{Label: "Dashboard", URL: "/"})
```

It does not register any resources, hooks, or APIs.

## Routing

The dashboard does not use `RegisterPage` for its route. Instead, `server.go` mounts the dashboard handler directly at `GET /{$}` because the root path falls outside the standard `/apps/{plugin_id}/` prefix.

## Page Rendering

The dashboard page displays:

1. **App Grid** — Cards linking to each enabled plugin's nav items (excluding dashboard itself). Each card shows the plugin's label and links to its URL.
2. **Widgets** — Rendered below the app grid in priority order (lowest priority number first). Each widget is a `plugin.Widget` registered by another plugin.
3. **Fallback** — If no apps or widgets are registered, shows a simple "You are logged in as {username}" message.

## Data Dependencies

The dashboard receives a `WidgetProvider` interface at construction:

```go
type WidgetProvider interface {
    Widgets() []plugin.Widget
    NavItems() []plugin.NavItem
    IsEnabled(string) bool
}
```

The `plugin.Registry` satisfies this interface.

## Widget System

Plugins register dashboard widgets during `Register()`:

```go
registrar.RegisterWidget(plugin.Widget{
    Title:    "Recent Files",
    Priority: 10,
    Render:   func(ctx context.Context, w io.Writer, userID string) error { ... },
})
```

### Widget Type

```go
type Widget struct {
    Title    string
    Priority int // lower = rendered first on the dashboard
    Render   func(ctx context.Context, w io.Writer, userID string) error
}
```

- **Title** — Displayed as the card heading wrapping the widget content.
- **Priority** — Controls ordering. Widgets are sorted ascending; ties are resolved by registration order.
- **Render** — Called at request time with the authenticated user's ID. Writes HTML directly to the response writer. Errors are logged but do not break the page.

### Design Considerations

- Widgets render server-side on each page load. There is no client-side refresh mechanism (plugins can add HTMX polling attributes in their rendered HTML if needed).
- Widget rendering has no timeout enforcement currently. Plugins should keep widget rendering fast.
- Widgets receive only the user ID, not a full user context. If a widget needs database access, the plugin should capture the necessary dependencies in a closure.

## Nav Item System

Plugins register navigation entries that appear in the top navbar:

```go
registrar.RegisterNavItem(plugin.NavItem{
    Label: "Files",
    URL:   "/apps/files",
    Icon:  "",  // optional CSS class
})
```

### NavItem Type

```go
type NavItem struct {
    Label string
    URL   string
    Icon  string // optional CSS class, currently unused in default theme
}
```

- Nav items from disabled plugins are excluded from the navbar.
- The dashboard collects nav items via `Registry.NavItems()` which iterates plugins in registration order and skips disabled ones.
- The active state is determined by URL prefix matching against the current request path.

## Plugin Enable/Disable

Admins can enable or disable plugins via `/admin/plugins`. The system stores state in a `plugin_settings` table:

```sql
CREATE TABLE IF NOT EXISTS plugin_settings (
    plugin_id TEXT PRIMARY KEY,
    enabled INTEGER NOT NULL DEFAULT 1
);
```

- **Default:** All plugins are enabled unless explicitly disabled.
- **Protected plugins** (dashboard, files) cannot be disabled. The admin UI hides or greys out their toggle.
- When a plugin is disabled:
  - Its nav items are removed from the navbar.
  - Its widgets are excluded from the dashboard.
  - All routes registered via `RegisterPage`, `RegisterAPI`, `RegisterAction`, and `RegisterResource` are automatically wrapped with `RequirePluginEnabled()` middleware to return 404.

## User Avatars

While not part of the dashboard plugin itself, the avatar system integrates with the navbar layout that the dashboard (and all pages) share:

- Each user's avatar is displayed in the dropdown toggle.
- Avatars are served from `/avatar/{user_id}` with automatic fallback to an SVG initials image.
- Upload/delete is managed via `/settings/avatar`.
- Avatar filenames are stored in the `users.avatar` column.
