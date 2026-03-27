# Plugin Interface Reference

## Plugin

The core interface every plugin must implement.

```go
type Plugin interface {
    ID() string
    Register(ctx PluginContext) error
    Start(ctx context.Context) error
    Stop() error
}
```

### ID

Returns the plugin's unique identifier. Used for route prefixes (`/apps/{id}/`), database paths, CLI namespacing, and resource type prefixes.

Convention: lowercase, alphanumeric, no spaces (e.g. `"files"`, `"calendar"`, `"bookmarks"`).

### Register

Called during startup. Use the `PluginContext` (which embeds `Registrar`) to declare routes, resources, nav items, widgets, search sources, and hooks. Also access services like `PluginDB()`, `Logger()`, and `ShareStore()`.

### Start

Called after all plugins have registered. Use this for background goroutines (polling, scanning, etc.). The context is cancelled on shutdown.

### Stop

Called during shutdown for cleanup. Return after all background work has stopped.

## Optional interfaces

### Doctor

Contribute health checks to `cog doctor`.

```go
type Doctor interface {
    DoctorChecks(ctx context.Context) []CheckResult
}

type CheckResult struct {
    Name    string
    Status  CheckStatus  // CheckOK, CheckWarn, or CheckError
    Message string
}
```

### FolderIconProvider

Provide custom folder icons in the file browser.

```go
type FolderIconProvider interface {
    FolderIcon(username, folderPath string) (icon string, ok bool)
}
```

Return inline SVG markup for folders your plugin manages (e.g. the Gallery plugin might show a camera icon for "Photos/").

## Lifecycle

1. **Load** -- plugin struct is created (e.g. `New()`)
2. **Register** -- plugin declares its routes, resources, and hooks
3. **Start** -- plugin begins background work
4. **Run** -- plugin serves requests
5. **Stop** -- plugin shuts down gracefully
