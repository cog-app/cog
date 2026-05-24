# Test Coverage Audit

Coverage measured via `go test -cover`.

## Per-package coverage

| Package | Coverage | Notes |
|---|---|---|
| `internal/quota` | 85.5% | best |
| `internal/group` | 68.1% | |
| `internal/share` | 65.7% | core logic well-tested |
| `internal/web` | 55.0% | many handlers, huge surface |
| `internal/search` | 43.5% | |
| `internal/mount` | 41.8% | |
| `vfs` | 40.1% | |
| `internal/user` | 36.4% | |
| `internal/federation` | 36.3% | |
| `plugin/files` | 35.6% | API surface untested |
| `internal/auth` | 21.3% | MFA/OTP/WebAuthn fully untested |

**Zero test files**: `internal/compat`, `internal/plugin`, `internal/services`, `internal/migrate`,
`internal/db`, `internal/settings`, `internal/email`, `internal/seq`, `internal/maildev`,
`internal/config`, `plugin/plugcmd`, `plugin/plugpb`, `cmd/cog-plugin-recipes`, `internal/cmd/dev`,
`internal/cmd/migratesql`, `internal/cmd/verify`, `internal/ssh/keys`. Several exercised
transitively (`compat` via `web`, `plugin` via `web`), several aren't.

**Pre-existing failures**: `plugin/gallery/TestBrowser/SettingsPage` and
`plugin/rss/TestBrowser/SettingsPage` fail on golden snapshot dimension mismatches — coverage for
those packages can't be trusted until fixed.

## Priority 1 — High-risk surface at 0%

### `plugin/files/api.go` — all handlers at 0%
Every handler at 0%: `handleList`, `handleUpload`, `handleDownload`, `handleMkdir`, `handleDelete`,
`handleRename`, `handleSearch`, `handleInfo`, `handleFavorite`, `handleFavorites`,
`handleListComments`, `handleCreateComment`, `handleDeleteComment`, `handleShareCreate`,
`handleShareList`, `handleSharesWithMe`, `handleShareDelete`, `handlePublicLinkCreate`,
`handlePublicLinkAccess`, `handlePublicLinkDownload`, `handleSharedDownload`, `verifyOwnership`,
`checkSharePassword`. Public REST API for the core privileged plugin — including auth, share
password check, ownership verification — never exercised.

### `plugin/files/comments.go` — entirely uncovered
`ListComments`, `CreateComment`, `UpdateComment`, `DeleteComment`, `CommentCount`,
`CommentCounts` all 0%. WebDAV-side `TestCommentsCRUD` covers compat layer, not plugin-store.

### `internal/migrate/migrate.go` — no tests
`Migrate`, `parseColumnGuard`, `ColumnExists` all uncovered. Migrations run as side-effect of
`cogtest.New` but no test covers out-of-order, idempotency, malformed guards, missing columns, or
mid-migration failure. Regression in `parseColumnGuard` silently bypasses guards.

### `plugin/plugcmd/*.go` and `internal/plugin/grpc.go` — gRPC bridge at 0%
Every public `plugcmd` method (`Run`, `Register`, `Start`, `Stop`, `HandleHTTPRequest`,
`RenderWidget`, all `Register*`, `PluginDB`, `UserFilesDir`) at 0%. Host-side `LoadExternal`,
`LoadExternalTrusted`, `proxyHandler`, `resourceProxyHandler`, `widgetRenderer`, `waitForSocket`
also 0%. Highest-risk cross-process security surface, no integration test.

### `internal/plugin/registry.go` — core gaps
Zero-coverage: `Stop` (164), `SetDBHookFactory` (273), `FireHook` (370), `RootDB` (403, 821),
`SetEnabled` (915), `IsDiscovered` (945), `RegisterHook` (797), `RegisterCommand` (813),
`AdminCommands` (852), `DoctorChecks` (874), `pluginRegistrar.EmailSender`, `FilesAccess`,
`VolumeAccess`, `QuotaCheck`. No negative test verifying unprivileged plugin denial.

## Priority 2 — Significant gaps in tested packages

### `internal/auth` — 21.3%, MFA fully untested
Zero-coverage files:
- `emailotp.go` — `Create`, `Validate`, `generateOTPCode`
- `mfapending.go` — `Create`, `Validate`, `Delete`
- `totp.go` — `BeginSetup`, `ConfirmSetup`, `Validate`, `Disable`, `RegenerateBackupCodes`,
  `BackupCodeCount`, `validateBackupCode`, `regenerateBackupCodes`, `generateBackupCode`
- `webauthn.go` — every function: `BeginRegistration`, `FinishRegistration`, `BeginLogin`,
  `FinishLogin`, `ListCredentials`, `DeleteCredential`
- `encrypt.go` — `encryptSecret`, `decryptSecret`
- `passwordreset.go` — `Create`, `Validate`, `MarkUsed`
- `middleware.go:69 authenticate` — 0% in this package's tests (exercised transitively via
  `internal/web`). The single most security-critical function in the codebase has no direct unit
  tests for bypass attempts, expired session + valid bearer interaction, malformed Basic Auth
  headers, header injection.
- `session.go:82 DeleteAllExcept`, `DeleteAllForUser` — logout-everywhere flow, 0%.

### `internal/web/login.go` MFA paths uncovered
`ShowMFA` (147), `HandleMFA` (176), `BeginWebAuthnLogin` (465). `share_handler.go:284 CreateFederatedShareForm`.

