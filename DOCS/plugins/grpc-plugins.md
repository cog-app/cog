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
3. Your process serves `plugpb.PluginService` on that socket — in Go, `plugcmd.Run()` does this
4. Cog connects and calls `GetID` to learn the plugin ID
5. Cog calls `Register`, which streams registration events (routes, nav items, widgets, etc.)
6. HTTP requests to your routes are proxied over gRPC

The protocol is the contract, not the Go SDK: any language that can serve `plugin/plugpb/plugin.proto` over a Unix socket can be a plugin. The examples here are Go because `plugcmd` is what the tree ships.

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

A gRPC plugin is a process that serves `plugpb.PluginService` on the socket named by `COG_PLUGIN_SOCKET`. Nothing about that requires Go or an import of Cog; `plugcmd` is one implementation of the server side, and everything in this section applies to any other.

### What the host does

**It captures your output.** The host reads the process's stdout and stderr and re-emits them line by line through its own logger, attributed to your plugin ID — stdout at INFO, stderr at ERROR. A crash message therefore reaches the admin log viewer (Admin → Logs) and not just the host's stderr. Two consequences worth designing around: write ordinary logs to stdout, because anything on stderr is recorded as an error regardless of what you think its level is; and write a line at a time, because that is the unit the host logs. Structured output is not parsed — a JSON log line is recorded as its text.

**It restarts a process that exits.** The host waits on the process, and when it exits — cleanly or not — relaunches it on a fresh socket, backing off from 1s to a maximum of 30s between attempts, and keeps trying until the server shuts down. While the plugin is down its routes answer 503 with a short "temporarily unavailable" page instead of leaking a dial error to the browser. A request that fails for any other reason gets a 500. Either way the underlying status is logged, not rendered.

**It requires you to come back the same.** Routes are mounted on the host's mux once, at load, and cannot be remounted, so a restarted process must be the plugin the host already mounted routes for:

- `GetID` must return the same ID it returned before.
- `Register` is called again on the new connection, and must stream the same route keys, in the same order, as the first time. The host uses the stream to let you rebuild your own route table; it does not re-mount anything from it.

Generate route keys from something stable — the route itself, or a fixed registration order — not from a clock, a random source, or anything that varies with external state. A process that comes back with different keys is refused and the failure logged as a restart rejection, because the alternative is dispatching requests to whichever handler now happens to hold the old key.

**It does not detect a wedged plugin.** Only exits are noticed. A process that stays up and stops answering keeps its routes.

### What your process must do

**Do not let one request end the process.** A fault in a handler should be caught and returned as a gRPC error — `INTERNAL` is what the host renders as a 500 — rather than propagating out and terminating the server. This matters more here than in an ordinary service: the host holds the path of the socket you were listening on, so a process that dies mid-request takes every one of its routes down until the supervisor gets it back.

In Go, `plugcmd.Run` does this for you with recovery interceptors on both the unary and streaming methods. That covers panics raised while serving a request. It cannot cover a panic in a goroutine you started yourself — nothing outside that goroutine can recover it — so recover in your own goroutines. An implementation in another language needs the equivalent guard at its own service boundary.

**Expect to be killed, and to be orphaned.** The host kills the process when it shuts down cleanly. If the host itself is killed, nothing does: the plugin is left running with a socket directory under `/tmp` that no one will connect to again. Hold no state that a sudden exit would corrupt, and treat a fresh start as the normal case rather than the exceptional one.
