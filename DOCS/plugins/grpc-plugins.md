# gRPC Plugins

External plugins run as separate processes and communicate with Cog over gRPC on a Unix socket.

## Building a gRPC plugin

Create a standard Go binary that calls `plugcmd.Run()`:

```go
package main

import (
    "context"
    "fmt"
    "net/http"

    "git.duckfam.us/jj-software/cog/plugin"
    "git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type myPlugin struct{}

func (p *myPlugin) ID() string { return "myplugin" }

func (p *myPlugin) Register(ctx plugin.PluginContext) error {
    ctx.RegisterNavItem(plugin.NavItem{
        Label: "My Plugin",
        URL:   "/apps/myplugin",
    })
    ctx.RegisterPage("{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        fmt.Fprintf(w, "<h1>Hello from gRPC plugin!</h1>")
    }))
    return nil
}

func (p *myPlugin) Start(ctx context.Context) error { return nil }
func (p *myPlugin) Stop() error                     { return nil }

func main() {
    plugcmd.Run(&myPlugin{})
}
```

Build it as a standalone binary:

```bash
go build -o myplugin ./cmd/myplugin
```

## How it works

1. Cog creates a temporary Unix socket
2. Cog launches your binary with `COG_PLUGIN_SOCKET` set to the socket path
3. Your binary calls `plugcmd.Run()`, which starts a gRPC server on that socket
4. Cog connects and calls `GetID` to learn the plugin ID
5. Cog calls `Register`, which streams registration events (routes, nav items, widgets, etc.)
6. HTTP requests to your routes are proxied over gRPC

## Supported registrations

The following work over gRPC:

- `RegisterPage` -- pages are proxied as HTTP requests
- `RegisterAPI` -- API endpoints are proxied
- `RegisterAction` -- actions are proxied with CSRF handling
- `RegisterNavItem` -- navigation entries
- `RegisterWidget` -- dashboard widgets (rendered via gRPC call)
- `RegisterHook` -- hook subscriptions (fire-and-forget)
- `RegisterResource` -- shareable resources

## Limitations

The following are **not available** to gRPC plugins:

| Feature | Reason |
|---------|--------|
| `PluginDB()` | Database access is not proxied over gRPC |
| `RootDB()` | Same |
| `UserFilesDir()` | Filesystem paths are not meaningful across processes |
| `UserVFS()` | VFS is not proxied |
| `RegisterDAVHandler()` | No-op for gRPC plugins |
| `RegisterSettingsPage()` | No-op for gRPC plugins |
| `RegisterCommand()` | No-op for gRPC plugins |
| `RegisterSearchSource()` | Not yet supported over gRPC |
| `ShareStore()` | Returns nil |
| `EmailSender()` | Returns nil |
| `CSRFSecret()` | Returns nil |

gRPC plugins should use their registered API/action routes for all data access, or manage their own local storage.

## Installation

Place compiled plugin binaries in `/var/lib/cog/plugins/`. The Cog host discovers and loads them via `Registry.LoadExternal()`.

## Process isolation

gRPC plugins run in separate processes. If a plugin crashes, the main Cog server continues running. Plugin routes will return errors until the plugin is restarted.