### `internal/federation` — token & proxy webdav untested
`webdav_token.go` — `ServeHTTP`, `servePublicShare`, `extractToken`, `handlePropfind`,
`writePropResponse`, `handlePut`, `handleMkcol`, `handleDelete` all 0%. Public token-authenticated
WebDAV endpoint — no test verifies wrong/expired token rejection, scope, PUT/DELETE block on
read-only links.
`webdav_proxy.go` — `NewWebDAVProxy`, `Proxy` both 0%.
`store.go` — `GetByToken`, `GetByRemoteID`, `Reject`, `Remove`, `FedShareIDFromNC`,
`ListAcceptedMounts`, `generateToken` 0%.
`ocs_handler.go` — `HandleDeclineShare`, `HandleRemoveShare` 0%.

### `vfs/overlay.go` — mutation untested
`Rename` (262), `Chmod` (423), `Chtimes` (446), `Truncate` (469), `Link` (492), `LstatInfo`
(345), `LocalRoot` (545), `Unwrap` (520) all 0%. Existing test covers basic stat/list/read/mount/
delete-virtual; cross-boundary mutations not exercised. `internal/mount/mountfs.go` is the
highest-count uncovered file (31 zero-coverage functions across all tests).

### WebDAV edge cases (`internal/web/webdav_test.go`)
Happy-path only. Missing:
- **PROPFIND Depth=infinity** — handler at `compat/webdav.go:120` defaults to 1. No test for
  `infinity` or invalid strings.
- **MOVE/COPY with Overwrite=F to existing destination** — handler returns 412 at
  `compat/webdav.go:318`. No test asserts this.
- **MOVE/COPY destination outside user namespace** — no cross-user copy / path-escape tests.
- **Large file PUT** — no multi-MB upload test.
- **PUT with `If-Match` / `If-None-Match`** — not tested.
- **PUT to a directory path** — not tested.
- **Chunked upload edge cases**: out-of-order chunks, duplicate chunk numbers, MOVE-to-assemble
  with destination already existing, MOVE-to-assemble across users.
- **Range requests** — no `Range: bytes=` test anywhere. Media plugin supports range streaming,
  zero tests for byte-range protocol.

### Share permission boundary tests are weak
`internal/share/share_test.go`:
- No `PermReshare` test — no verification that read-only holder cannot create downstream share,
  or that OCS bitmask round-trips reshare.
- `TestCheckAccessPermissionMerge` (155) doesn't test `Permissions = 0` denial or downgrade on
  partial delete.
- `share.go:217 ListByOwner` uncovered.
- No write-rejected-on-read-only-share at HTTP layer (because all file-API handlers are 0%).
OCS sharing tests cover create/update/delete/forbid but only at perms 1 and 15. No invalid
bitmask, share-to-self, or share to deleted user.

### Concurrency / single-writer SQLite — zero coverage
`grep -rE "t\.Parallel|sync\.WaitGroup|go func" internal/{web,auth,share}/*_test.go` → nothing.
With `MaxOpenConns=1` (per design — actually missing per `bugs-quality.md`) lock-contention /
deadlock under concurrent writes (simultaneous PUT same path, simultaneous share-create) has no
regression coverage. Exactly the class of bug the architecture is most likely to hit.

### HTTP method edge cases
No grep hits for `MethodNotAllowed`, `405`, or "wrong method" in tests. Go 1.22 routing returns
405 automatically when method-restricted patterns match — no test verifies, e.g., DELETE on
admin/users/{id} rejects GET, or WebDAV PUT handler rejects POST.

### `internal/services/services.go` — 0%
Wiring code. `cogtest.New` exercises construction, but no test asserts graceful shutdown order,
errors when SQLite cannot open, or `DataDir` permission errors.

## Priority 3 — Smaller gaps

- `internal/compat/webdav.go:358 copyDir` — recursive copy, 0%. COPY tests are single-file only.
- `internal/compat/ocs.go` lines 33, 38, 94, 109, 155, 200, 210 — uncovered branches (XML-vs-JSON
  negotiation, OCS error responses).
- `internal/web/adapters.go` — `ListBySubject`, `CheckAccess`, `Create`, `Delete`, `GetByID`,
  `ListAccessible`, `Send`, `SendWithAttachments` all 0%. Trivial smoke tests possible.
- `internal/web/admin_volumes.go:336 resolveSubjectName` — 0%.
- `plugin/files/files.go` — `Start`, `Stop` lifecycle, `GetByID`, `HardDelete`
  (permanent delete after trash, no test), `ParentID`, `SetFavorite`, `SetFavoriteByID` all 0%.
- `internal/plugin/plugin.go:105 protected` — function deciding plugin privilege, 0%.
- `internal/maildev` entire package 0% — dev-only, low priority.
- `cmd/cog/main.go` 4.4% — CLI flag parsing has no direct tests.

**Smoke-only tests**: `internal/web/server_test.go:TestNCLoginFlow` is end-to-end happy-path; no
assertion that poll returns the right user, that grant from a different IP behaves correctly, or
that polling after expiry returns 404. `internal/web/share_handler_test.go:TestSharePanel`
asserts 200 with no body verification.

## Recommended order

1. HTTP tests for `plugin/files/api.go` — 22 handlers at 0% is the biggest single gap.
2. Focused `internal/migrate` test using tmpfs migrations FS.
3. WebDAV edge cases: PROPFIND Depth=infinity/0, MOVE Overwrite=F, COPY of directory,
   chunked-upload out-of-order.
4. `internal/federation/webdav_token.go` token validation (expired / wrong / cross-resource).
5. MFA happy-path + bypass for TOTP and WebAuthn — entire features at 0%.
6. Concurrency tests using `t.Parallel`/goroutines for simultaneous PUT same path, share-create,
   session-create race.
7. gRPC plugin smoke test spinning up `plugcmd.Run` against `LoadExternal`.
