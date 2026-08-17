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

# Run all verifications (generate, fmt, templ fmt, fix, vet, tests with coverage)
go tool verify
go tool verify -cover-only   # skip generate/fmt/vet, just tests + coverage
go tool verify -continue     # don't stop on first failure
```

Performance is important. Avoid redundant work, N+1 queries, and unnecessary round-trips. Use strong-types when easily available.

Avoid running tools outside of the Go tool. Tools should be versioned with go.mod. Prefer `go generate` over `go tool <tool>` over anything installed in the path.

WE DO NOT SWALLOW ERRORS. Errors should be handled by either wrapping and returning or logging the error.

\*.tmpl files use tabs. The read tool will show them as spaces, but they're all tabs.

## Architecture

**Entry point**: `cmd/cog/main.go` — parses flags, builds service graph, starts HTTP server.

**Service layer** (`internal/services/services.go`): Central `Providers` struct wires together all dependencies (Users, Sessions, AppPasswords, Plugins, Shares, Files, DB). Constructed via `services.New(cfg)`.

**HTTP routing** (`internal/web/routes.go`): Uses Go 1.22+ pattern-matched routing (`GET /path`). Auth middleware wraps the entire mux; handlers extract the user via `auth.UserFromContext(ctx)`. Handler structs embed a `handler` base type holding `*services.Providers`.

**Auth chain** (`internal/auth/`): Checks session cookie → Basic Auth → Bearer token (app passwords). `RequireAuth()` wraps handlers to enforce authentication.

**Files plugin** (`plugin/files/`): Core privileged plugin (in-process, not gRPC). Each user gets a separate SQLite database for file metadata. Handles upload, download, list, delete, rename, search.

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

## Plugin databases

The host owns every database: where it lives, how it is opened, and when migrations run. Plugins declare a schema and ask for a handle.

```go
func (p *Plugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterSchema(sqlFS)   // sql/schema.sql + sql/migration*.sql
	...
}

