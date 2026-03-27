# CLI Commands

Plugins can register commands that appear under `cog <plugin-id>` in the CLI.

## Registering a command

```go
ctx.RegisterCommand(plugin.CommandDef{
    Name:  "scan",
    Short: "scan all feeds for new articles",
    Run: func(ctx context.Context, args []string) error {
        // Perform the scan...
        return nil
    },
})
```

This creates `cog rss scan` (assuming the plugin ID is `"rss"`).

## CommandDef

```go
type CommandDef struct {
    Name  string                                        // Command name
    Short string                                        // One-line description
    Args  []string                                      // Positional argument names
    Run   func(ctx context.Context, args []string) error // Handler
    Sub   []CommandDef                                  // Subcommands
}
```

### Positional arguments

Specify expected arguments by name. The CLI enforces the exact count:

```go
plugin.CommandDef{
    Name:  "import",
    Short: "import bookmarks from a file",
    Args:  []string{"filename"},
    Run: func(ctx context.Context, args []string) error {
        filename := args[0]
        // ...
    },
}
```

Usage: `cog bookmarks import bookmarks.html`

### Subcommands

Use `Sub` for command groups. `Run` and `Sub` are mutually exclusive:

```go
plugin.CommandDef{
    Name:  "feeds",
    Short: "manage feeds",
    Sub: []plugin.CommandDef{
        {
            Name:  "list",
            Short: "list all feeds",
            Run:   listFeeds,
        },
        {
            Name:  "add",
            Short: "add a new feed",
            Args:  []string{"url"},
            Run:   addFeed,
        },
    },
}
```

Usage: `cog rss feeds list`, `cog rss feeds add https://example.com/feed.xml`
