# Fix-Now Remediation Plan

Plan addresses ten Fix-now findings from `top-findings.md`. Each section: scope, design, files
touched, tests, rollout. Ordered to minimize churn (#4 first — `MaxOpenConns=1` change cascades
into other tests; #6 cookie change is global; rest are local).

Branch strategy: one feature branch per fix (`fix/audit-NN-slug`) merging to `master` independently.
Findings #1, #2, #3, #5, #6, #7 should ship together as a coordinated security release; #8,
#9, #10 can ship individually. **#4 retracted.**

---

## #4 — RETRACTED

Original finding was wrong. WAL mode + `_busy_timeout=5000` (already set in
`internal/db/hook.go:46`) is the correct design. `SetMaxOpenConns(1)` would deadlock on nested
queries issued during `rows.Next()` iteration and serialize reads unnecessarily. The CLAUDE.md
mention is stale and will be cleaned up separately.

No action required for this item.

---

## #1 — Gate `/debug/pprof/*` behind admin auth

**Scope:** five routes registered in `internal/web/routes.go:38-42` currently exposed to anyone
who can reach the host. `authMW.Wrap` wraps the mux but pprof does not require auth.

**Design:** wrap pprof handlers with a new `requireAdmin` middleware. Cleanest: register pprof on
a *separate* mux that the admin middleware fronts, then mount under `/admin/debug/pprof/`. Also
acceptable: keep path, wrap each handler with `auth.RequireAuthWeb` + admin check.

Preferred approach — move to `/admin/debug/pprof/` so log scrapers / port scanners that fingerprint
the well-known path miss it.

**Files:**
- `internal/web/routes.go:37-42` — replace the five `HandleFunc` calls with a helper
  `registerPprof(mux, p)` that wraps each handler with `auth.RequireAuth(...)` + admin gate.
- New `internal/web/pprof.go` housing the gate + registration.
- `internal/auth/middleware.go` — confirm `UserFromContext` exposes `IsAdmin`; if not, expose it.

**Tests:**
- `internal/web/pprof_test.go`: anonymous GET → 401/302; non-admin user → 403; admin → 200 with
  `Content-Type: application/octet-stream` (for `/profile`) or `text/html` (for index).

**Rollout:** breaking change for operators using `pprof` over an SSH tunnel without an admin
cookie. Document in CHANGELOG under "Security" with the new path.

---

## #7 — Stop trusting `X-Forwarded-For` / `X-Real-IP` blindly

**Scope:** `internal/ratelimit/ratelimit.go:93-110` `ClientIP`. Currently any request can spoof
its rate-limit identity.

**Design:** add config `cfg.TrustedProxies []netip.Prefix` (loaded from `config.toml`). `ClientIP`
parses `r.RemoteAddr`; if it matches a trusted prefix, walk `X-Forwarded-For` *right-to-left*
skipping trusted hops; first untrusted is the client. If `RemoteAddr` is not trusted, return it
verbatim and ignore the headers.

**Files:**
- `internal/config/config.go` — add `TrustedProxies []string`; parse to `[]netip.Prefix` on load.
- `internal/ratelimit/ratelimit.go` — rewrite `ClientIP` taking the prefix list. Signature change:
  pass prefixes in via a `Limiter` field or new param.
- `internal/ratelimit/ratelimit_test.go` — table tests covering: direct connect (header ignored),
  one trusted hop, two trusted hops, spoofed XFF from untrusted upstream, IPv6, malformed.
- All call sites of `ratelimit.ClientIP` — confirm there are none outside the limiter; if there
  are, route them through the limiter helper.

**Tests:** mentioned above. Also extend the existing login brute-force test to spoof XFF and
verify it does NOT bypass.

**Rollout:** default `TrustedProxies = []` so a fresh install accepts no XFF. Document in
`DOCS/admin/` how to add a reverse proxy (`127.0.0.1/32`, `::1/128`, etc.).

**Security note:** failure to configure `TrustedProxies` after this change means deployments
behind a real proxy will rate-limit by the proxy IP — backwards from today. Surface this in the
upgrade notes prominently.

---

## #2 — Sign / authenticate OCM federation endpoints

**Scope:** `POST /ocm/shares` (`internal/federation/ocm_handler.go:39-155`) and
`POST /ocm/notifications` accept any unauthenticated caller, allowing inbound-share spam and
state-flip of outbound shares by anyone who can guess a `providerID`.

**Design:** OCM 1.1 does not mandate request signing, so two-track:
1. **Mandatory:** make `providerID` unguessable. Today it's a sequential or short ID. Replace with
   a 32-byte URL-safe random token generated at outbound-share creation, stored in
   `federation.OCMShare.ProviderID`. Notifications then need the token, not just the integer.
2. **Mandatory:** for `POST /ocm/notifications` require a `provider_id` from the same peer host
   that originally received the share (store `RemoteHost` at share creation, compare on inbound).
3. **Mandatory:** add a per-peer HMAC. On outbound share, generate a shared secret and embed in
   the outbound payload (`additionalProperties.secret`). On notification, require the peer to
   include the same secret. Reject mismatch.
4. **Optional / future:** require all inbound `POST /ocm/shares` come from a peer host with a
   valid HTTPS cert; expose an operator allowlist
   (`cfg.Federation.PeerAllowlist []string`); reject host not on list. Default deny.

**Files:**
- `internal/federation/store.go` — add `Secret`, `RemoteHost` to `FederatedShare`. Schema
  migration `migrations/00NN_fedshare_secret.sql`.
- `internal/federation/ocm_handler.go:39-155` — verify secret on every notification. Require
  allowlist on inbound shares.
- `internal/federation/client.go:55,101` — generate secret on `SendShare`, send in payload;
  include on `Notify`.
- `internal/config/config.go` — `Federation.PeerAllowlist []string` (`*` means allow all,
  default empty = deny all inbound).
- `internal/federation/ocm_handler_test.go` — currently nonexistent. Add: anonymous notification
  rejected, wrong secret rejected, correct secret accepted, allowlist deny.

**Tests:** as above. Plus integration: two Cog instances on different ports, mutual share-create
round-trip.

**Rollout:** breaking for existing federated shares. Provide one-shot migration that backfills
random secrets and emails operators of remote peers (out of scope for code; document procedure).
For zero-disruption upgrade, accept legacy shares without secret for 90 days behind
`cfg.Federation.RequireSecret = false`; flip default to `true` in next minor.

---

## #3 — Block SSRF in federation client

**Scope:** `internal/federation/client.go:31-141`. `Discover`/`SendShare`/`Notify` follow
`remoteHost` (from user form) and `provider.EndPoint` (from peer response). No private-IP block,
no DNS rebinding defense, default redirect-following.

**Design:**
1. **Block private/loopback/link-local/multicast/CGNAT/reserved ranges** by validating the
   resolved IP, not the hostname. Use `net.DefaultResolver.LookupIP`; if any address is in a
   block list, refuse.
2. **Custom `http.Transport.DialContext`** that performs the lookup and IP-checks before
   `net.Dial`, so DNS rebinding (TOCTOU between lookup and dial) is impossible — pin the IP we
   validated as the actual dial target.
3. **Disable redirect following:** `client.CheckRedirect = func() error { return http.ErrUseLastResponse }`.
   Federation discovery returns a JSON document; redirects are not expected.
4. **Require HTTPS:** reject `http://` `remoteHost` unless `cfg.Federation.AllowPlaintext=true`
   (default false). `normalizeHost` already prepends `https://` — verify.
5. **Tight timeouts:** dial 5s, response header 10s, total 30s.

**Files:**
- New `internal/federation/safedial.go` — `SafeDialContext` helper with prefix block list:
  `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`,
  `192.0.0.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `224.0.0.0/4`, `240.0.0.0/4`, IPv6 ULA/LL/
  loopback equivalents. Configurable via `cfg.Federation.ExtraBlocks` and `AllowPrivate` (tests
  only).
- `internal/federation/client.go` — replace bare `http.Client{}` with one using the safe
  transport. Set `CheckRedirect`. Enforce HTTPS.
- `internal/federation/safedial_test.go` — table tests for every block range, dual-stack, and the
  rebinding TOCTOU pin.

**Tests:** httptest server bound to `127.0.0.1` — confirm refusal unless `AllowPrivate=true`.
Stub resolver returning private IPs for a public hostname — confirm refusal.

**Rollout:** local-dev integration tests need `AllowPrivate=true` flag.

---

## #5 — Federation token WebDAV must go through files plugin

**Scope:** `internal/federation/webdav_token.go:208-241`. `handlePut`/`handleMkcol`/`handleDelete`
write the local filesystem directly, bypassing files-plugin metadata, quota, indexing, activity
log.

**Design:** replace direct `os.Create`/`MkdirAll`/`RemoveAll` with calls to `filesPlugin.Plugin`
methods. The token resolves to a `(ownerUsername, basePath)` pair; obtain the relative path
within owner's namespace and call `files.Upload(ctx, owner, relPath, body)` /
`files.CreateDir(...)` / `files.Delete(...)` exactly as the WebDAV compat path does.

**Files:**
- `internal/federation/webdav_token.go` — inject `*filesPlugin.Plugin` (or a small interface
  `tokenFiles` exposing only the methods used) via the `TokenWebDAVHandler` struct. Replace the
  three handlers with calls into that interface. Use `r.Context()` for cancellation.
- `internal/web/routes.go` — pass the files plugin into `TokenWebDAVHandler` at construction.
- New `internal/federation/webdav_token_test.go` — exercise PUT/MKCOL/DELETE with a real
  `cogtest.New`, then assert metadata exists (`files.GetByPath`), quota counted, search-index
  hook fired.

**Tests:** as above. Plus a regression test for the path-traversal `HasPrefix` bug (#39 in
security.md) — fixed incidentally by routing through the plugin, but assert directly.

**Rollout:** zero data migration (no behavioural change for valid clients; orphan files from
prior writes need a one-shot reconcile that the files plugin already supports).

---

## #6 — `Secure` flag on every cookie

**Scope:** every cookie in the codebase. Currently none set `Secure`, so session/MFA/flash cookies
travel over plaintext HTTP.

**Design:** add a `cfg.SecureCookies *bool` config (nil → auto-detect from `BaseURL` scheme; true
forces always; false disables — for HTTP-only dev). Centralize cookie construction in
`internal/auth/cookie.go` exposing `Set(w, name, value, opts CookieOpts)` and `Clear(w, name)`.
Every call site must use these helpers.

**Files:** all cookie call sites listed in security.md #6:
- `internal/auth/middleware.go:49`
- `internal/web/login.go:97,137,231,254,545,559`
- `internal/web/flash.go:34,51`
- `plugin/files/pages.go:729,744`
- `internal/web/groups.go:490`
Plus `internal/compat/login.go` if it sets any.

Pass the `CookieConfig` through `services.Providers` and thread it to all handlers.

**Tests:**
- `internal/auth/cookie_test.go` — assert `Secure: true` when `BaseURL` scheme is `https`, false
  otherwise.
- Smoke test through `cogtest.New` (which uses `http://` test server) — auto-detect must NOT
  break that path. Add an explicit override in `cogtest.New` so all integration tests still pass
  cookies back.

**Rollout:** any operator running Cog over plain HTTP must set
`cfg.SecureCookies = false` explicitly (will be obvious — they'll fail to log in). Document in
upgrade notes.

Also worth bundling here: set `HttpOnly: true` (already true on most), `SameSite: SameSiteLaxMode`
universally, `Path: "/"`, and on the session cookie consider `SameSite=Strict` since it's only
read by Cog routes.

---

## #8 — OCS share `GET` IDOR

**Scope:** `internal/compat/sharing.go:474-505`. Handler resolves share by ID and returns it with
no requester-vs-owner-vs-subject check.

**Design:** before returning, verify the requester is one of:
- the share owner (`rec.OwnerUserID == u.ID`),
- the share subject (`SubjectType=USER && SubjectID==u.Username`), or
- a member of the subject group, or
- (for `SubjectType=PUBLIC`) any authenticated user, but strip sensitive fields (`Token`,
  `Password`) before returning.

If none of the above, return 404 (not 403 — avoid existence oracle).

**Files:**
- `internal/compat/sharing.go:474-505` — add guard.
- Helper `shareVisibleTo(rec *share.Record, u *user.User, groups GroupChecker) bool`.
- `internal/compat/sharing_test.go` — add tests: foreign user gets 404, owner gets full record,
  subject user gets full record, group member gets full record, public + non-owner gets
  redacted record.

Also audit sibling endpoints (`HandleListShares`, `HandleDeleteShare`, `HandleUpdateShare`) for
the same flaw — they probably exist.

**Tests:** as above. Plus `NCIDFor` enumeration test that confirms a brute-force over share IDs
yields only the requester's shares.

**Rollout:** local. No migration.

---

## #9 — Send-after-close panic in search collector/propagator

**Scope:**
- `internal/search/collector.go:55-72` — `Send()` writes to `c.ch`; `Stop()` closes `c.ch`.
- `internal/search/propagator.go:59-89` — `OnShareCreate/Delete` write to `p.ch`; `Stop()` closes
  `p.ch`. The `select … default` pattern doesn't prevent send-to-closed-channel panic.

**Design:** replace channel-close shutdown with a quit channel + atomic stopped flag. Pattern:
```go
type Collector struct {
    ch       chan event
    quit     chan struct{}
    stopped  atomic.Bool
    done     chan struct{}
}

func (c *Collector) Send(e event) {
    if c.stopped.Load() { return }
    select {
    case c.ch <- e:
    case <-c.quit:
    default:
        // drop on full
    }
}

func (c *Collector) Stop() {
    c.stopped.Store(true)
    close(c.quit)
    <-c.done    // wait run() exit
    // ch left to GC; never closed
}
```

`run()` selects on `c.ch` and `c.quit`; drains `c.ch` after quit before returning.

**Files:**
- `internal/search/collector.go:55-125` — rewrite.
- `internal/search/propagator.go:59-95` — rewrite same pattern.
- `internal/search/manager.go:148-167` — `Stop` should wait for both before closing the index.

**Tests:**
- `internal/search/collector_test.go` — stress test: 1000 goroutines `Send` while another calls
  `Stop`. No panic.
- Same for propagator.

**Rollout:** local.

---

## #10 — `UserDBHook` re-enters SQLite from inside update callback

**Scope:** `internal/search/manager.go:92-114`. The SQLite update hook is synchronous on the write
path; the lazy path calls `ensureUserIndex` → `collector.FullSync` which issues
`SELECT rowid FROM <table>` against the same connection that's mid-write. Deadlock or undefined
behavior under load.

**Design:** the update hook must do no SQL of its own. Two changes:
1. Replace synchronous `ensureUserIndex` call inside the hook with a non-blocking signal — drop
   the (table, rowid) onto the collector's channel; the collector's own goroutine handles
   FullSync off the SQLite write path.
2. `ensureUserIndex` lazy-init becomes the responsibility of the *collector goroutine*, not the
   hook callback. Hook just buffers events; first event triggers lazy init in the goroutine.

```go
func (m *Manager) UserDBHook(username string) func(op int, db, table string, rowid int64) {
    return func(op int, db, table string, rowid int64) {
        col := m.collectorFor(username)  // cheap, map lookup + mu
        col.Send(event{table, rowid, op})
    }
}
```

The `collectorFor` lookup uses a sync.Map or fine-grained mutex (NOT `m.mu` held across blocking
work, see audit §Concurrency).

**Files:**
- `internal/search/manager.go:92-247` — refactor lazy init out of the hook path; reduce `m.mu`
  scope to only the map mutation.
- `internal/search/collector.go` — own its lazy-init / FullSync state.
- `internal/search/manager_test.go` — add: write-burst from a goroutine while another calls
  `ensureUserIndex` explicitly; assert no deadlock with a 5s test timeout.

**Tests:** as above. Add a `go test -race` run to CI explicitly for `internal/search`.

**Rollout:** local. The first write after a process restart no longer eagerly opens the index, so
queries against an unopened index return empty until the collector finishes FullSync. Acceptable
— existing behavior already async at the index-write boundary.

---

## Order of execution

1. **Branch 1 — single PR, security release** (#1, #2, #3, #5, #6, #7, #8). Internally
   independent files; ship together to avoid leaving partial defenses on master.
2. **Branch 2** — #9 + #10 together (search subsystem refactor).

Tag a security release after Branch 1 merges. Backport policy: none (no prior release line).

## Verification checklist (per PR)

- `go tool verify` clean.
- `go test -race ./...` clean.
- Hand-test each scenario per the per-fix Test section.
- For #1, #2, #3, #5, #6, #7, #8 — add an entry to `CHANGELOG.md` under "Security" with the CVE-like
  ID `COG-AUDIT-NN`.