db, err := ctx.PluginDB(username)                                  // migrated, cached, shared
db, err := ctx.PluginDB(username, plugin.OfPlugin("files"), plugin.ReadOnly, plugin.NoHooks)
```

- **Never build a path or a DSN in a plugin.** `internal/db.PluginDBPath` is the only place that knows the layout, and `internal/db.OpenPluginDB` the only place that knows the connection settings. Both the registry and `plugcmd`'s trusted registrar go through them, so an out-of-process plugin follows the same rules. Hard-coding either is a bet that the database is a local SQLite file, which will not hold for a hosted multi-tenant deployment.
- **Options describe intent, not SQLite syntax** (`ReadOnly`, `NoHooks`, `OfPlugin`). `mode=ro` does not survive a change of backend; "read only" does.
- **An options-bearing open is not cached** and belongs to the caller to close. Default opens share one cached handle per (user, plugin).
- **Migrations run on open, before the handle escapes.** Do not call `plugin.Migrate` from a plugin — that duplication is what let a handler reach an unmigrated database when nothing else had opened it first.
- Each plugin keeps a one-line `DB(username)` delegating to `ctx.PluginDB`, because `ctx` is unexported and tests need an accessor.

### SQLite specifics

- Every DSN sets `_busy_timeout`. Without it a writer that finds the database locked fails its query instead of waiting, and a page load plus its HTMX follow-ups arrive on separate connections.
- `internal/db.TunePool` sets the idle-connection timeout so per-user handles do not hold file descriptors forever. **It deliberately skips in-memory databases**: a shared-cache in-memory database is destroyed when its last connection closes, so expiring the last connection deletes the data rather than freeing a resource.
- **Nothing runs on a shared-cache in-memory database, including tests.** In shared-cache mode two connections contend for table-level locks, and SQLite reports that as `SQLITE_LOCKED` ("database table is locked") *without* invoking the busy handler — so `_busy_timeout` does not apply and a concurrent writer fails outright. Change-capture triggers make every write touch `_cog_row_changes`, which the search drainer writes to as well, so that contention is guaranteed rather than rare. The harness therefore puts the system database on a file in its temp data directory, as the server does.
- Never `SetMaxOpenConns(1)`. It deadlocks any code that reads while a write transaction is open on another connection.
- Search indexing is fed by a queue inside each database (`internal/db/changelog.go`), filled by triggers on the tables a plugin's search sources declare and drained by `internal/search/drain.go`. A change is recorded in the transaction that made it, so it survives a crash and is visible to whichever process drains it — which is what lets more than one replica index, and what a backend with no connection hooks would rely on. The update hooks that remain only tell the drainer to run sooner.

## Key Patterns

- **Per-user plugin databases**: Each plugin gets its own SQLite DB per user
- **Context-based auth**: Authenticated user propagated via request context
- **Protected pattern**: Privileged plugins embed `internal/plugin.Protected`; auto-detected by registry (no `SetProtected` call)
- **Custom builds**: `cog.Run(cfg, extraPlugin1, extraPlugin2)` starts a server with additional plugins
- **gRPC plugins**: External plugin binaries use `plugcmd.Run(p)` and are loaded via `registry.LoadExternal(path)`
- **Proto generation**: `cd plugin/plugpb && go generate` (uses `goprotoc`, no external `protoc` needed)
- **Dev tool**: `go tool dev` is defined in `go.mod` as a Go tool dependency (`internal/cmd/dev`)
- **Design docs**: `/DESIGN/` directory contains specs for requirements, database schema, file protocols, sharing, frontend stack (HTMX + templ), and NextCloud API compatibility

## Testing

Tests use the `internal/testing/cogtest` package which spins up a full server with a SQLite database in a temp data directory. Requests route directly through the handler (no network).

Key helpers:

- `cogtest.New(t)` — creates test environment with `Providers`, `Client`, `Handler`
- `env.CreateUser(t, "name", "pass")` — returns a user with embedded auth helpers
- Request methods: `env.Get()`, `env.PostJSON()`, `env.Do()` etc.
- HTTP options: `cogtest.Auth(user)`, `cogtest.BasicAuth(user, pass)`, `cogtest.Header(k, v)`
- Response assertions: `.Status(code)`, `.JSON(&v)`, `.Contains("text")`, `.Equals("text")`

Custom assertion packages at `internal/testing/assert` (non-fatal) and `internal/testing/require` (fatal) — similar API to testify but lightweight. Don't ignore errors; use `require.NoError(t, err)`.

### Browser snapshots

A snapshot is a pair in `testdata/snapshots`: `<TestName>.html` holds the normalised DOM and is what the test asserts on, and the `.png` beside it renders that DOM for a human to look at in review. Pixels are never compared — they depend on whichever Chromium `launcher.LookPath()` finds, so asserting on them makes a browser update indistinguishable from an application change.

A screenshot is only captured when the DOM golden does not match, which keeps the image in step with the markup without ever comparing it. CSS is covered by the same mechanism: stylesheets are served with a content hash in their URL (`internal/assets`), so editing one changes the DOM.

```bash
WEBTEST_UPDATE=1 go test ./plugin/calendar/    # accept DOM changes, refresh images
```

A failing snapshot writes `<TestName>.actual.html` and `.actual.png` next to the golden. Diff the HTML to see what changed; the image is there to look at.

CSRF tokens and user identifiers are masked in the golden — they differ per run. Nothing else does, so a golden that changes between runs means the page is genuinely nondeterministic, which is worth fixing rather than masking.

`<script>` elements are dropped. The DOM is captured once the page has settled, so what a script did to the page is already in the tree; keeping the element would only churn every golden when a library's content hash changed. Stylesheets stay, because their hash is what makes a CSS edit reach the golden.

When a browser test fails, read the saved page source before assuming a timing problem — `webtest` writes the DOM and a screenshot to the cache dir on failure, and a server-side error (`no such table`, `access denied`) looks exactly like a flaky wait from the test output alone.
