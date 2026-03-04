# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Cog?

Cog (Cloud On Go) is a Go-based file storage and plugin platform with NextCloud API compatibility. It uses SQLite for persistence and serves HTTP (including WebDAV) on port 8580.

## Commands

```bash
# Run dev server (builds, creates admin:admin user, enables request logging)
go tool dev
go tool dev -reset   # wipe tmp/dev state first

# Build production binary
go build -o cog ./cmd/cog

# Run production binary
./cog --addr :8580 --db system.db --data-dir data --base-url http://host:8580

# Create a user
./cog --create-user username:password

# Run all tests
go test ./...

# Run a single test
go test -run TestNCLoginFlow ./internal/server

# Run tests in a specific package
go test ./internal/server
```

## Architecture

**Entry point**: `cmd/cog/main.go` — parses flags, builds service graph, starts HTTP server.

**Service layer** (`internal/services/services.go`): Central `Providers` struct wires together all dependencies (Users, Sessions, AppPasswords, Plugins, Shares, Files, DB). Constructed via `services.New(cfg)`.

**HTTP routing** (`internal/server/server.go`): Uses Go 1.22+ pattern-matched routing (`GET /path`). Auth middleware wraps the entire mux; handlers extract the user via `auth.UserFromContext(ctx)`.

**Auth chain** (`internal/auth/`): Checks session cookie → Basic Auth → Bearer token (app passwords). `RequireAuth()` wraps handlers to enforce authentication.

**Files plugin** (`internal/plugin/files/`): Core privileged plugin (in-process, not gRPC). Each user gets a separate SQLite database for file metadata. Handles upload, download, list, delete, rename, search.

**Plugin system** (`internal/plugin/`): Plugins implement `Plugin` interface (ID, Register, Start, Stop). Registration provides hooks for APIs, pages, actions, resources. Plugins get per-user databases via the registry.

**NextCloud compat** (`internal/compat/`): OCS endpoints, Login Flow v2, WebDAV at `/remote.php/dav/files/{user_id}/*`, direct download tokens.

**Sharing** (`internal/share/`): Bitmask permissions (Read=1, Write=2, Reshare=4). Subjects: USER, GROUP, PUBLIC. Resources scoped as `plugin:resourcetype`.

## Key Patterns

- **Single-writer SQLite**: WAL mode, `MaxOpenConns=1`, foreign keys enabled
- **Per-user plugin databases**: Each plugin gets its own SQLite DB per user
- **Context-based auth**: Authenticated user propagated via request context
- **Dev tool**: `go tool dev` is defined in `go.mod` as a Go 1.24 tool dependency (`internal/cmd/dev`)
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
