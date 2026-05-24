# Plugin System Audit

Findings on the in-process registry, the gRPC plugin bridge, lifecycle, and design-doc drift.

## 1. Privilege Boundary

### 1.1 `Protected` marker reachable from every first-party plugin
`internal/plugin/plugin.go:97-105` — `protectedPlugin` is unexported. External Go plugins can't
spoof it. But `plugin/profanity/profanity.go:17,29` imports `internal/plugin` from outside the
`internal/` tree (allowed because it's in the same module). Any plugin under `plugin/` can stamp
itself protected. Design doc (`DESIGN/plugins/index.md:38`) says only `dashboard` and `files`
should be Protected; `profanity` opted in undocumented.

### 1.2 `LoadExternalTrusted` hands children full secrets
`internal/plugin/grpc.go:266-270` — writes `COG_TRUSTED=1`, `COG_DATA_DIR=…`, `COG_CSRF_SECRET=…`
into env. Discovered `cog-plugin-*` binaries on PATH (see 1.3) get the host CSRF secret. No
signature, no hash, no allowlist.

### 1.3 `DiscoverProcessPlugins` loads *trusted* by default
`internal/plugin/registry.go:1021` — discovered plugins go through `LoadExternalTrusted`. The
"untrusted external" tier from the design doc has no code path.

### 1.4 No permission system implemented
Despite `DESIGN/plugins/index.md:42-56,170-180` (manifest, `DB_SANDBOX`, `DB_READ:*`, `FS_READ:*`,
`ROUTE_GLOBAL_BIND`), the code contains no capability tokens. `RegisterRoute` is gated purely by
"is the registrar typed `PrivilegedContext`?" (`registry.go:769-771`) — i.e. "are you in the same
Go module?".

## 2. Concurrency on Registry State

### 2.1 Enabled-state map has no synchronization
`registry.go:903-928` — `IsEnabled`/`SetEnabled`/`loadEnabledState` touch `r.enabled` with no
lock. `RequirePluginEnabled` middleware (`registry.go:1061-1071`) reads on every request while
admin POSTs to `SetEnabled`. Concurrent map writes panic in Go's runtime.

### 2.2 `discovered`, `plugins`, `order` also unsynchronized
`registry.go:127-150,1021-1026`. `r.mu` covers only the DB cache (`registry.go:71-73`), not the
plugin or enable maps.

### 2.3 Disabling a plugin doesn't `Stop()` it
RSS poller (`plugin/rss/rss.go:101-115`) and calendar reminder loop (`plugin/calendar/calendar.go:88-101`)
keep running while routes 404. Re-enabling has no re-Start hook.

## 3. Lifecycle

### 3.1 No panic recovery anywhere
`registry.go:154-161` `Start()` and `:370-376` `FireHook` and `:786-794` resource handler all
invoke plugin code without `defer recover()`. A plugin panic kills the host.

### 3.2 `Stop()` ignores errors
`registry.go:164-176` logs and discards. No Stop timeout / context. If a plugin's `Stop()` blocks
(e.g. calendar's `<-p.done`), DBs are never closed.

### 3.3 No supervisor for external plugin processes
`internal/plugin/grpc.go:189-247` — once `LoadExternal` succeeds, fire-and-forget. No `cmd.Wait()`
goroutine, no restart, no log capture (stdout/stderr piped into host's own stdout/stderr, mixing
into structured log), no liveness check. Child death → 502 forever from `proxyHandler`.

### 3.4 `grpcPlugin.Stop()` calls `Kill()` after RPC with no deadline
`internal/plugin/grpc.go:89-97`. No SIGTERM-then-SIGKILL escalation; if Stop RPC blocks, Kill
never runs.

### 3.5 `waitForSocket` polls 100ms × 50
`internal/plugin/grpc.go:318-328`. 5-second hard cap, no exponential backoff, no log of child's
stderr on timeout.

### 3.6 Plugin DB cache holds connections forever
`registry.go:181-201,403-424`. N users × M plugins of SQLite handles, no eviction, no LRU.

## 4. gRPC Bridge

### 4.1 Hooks over gRPC are silent no-ops
`internal/plugin/grpc.go:63-67` — `RegisterHook` from a gRPC plugin registers a callback that
only debug-logs. `FireHook` still iterates it. `DESIGN/plugins/grpc_bridge.md:68` admits this;
plugins should fail registration, not silently no-op.

### 4.2 No user/auth context proxied to gRPC handlers
`plugin.proto:94-96` has `user_id`/`username` fields. `internal/plugin/grpc.go:112-119`
`proxyHandler` never populates them. Plugin-side `plugin.UserFromContext` returns nil.

### 4.3 Whole-body buffering, no streaming
`internal/plugin/grpc.go:102-119,138-156` + `plugcmd/plugcmd.go:97-129,294-303`. Entire bodies
through `io.ReadAll`. Multi-GB upload OOMs the host. Default gRPC max message size (4 MiB)
silently truncates.

### 4.4 `w.Write` errors ignored
`internal/plugin/grpc.go:131`, `plugcmd.go:303`. CLAUDE.md violation.

### 4.5 Registration stream lacks EOF semantics on host
`internal/plugin/grpc.go:34-82`. Plugin closing the stream without explicit close → host blocks.

### 4.6 `responseCapture` `Write` doesn't auto-set 200
`plugcmd/plugcmd.go:294-303`. Papered over by `statusCode: 200` default at line 116. Middleware
relying on `WriteHeader` ordering breaks.

### 4.7 No Flush/Hijack/streaming response
WebSocket / SSE handlers in gRPC plugins impossible.

### 4.8 `RenderWidget` uses `context.Background`
`plugcmd/plugcmd.go:140`. Loses host's cancellation, deadline, trace.

### 4.9 `proto` missing event types
`plugin.proto:31-41` `RegistrationEvent` lacks `RegisterResource` (search source), `RegisterDAVHandler`,
`RegisterSettingsPage`, `RegisterCommand`. `capturingRegistrar` stubs these as no-ops
(`plugcmd.go:255-292`) — silent contract failure.

### 4.10 Unix socket perms not explicitly 0600
`internal/plugin/grpc.go:191-195`. Dir is 0700 from `MkdirTemp`, fine on Linux, but no explicit
`chmod` on the socket file.

### 4.11 `plugcmd.Run` `log.Fatal` on env-not-set
`plugcmd.go:34,39,46`. Exit code 1 with no graceful gRPC shutdown.

### 4.12 Trusted env is hand-rolled, no nonce
`plugcmd/trusted.go:29-47`. Trust upgrade purely on `COG_TRUSTED=1` + `COG_DATA_DIR` non-empty.
No HMAC tying env vars back to the host process.

### 4.13 Trusted registrar opens DBs without host's update hooks
`plugcmd/trusted.go:62-65`. Bare `sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")`.
The host's search-index update hook (`registry.go:255-268`) is NOT installed → trusted plugin
writes don't trigger indexing. Also `MaxOpenConns=1` not set → host+plugin concurrent writes hit
"database is locked" instead of queueing.

### 4.14 Trusted registrar service stubs return nil
`plugcmd/trusted.go:130-135`. ShareStore, EmailSender, UserLookup, GroupStore, FilesAccess,
VolumeAccess all nil. Trusted gRPC plugin has DB/FS access but can't use share/email/user APIs.

## 5. Routing / Resources

### 5.1 `RegisterRoute` has no auth/CSRF wrapper
`registry.go:586-590,763-771`. `RegisterPage` wraps `RequireAuthWeb`+shell. `RegisterAction` wraps
`RequireAuthWeb`+CSRF. `RegisterRoute` wraps only `RequirePluginEnabled`. Privileged-plugin
authors must remember to wrap themselves. `plugin/files/files.go:122-134` hand-wraps every route.

### 5.2 `RegisterAPI` no CSRF (intended) but no Bearer-only guard
`registry.go:758-761`. Session-cookie users mutate via methods on `/apps/` routes too. Design
says "APIs use Bearer or App Passwords"; auth chain is identical.

### 5.3 `RegisterDAVHandler` middleware order leaks plugin existence
`registry.go:825-829`. `auth.RequireAuth(pr.wrap(handler))` — auth runs first, so a disabled
plugin's DAV endpoint returns 401 (not 404) to anonymous callers.

### 5.4 `RegisterResource` mounts without method
`registry.go:781-794`. Pattern `/apps/{plugin_id}/{type}/` matches all methods → unintended POST
hits `ServeResource` without CSRF.

### 5.5 `shellMiddleware` buffers via `httptest.NewRecorder`
`registry.go:623-693`. Kills streaming, double-allocates large pages, strips Content-Length
(`:684-689`).

### 5.6 `shellMiddleware` HX-Request bypass on untrusted header
`registry.go:626-628`. Any caller sending `HX-Request: true` skips shell.

### 5.7 Dead `r.PathValue("id")` extraction
`registry.go:786-793`. Pattern has no `{id}`; always falls through to `filepath.Base`.

## 6. API Surface / Stability

### 6.1 `plugin.UserContext` declared, never referenced
`plugin/plugin.go:136-141`. Dead type suggesting old design.

### 6.2 `plugin.UserDB`/`Username` panic on nil context
`plugin/helpers.go:20-24,32-34`. No nil-check on the pluginDB func / `*UserInfo`.

### 6.3 `PrivilegedContext` only reachable by in-module plugins
`internal/plugin/plugin.go:115-127`. `cog.Run` accepts any `plugin.Plugin` but third-party plugins
cannot type-assert to `PrivilegedContext`. Silently restricts external authors to non-privileged
paths.

### 6.4 `FolderIconProvider`/`ContentFilter`/`ActivityLogger`/`Doctor` only work for in-process plugins
gRPC plugins cannot expose these — registry stores them as `Plugin` and type-asserts on Go type;
`grpcPlugin` doesn't implement them.

### 6.5 Inconsistent provider caching
`plugin/files/files.go:148-154` and `plugin/rss/rss.go:103-109` cache `priv.Plugins()` at startup.
Disable/enable later → cache stale.

### 6.6 `Migrate` runs on every `DB(username)` call
`plugin/files/files.go:175-184` and equivalents in every plugin. Idempotent but reads `schema.sql`,
globs migrations, runs PRAGMAs per call. Should be once per (user, plugin).

## 7. Inter-Plugin

### 7.1 No mediation
`internal/plugin/plugin.go:122-126`. `PluginByID`/`Plugins()` return raw `Plugin`. Privileged
callers cast to concrete types (`plugin/files/files.go:148-153`). Once privileged, all internals
accessible — no "plugin X cannot call plugin Y".

### 7.2 Hooks have no registry / panic recovery
`registry.go:370-376`. Arbitrary string names, `data any`, no schema. A subscriber panic with no
recover (3.1) kills the firing handler.

### 7.3 Hook subscribers iterated in map-random order
`registry.go:371`. Side-effecting hooks non-deterministic across runs.

## 8. Migration System

### 8.1 No version table
`internal/migrate/migrate.go:23-61`. Re-runs `schema.sql` (idempotent only via `IF NOT EXISTS`).
Migrations rely on `-- IF COLUMN table.column NOT EXIST` guards. No table rename, FK change, or
arbitrary one-shot SQL support.

### 8.2 No transaction per migration file
`migrate.go:56`. Full body executed at once. Half-applied migration leaves schema corrupt.

### 8.3 No cross-plugin schema ordering
Each plugin migrates independently. No coordination if future capabilities introduce shared
schema.

### 8.4 Concurrent `Migrate` calls race
`plugin/files/files.go:175-184`. Two concurrent `DB(username)` calls invoke `Migrate` on the same
`*sql.DB`. `MaxOpenConns=1` not set (see Bugs report) → both can run.

### 8.5 `parseColumnGuard` only handles "column exists"
`migrate.go:64-78`. No `table.index`, no compound guards.

## 9. Inconsistencies Across Plugins

### 9.1 `Start`/`Stop` patterns hand-rolled
Files / RSS / Calendar each implement cancel+done plumbing. No shared helper. Easy to introduce a
race by forgetting to `<-done`.

### 9.2 `DB(username)` helper duplicated everywhere
Every plugin (files, rss, bookmarks, calendar, contacts, tasks, ...) has near-identical
`DB(username) (*sql.DB, error)` calling `ctx.PluginDB` + `plugin.Migrate`. Centralize.

### 9.3 `ctx.Now()` vs `time.Now()` mixed
`plugin/files/*` uses `ctx.Now()`. `plugcmd/trusted.go` uses `time.Now()` directly — virtual
clock in tests doesn't affect gRPC plugins.

### 9.4 `profanity` is Protected, design says no
`plugin/profanity/profanity.go:29` vs `DESIGN/plugins/index.md:18`.

### 9.5 Activity logging inconsistent
Files always calls `ActivityLog()`. Bookmarks, calendar, notes, tasks don't. No contract.

### 9.6 `RegisterResource` shape inconsistent
`plugin/files/files.go:77-88` sets neither `Handler` nor `PermissionLevels`. Other plugins do.
Unclear if intentional.

## 10. Design Doc Drift

| Design (`DESIGN/plugins/index.md`) | Implementation |
| --- | --- |
| Four trust tiers (Protected/Privileged/Trusted/External) | Binary Protected-or-not + binary `LoadExternalTrusted`/`LoadExternal`. |
| Manifest-based permissions | None. |
| ATTACH DATABASE managed by host | Not implemented. |
| Custom SQLite funcs `read_file`, `head_file`, `file_exists`, `file_size` | Not implemented. |
| Template-based pages with embedded SQL | Not implemented. |
| CSRF token namespaced `HMAC(secret, sessionID+pluginID)` | `pr.injectPluginContext` (`registry.go:594-617`) uses `auth.CSRFToken(sessID, pr.csrfSecret)` — not namespaced. Same token works across plugins. |
| External UI in iframe sandbox | No iframe sandboxing. |
| Doc `Registrar` lacks `RegisterDAVHandler`, `RegisterSearchSource`, `RegisterNavItem`, `RegisterWidget`, `RegisterCommand` | All exist in code. Docs stale. |
| `SettingsPage` doc has Name+Handler only | Code has Slug, Name, Handler, Action (`plugin/plugin.go:79-84`). |
| `Per-user DB not yet proxied; externals must use API` (`grpc_bridge.md:70`) | Trusted gRPC opens files directly (`plugcmd/trusted.go:71-83`). Doc out of date. |
| Lifecycle handshake for capability/version negotiation | No handshake. `GetID` returns ID only. |

## 11. Misc

- `registry.go:915-928` `setEnabled` persists only, no lifecycle call.
- `plugcmd/trusted.go:81` `RootDB` path matches host by string convention, no shared helper.
- `registry.go:945-952` `IsDiscovered` has unreachable second statement.
- `registry.go:260-263` `connectHooks` uses `Conn.RegisterUpdateHook` which allows one hook per
  connection — plugin self-setup silently replaces it.
- `plugin.proto` has no version field on any message. Proto3 add-field is BC, but rename/remove
  has no clear failure mode.
- Privileged plugins (`plugin/files/files.go:124-126,132-133`) hand-wrap `auth.RequireCSRF` — forget
  and your state-changing endpoint has no CSRF.
- `RegisterPage` always wraps `shellMiddleware`, including for redirect responses. Recorder strips
  Content-Type for non-redirects (`registry.go:684-689`); 200 page loses CT.

## Summary

A working in-process registry with a thin Protected-marker for privilege. The advertised
permission / manifest / four-tier / iframe-sandbox / ATTACH-DB design is largely unbuilt. The
gRPC bridge buffers full payloads, lacks streaming, lacks user-context propagation, lacks panic
recovery, and silently no-ops several `Registrar` methods. Concurrency on registry state is
unsafe. Discovered `cog-plugin-*` binaries auto-load as trusted with the host CSRF secret.

Highest-impact follow-ups: synchronize the enable / plugins maps, add panic recovery around all
plugin invocations, gate `LoadExternalTrusted` behind explicit operator opt-in, build the
permission manifest the design promises.
