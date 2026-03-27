# Plugin System Overview

Cog's plugin system allows developers to extend the platform with new apps, protocols, and integrations. Plugins can register web pages, API endpoints, dashboard widgets, navigation items, CLI commands, search sources, shareable resources, and WebDAV handlers.

## Trust tiers

Plugins fall into four trust tiers:

| Tier | Language | Isolation | Route scope | Disable | Examples |
|------|----------|-----------|-------------|---------|----------|
| **Protected** | Go (in-process) | None | Global | No | Dashboard, Files |
| **Privileged** | Go (in-process) | None | Global | Yes | Calendar, Contacts |
| **Trusted** | Go or gRPC | Process (gRPC) | `/apps/{id}/` | Yes | 3rd-party plugins |
| **External** | Any (gRPC) | Process + iframe | `/apps/{id}/` | Yes | User-installed |

- **Protected** plugins are always on and cannot be disabled by admins.
- **Privileged** plugins run in-process and can bind routes outside their `/apps/{id}/` prefix.
- **Trusted** plugins can be Go (compiled in) or gRPC (separate process). Routes are scoped to their prefix.
- **External** plugins run as separate processes and are sandboxed.

## Package layout

```
plugin/              # Public API -- import this to write plugins
  plugin.go          # Plugin, Registrar interfaces and types
  context.go         # PluginContext interface (service accessors)
  helpers.go         # Helper functions (CSRFToken, UserDB, WriteJSON, etc.)
  migrate.go         # Database migration helper
  plugpb/            # gRPC proto definition and generated code
  plugcmd/           # plugcmd.Run() helper for external plugin binaries
```

The module path is `git.duckfam.us/jj-software/cog`. Import the plugin package as:

```go
import "git.duckfam.us/jj-software/cog/plugin"
```

## Next steps

- [Getting Started](getting-started.md) -- build your first plugin
- [Plugin Interface](plugin-interface.md) -- full interface reference
- [Registration](registration.md) -- routes, nav items, widgets, and more
