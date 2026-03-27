# Getting Started with Plugin Development

This guide walks through building a minimal Cog plugin from scratch.

## The Plugin interface

Every plugin implements four methods:

```go
type Plugin interface {
    ID() string
    Register(ctx PluginContext) error
    Start(ctx context.Context) error
    Stop() error
}
```

- **ID** -- unique string identifier (e.g. `"myplugin"`)
- **Register** -- declare routes, resources, nav items, widgets, etc.
- **Start** -- begin background work (called after all plugins register)
- **Stop** -- clean up on shutdown

## Minimal example

Create a new Go module for your plugin:

```bash
mkdir cog-hello && cd cog-hello
go mod init example.com/cog-hello
go get git.duckfam.us/jj-software/cog
```

Create `hello.go`:

```go
package hello

import (
    "context"
    "fmt"
    "net/http"

    "git.duckfam.us/jj-software/cog/plugin"
)

type Hello struct{}

func New() *Hello { return &Hello{} }

func (h *Hello) ID() string { return "hello" }

func (h *Hello) Register(ctx plugin.PluginContext) error {
    // Add a navigation entry
    ctx.RegisterNavItem(plugin.NavItem{
        Label: "Hello",
        URL:   "/apps/hello",
    })

    // Register a page at GET /apps/hello/
    ctx.RegisterPage("{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        user := plugin.UserFromContext(r.Context())
        fmt.Fprintf(w, "<h1>Hello, %s!</h1>", user.Username)
    }))

    // Register a POST action at POST /apps/hello/greet
    ctx.RegisterAction("POST", "greet", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("HX-Redirect", "/apps/hello")
        w.WriteHeader(http.StatusNoContent)
    }))

    return nil
}

func (h *Hello) Start(ctx context.Context) error { return nil }
func (h *Hello) Stop() error                     { return nil }
```

## Wire it into a custom build

Create `cmd/cog-custom/main.go`:

```go
package main

import (
    "git.duckfam.us/jj-software/cog"
    "example.com/cog-hello"
)

func main() {
    cog.Main(hello.New())
}
```

The built-in plugins (Dashboard, Files, Calendar, etc.) are automatically included. Your plugin is added alongside them.

Build and run:

```bash
go run ./cmd/cog-custom
```

Visit `http://localhost:8580` and you'll see "Hello" in the navigation bar.

## Adding a database

Most plugins need per-user data storage. Cog provides SQLite databases automatically.

Create `sql/schema.sql`:

```sql
CREATE TABLE IF NOT EXISTS greetings (
    id INTEGER PRIMARY KEY,
    message TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
```

Embed it and run migrations in Register:

```go
import "embed"

//go:embed sql
var sqlFS embed.FS

func (h *Hello) Register(ctx plugin.PluginContext) error {
    // Run schema migrations for each user's plugin DB
    // (migrations run lazily when PluginDB is first opened for a user)

    ctx.RegisterPage("{$}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        db, err := plugin.UserDB(r)
        if err != nil {
            http.Error(w, err.Error(), 500)
            return
        }
        plugin.Migrate(db, sqlFS)
        // Use db to query greetings...
    }))

    return nil
}
```

See [Databases](databases.md) for the full database guide.

## Adding a dashboard widget

```go
ctx.RegisterWidget(plugin.Widget{
    Title:    "Recent Greetings",
    Priority: 50,
    Render: func(ctx context.Context, w io.Writer, userID string) error {
        fmt.Fprintf(w, "<p>No greetings yet.</p>")
        return nil
    },
})
```

## Adding search

See [Search Indexing](search.md) for making your plugin's data searchable.

## Next steps

- [Plugin Interface](plugin-interface.md) -- full reference for Plugin and optional interfaces
- [Registration](registration.md) -- all Registrar methods
- [Databases](databases.md) -- per-user and root databases, migrations
- [Custom Builds](custom-builds.md) -- building custom Cog distributions
- [gRPC Plugins](grpc-plugins.md) -- building out-of-process plugins
