# Custom Builds

You can build a custom Cog binary that includes additional plugins alongside the built-in ones.

## Using cog.Main

The simplest approach -- provides the full CLI (serve, user, group, volume, shell, doctor):

```go
package main

import (
    "git.duckfam.us/jj-software/cog"
    "example.com/my-plugin"
    "example.com/another-plugin"
)

func main() {
    cog.Main(
        myplugin.New(),
        anotherplugin.New(),
    )
}
```

The protected plugins (Dashboard and Files) are always included automatically. Your plugins are added alongside the built-in ones.

## Using cog.Run

For more control over the server lifecycle:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"

    "git.duckfam.us/jj-software/cog"
    "example.com/my-plugin"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    cfg := cog.Config{
        Addr:    ":8580",
        DBPath:  "/var/lib/cog/system.db",
        DataDir: "/var/lib/cog/data",
        BaseURL: "https://cloud.example.com",
    }

    if err := cog.Run(ctx, cfg, myplugin.New()); err != nil {
        log.Fatal(err)
    }
}
```

## Config

```go
type Config struct {
    Addr           string // HTTP listen address (e.g. ":8580")
    DBPath         string // Path to system.db
    DataDir        string // Root data directory
    BaseURL        string // Public URL
    LogFormat      string // "text" or "json"
    LogLevel       string // "debug", "info", "warn", "error"
    SSHAddr        string // SSH listen address (e.g. ":8522")
    SSHEnabled     bool   // Enable SSH/SFTP server
    SSHHostKeyPath string // SSH host key file
}
```

When using `cog.Main`, config is read from environment variables. When using `cog.Run`, you provide the config directly.

## The standard build

The default `cmd/cog/main.go` is a minimal custom build that includes all built-in plugins:

```go
package main

import (
    "git.duckfam.us/jj-software/cog"
    "git.duckfam.us/jj-software/cog/plugin/bookmarks"
    "git.duckfam.us/jj-software/cog/plugin/calendar"
    "git.duckfam.us/jj-software/cog/plugin/contacts"
    "git.duckfam.us/jj-software/cog/plugin/dashboard"
    "git.duckfam.us/jj-software/cog/plugin/files"
    "git.duckfam.us/jj-software/cog/plugin/gallery"
    "git.duckfam.us/jj-software/cog/plugin/notes"
    "git.duckfam.us/jj-software/cog/plugin/rss"
    "git.duckfam.us/jj-software/cog/plugin/tasks"
)

func main() {
    cog.Main(
        dashboard.New(),
        files.New(),
        calendar.New(),
        contacts.New(),
        notes.New(),
        rss.New(),
        gallery.New(),
        bookmarks.New(),
        tasks.New(),
    )
}
```

You can use this as a template and add your own plugins to the list.
