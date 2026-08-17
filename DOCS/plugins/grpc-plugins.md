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

gRPC plugins run in separate processes, so a plugin that dies cannot take the Cog server with it.

**Panics are contained.** `plugcmd.Run` installs gRPC recovery interceptors: a panic in one of your handlers is logged with its stack, returned to the host as `codes.Internal`, and rendered to the user as a 500. The process keeps serving its other routes. This does **not** cover panics in goroutines you start yourself — nothing outside the goroutine can recover those, and one will kill the plugin. Recover in your own goroutines.

**A dead process is restarted.** The host reaps the process and relaunches it on a fresh socket, backing off from 1s to a maximum of 30s between attempts, and keeps trying until the server shuts down. While the plugin is down its routes answer 503 with a short "temporarily unavailable" page rather than leaking a dial error to the browser.

Routes are mounted on the host's mux once, at load, and cannot be remounted. A restart therefore re-runs `Register` against the plugin process only, to rebuild its own route table, and checks that the route keys match the ones the host already mounted. Register the same things in the same order every time: a plugin whose registrations depend on external state can come back with different route keys, and the host will refuse to adopt it (logged as "restart rejected") instead of routing requests to the wrong handlers.

**Output is captured.** The host reads your stdout and stderr and re-emits them line by line through its own logger, attributed to your plugin ID — stdout at INFO, stderr at ERROR. That means a panic trace shows up in the admin log viewer (Admin → Logs) and not only on the host's stderr. Note the level mapping: if you log with slog's default handler, which writes to stderr, your INFO lines are recorded as ERROR. Write ordinary logs to stdout if that matters to you.
