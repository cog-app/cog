# Registration

During `Register()`, plugins use the `Registrar` interface to declare their capabilities.

## Pages

```go
RegisterPage(path string, handler http.Handler)
```

Mounts a GET handler at `/apps/{plugin_id}/{path}`. The handler renders an HTML content fragment -- the navigation bar, head, and scripts are wrapped automatically by middleware.

Use `{$}` as the path for the plugin's root page.

The request context contains the authenticated user and CSRF token:

```go
ctx.RegisterPage("{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    user := plugin.UserFromContext(r.Context())
    csrf := plugin.CSRFToken(r)
    // Render page content...
}))
```

## API endpoints

```go
RegisterAPI(path string, handler http.Handler)
```

Mounts a GET handler at `/apps/{plugin_id}/{path}` for JSON or HTMX partial responses. Auth-wrapped (returns 401 on failure, not a redirect).

## Actions

```go
RegisterAction(method, path string, handler http.Handler)
```

Mounts a state-changing handler (POST, PUT, or DELETE) at `/apps/{plugin_id}/{path}`. Automatically wrapped with auth and CSRF protection.

For form submissions, use the [Post-Redirect-Get pattern](../admin/cli.md): process the form, set a flash message, and redirect with 303.

## Navigation items

```go
RegisterNavItem(item NavItem)
```

Adds an entry to the top navigation bar.

```go
type NavItem struct {
    Label string  // Display text, e.g. "Files"
    URL   string  // Link target, e.g. "/apps/files"
    Icon  string  // Optional inline SVG markup
}
```

Nav items from disabled plugins are automatically excluded.

## Dashboard widgets

```go
RegisterWidget(w Widget)
```

Adds a widget to the dashboard.

```go
type Widget struct {
    Title    string
    Priority int  // Lower = rendered first
    Render   func(ctx context.Context, w io.Writer, userID string) error
}
```

The `Render` function writes HTML directly to the writer. Keep widget rendering fast -- there is no timeout enforcement.

## Hooks

```go
RegisterHook(hookName string, callback HookCallback)
```

Subscribe to named events. Hook callbacks receive a context and event data:

```go
type HookCallback func(ctx context.Context, data any)
```

## WebDAV handlers

```go
RegisterDAVHandler(prefix string, handler http.Handler)
```

Mounts a handler under `/remote.php/dav/{prefix}/`. The handler receives authenticated requests with the full path. Used for CalDAV, CardDAV, and custom DAV endpoints.

## Settings pages

```go
RegisterSettingsPage(page SettingsPage)
```

Adds an admin settings page for the plugin.

```go
type SettingsPage struct {
    Slug    string        // URL segment (empty for default page)
    Name    string        // Sidebar display name (empty uses plugin name)
    Handler http.Handler  // GET -- renders content fragment
    Action  http.Handler  // POST -- handles form submission (optional)
}
```

See [Settings Pages](settings.md) for details.

## CLI commands

```go
RegisterCommand(def CommandDef)
```

Registers a CLI command available as `cog <plugin-id> <command>`.

```go
type CommandDef struct {
    Name  string
    Short string
    Args  []string
    Run   func(ctx context.Context, args []string) error
    Sub   []CommandDef  // Subcommands (mutually exclusive with Run)
}
```

See [CLI Commands](cli-commands.md) for details.

## Search sources

```go
RegisterSearchSource(def SearchSourceDef)
```

Registers a database table for full-text indexing. See [Search Indexing](search.md) for details.

## Shareable resources

```go
RegisterResource(def ResourceDef)
```

Declares a resource type that users can share. See [Sharing Resources](sharing.md) for details.
