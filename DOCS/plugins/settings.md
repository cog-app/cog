# Settings Pages

Plugins can register admin settings pages for configuration.

## Registering a settings page

```go
ctx.RegisterSettingsPage(plugin.SettingsPage{
    Slug:    "",                        // empty = plugin's default page
    Name:    "",                        // empty = use plugin display name
    Handler: http.HandlerFunc(getPage), // GET renders content
    Action:  http.HandlerFunc(postPage), // POST handles form submission
})
```

## SettingsPage

```go
type SettingsPage struct {
    Slug    string       // URL path segment
    Name    string       // Sidebar display name
    Handler http.Handler // GET handler
    Action  http.Handler // POST handler (optional)
}
```

### Slug

The URL segment for the page. Empty string means this is the plugin's default settings page. Non-empty slugs create additional pages (e.g. `"recognition"` becomes `/admin/plugins/{id}/recognition`).

### Name

Display name in the settings sidebar. If empty, the plugin's display name is used.

### Handler

Renders the page content fragment on GET. The HTML shell (sidebar, navigation) is wrapped automatically.

### Action

Handles POST form submissions. Use the Post-Redirect-Get pattern: process the form, set a flash message, and redirect with 303.

## Sidebar navigation

The sidebar entry depends on how many pages the plugin registers:

- **Single page, no name** -- shows as just the plugin name (e.g. "RSS")
- **Single page, with name** -- shows as "Plugin: Name" (e.g. "RSS: Feeds")
- **Multiple pages** -- displayed as a nested group under the plugin name. A page with empty slug becomes the overview.

## Multiple settings pages

```go
// Overview page
ctx.RegisterSettingsPage(plugin.SettingsPage{
    Slug:    "",
    Name:    "",
    Handler: overviewHandler,
})

// Recognition settings
ctx.RegisterSettingsPage(plugin.SettingsPage{
    Slug:    "recognition",
    Name:    "Recognition",
    Handler: recognitionHandler,
    Action:  recognitionAction,
})
```

Settings pages are admin-only -- regular users cannot access them.
