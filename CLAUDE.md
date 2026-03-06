# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Cog?

Cog (Cloud On Go) is a Go-based file storage and plugin platform with NextCloud API compatibility. It uses SQLite for persistence and serves HTTP (including WebDAV) on port 8580.

## Commands

```bash
# Run dev server (builds, creates admin:admin user, enables request logging)
go tool dev
go tool dev -reset   # wipe tmp/dev state first
go tool templ fmt .  # format all templ files (don't bother doing this by hand)

# Run code generators
go generate ./...

# Run all tests
go test ./...

# Run a single test
go test -run TestNCLoginFlow ./internal/web

# Run tests in a specific package
go test ./internal/web
```

Performance is important. Avoid redundant work, N+1 queries, and unnecessary round-trips. Use strong-types when easily available.

Avoid running tools outside of the Go tool. Tools should be versioned with go.mod. Prefer `go generate` over `go tool <tool>` over anything installed in the path.

## Architecture

**Entry point**: `cmd/cog/main.go` — parses flags, builds service graph, starts HTTP server.

**Service layer** (`internal/services/services.go`): Central `Providers` struct wires together all dependencies (Users, Sessions, AppPasswords, Plugins, Shares, Files, DB). Constructed via `services.New(cfg)`.

**HTTP routing** (`internal/web/routes.go`): Uses Go 1.22+ pattern-matched routing (`GET /path`). Auth middleware wraps the entire mux; handlers extract the user via `auth.UserFromContext(ctx)`. Handler structs embed a `handler` base type holding `*services.Providers`.

**Auth chain** (`internal/auth/`): Checks session cookie → Basic Auth → Bearer token (app passwords). `RequireAuth()` wraps handlers to enforce authentication.

**Files plugin** (`internal/plugin/files/`): Core privileged plugin (in-process, not gRPC). Each user gets a separate SQLite database for file metadata. Handles upload, download, list, delete, rename, search.

**Plugin system**: Split into public and internal packages:

- `plugin/` — Public API for plugin authors (`Plugin`, `Registrar` interfaces, types, permission constants).
- `plugin/plugpb/` — gRPC proto definition for out-of-process plugins. Generated via `go generate` using `goprotoc`.
- `plugin/plugcmd/` — `Run(plugin.Plugin)` helper for building external plugin binaries.
- `internal/plugin/` — Host-side registry, `Protected` marker struct, type aliases, and `grpcPlugin` adapter.
- Privileged plugins embed `internal/plugin.Protected`; the registry auto-detects this in `Add()`.
- `cog.go` — Root package with `cog.Run(cfg, ...plugin.Plugin)` convenience entry point for custom builds.

**NextCloud compat** (`internal/compat/`): OCS endpoints, Login Flow v2, WebDAV at `/remote.php/dav/files/{user_id}/*`, direct download tokens.

**Sharing** (`internal/share/`): Bitmask permissions (Read=1, Write=2, Reshare=4). Subjects: USER, GROUP, PUBLIC. Resources scoped as `plugin:resourcetype`.

## Web Patterns

- **Post-Redirect-Get (PRG)**: POST handlers must never render HTML directly. On success or error, set a flash cookie via `setFlash(w, kind, msg)` or `setFlashURL(w, kind, msg, url)` and redirect (303). The subsequent GET handler reads the flash with `getFlash(w, r)` and passes it to the template. This prevents "re-submit form?" dialogs on browser refresh. Exception: HTMX partial responses (hx-swap) may return HTML from POST since the browser doesn't navigate.

## Key Patterns

- **Single-writer SQLite**: WAL mode, `MaxOpenConns=1`, foreign keys enabled
- **Per-user plugin databases**: Each plugin gets its own SQLite DB per user
- **Context-based auth**: Authenticated user propagated via request context
- **Protected pattern**: Privileged plugins embed `internal/plugin.Protected`; auto-detected by registry (no `SetProtected` call)
- **Custom builds**: `cog.Run(cfg, extraPlugin1, extraPlugin2)` starts a server with additional plugins
- **gRPC plugins**: External plugin binaries use `plugcmd.Run(p)` and are loaded via `registry.LoadExternal(path)`
- **Proto generation**: `cd plugin/plugpb && go generate` (uses `goprotoc`, no external `protoc` needed)
- **Dev tool**: `go tool dev` is defined in `go.mod` as a Go tool dependency (`internal/cmd/dev`)
- **Design docs**: `/DESIGN/` directory contains specs for requirements, database schema, file protocols, sharing, frontend stack (HTMX + templ), and NextCloud API compatibility

## Testing

Tests use the `internal/cogtest` package which spins up a full server with in-memory SQLite and temp data directory. Requests route directly through the handler (no network).

Key helpers:

- `cogtest.New(t)` — creates test environment with `Providers`, `Client`, `Handler`
- `env.CreateUser("name", "pass")` — returns a user with embedded auth helpers
- Request methods: `env.Get()`, `env.PostJSON()`, `env.Do()` etc.
- HTTP options: `cogtest.Auth(user)`, `cogtest.BasicAuth(user, pass)`, `cogtest.Header(k, v)`
- Response assertions: `.Status(code)`, `.JSON(&v)`, `.Contains("text")`, `.Equals("text")`

Custom assertion packages at `internal/testing/assert` (non-fatal) and `internal/testing/require` (fatal) — similar API to testify but lightweight.
