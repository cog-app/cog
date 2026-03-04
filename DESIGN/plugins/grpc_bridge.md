# gRPC Plugin Bridge

## Overview

External plugins run as separate processes and communicate with the Cog host over gRPC on a Unix domain socket. The plugin binary is the gRPC **server**; the Cog host is the **client**.

## Wire Protocol

Defined in `plugin/plugpb/plugin.proto`. Key RPCs:

| RPC                | Direction      | Purpose |
|--------------------|----------------|---------|
| `GetID`            | host → plugin  | Learn the plugin's unique ID |
| `Register`         | host → plugin  | Stream registration events (routes, hooks, widgets, nav items) back to host |
| `Start`            | host → plugin  | Signal plugin to begin background work |
| `Stop`             | host → plugin  | Signal graceful shutdown |
| `HandleHTTPRequest`| host → plugin  | Proxy an HTTP request to a registered handler |
| `RenderWidget`     | host → plugin  | Render a dashboard widget to HTML |

## Socket-Based Launching

1. Host creates a temp Unix socket path (`/tmp/cog-plugin-*/plugin.sock`).
2. Host launches the plugin binary with `COG_PLUGIN_SOCKET=<path>`.
3. Plugin binary calls `plugcmd.Run(myPlugin)`, which starts a gRPC server on that socket.
4. Host connects to the socket and calls `GetID` to learn the plugin ID.
5. Host calls `Register`, which streams `RegistrationEvent` messages back.
6. For each registration event, the host calls the corresponding `Registrar` method, wiring up proxy handlers for HTTP routes.

## HTTP Proxying

When a request hits a route registered by an external plugin:

1. Host serializes the `http.Request` into an `HTTPRequest` protobuf (method, path, query, headers, body, user context).
2. Host calls `HandleHTTPRequest` on the plugin's gRPC client.
3. Plugin dispatches to the captured `http.Handler` and returns an `HTTPResponse`.
4. Host writes the response headers, status, and body back to the original `http.ResponseWriter`.

## Plugin Author Usage

```go
package main

import (
    "git.duckfam.us/jj-software/cog/plugin"
    "git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type myPlugin struct{}

func (p *myPlugin) ID() string { return "myplugin" }
func (p *myPlugin) Register(r plugin.Registrar) error {
    r.RegisterNavItem(plugin.NavItem{Label: "My Plugin", URL: "/apps/myplugin"})
    r.RegisterPage("", http.HandlerFunc(handler))
    return nil
}
func (p *myPlugin) Start(ctx context.Context) error { return nil }
func (p *myPlugin) Stop() error { return nil }

func main() {
    plugcmd.Run(&myPlugin{})
}
```

Build the plugin as a standalone binary, then load it from the host via `Registry.LoadExternal("/path/to/myplugin")`.

## Limitations

- Hook callbacks are fire-and-forget over gRPC (no data is serialized to the plugin).
- Protected plugins (dashboard, files) are always in-process and cannot be loaded via gRPC.
- Per-user database access is not yet proxied; external plugins must use the API layer for data.

## Code Generation

Proto files are generated with `goprotoc` (no external `protoc` install required):

```bash
cd plugin/plugpb
go generate
```
