# Top Findings

The smallest set of issues worth fixing first. Each line cites the source report and `file:line`.

## Fix-now

1. **Unauthenticated pprof at `/debug/pprof/*`** — leaks heap (CSRF/encrypt secret, password
   hashes, session IDs) and enables CPU-pin DoS. `internal/web/routes.go:38-42`. (security #1)

2. **Federation OCM mutation endpoints accept anonymous requests** — anyone on the internet can
   inject inbound shares and flip outbound shares. `internal/federation/ocm_handler.go:39-155`.
   (security #3)

3. **SSRF in federation client** — `user@host` form value flows directly into HTTP fetches with
   no private-IP block, no DNS rebinding defense. `internal/federation/client.go:31-141`.
   (security #4)

4. ~~**`MaxOpenConns=1` never set**~~ — **RETRACTED**. WAL + `_busy_timeout=5000` is correct.
   `MaxOpenConns=1` would deadlock on nested queries. CLAUDE.md line stale.

5. **Federation token WebDAV writes bypass the files plugin** — no quota, no metadata, no
   indexing; orphan files invisible to owner. `internal/federation/webdav_token.go:208-241`.
   (security #5)

6. **No `Secure` flag on any cookie** — sessions, MFA-pending, and flash (which carries plaintext
   app password on creation) ride HTTP. (security #6)

7. **Rate limiter trusts `X-Forwarded-For` unconditionally** — defeats login / MFA / OTP brute-force
   protection. `internal/ratelimit/ratelimit.go:93-110`. (security #2)

8. **OCS share `GET` IDOR** — any authenticated user reads any share by ID, with public-link
   tokens. `internal/compat/sharing.go:474-505`. (security #16)

9. **`search.Collector` and `search.Propagator` panic on send-after-close** —
   `internal/search/collector.go:55-72`, `internal/search/propagator.go:59-89`. (bugs §Concurrency)

10. **`UserDBHook` re-enters SQLite from inside its own update callback** — likely deadlock.
    `internal/search/manager.go:92-114`. (bugs §Concurrency)

## High-impact but more work

- **Plugin registry maps unsynchronized** — concurrent map writes on `r.enabled`, `r.plugins`,
  `r.discovered`. Runtime panic. `internal/plugin/registry.go:903-928,127-150,1021-1026`. (plugin §2.1-2.2)
- **No panic recovery anywhere in plugin invocation** — `Start`, `FireHook`, resource handler.
  Plugin panic kills host. (plugin §3.1)
- **gRPC plugins auto-discovered as trusted with host CSRF secret** — `cog-plugin-*` on PATH gets
  full privileges via env vars; no signature, no allowlist.
  `internal/plugin/grpc.go:266-270`, `internal/plugin/registry.go:1021`. (plugin §1.2, §1.3)
- **No `defer recover` around `FireHook` subscribers** — a buggy subscriber takes down the firing
  request handler. (plugin §3.1, §7.2)
- **Password reset / MFA-pending / WebAuthn-session tokens stored plaintext at rest.**
  (security #12)
- **TOTP `BeginSetup` overwrites a confirmed secret without re-auth** — hijacked session bricks
  legitimate user's MFA. (security #8)
- **Trash / HardDelete / Rename not transactional** — crash leaves on-disk and metadata state
  inconsistent. `plugin/files/trash.go:29-84`, `plugin/files/files.go:422-471,474-531`. (bugs §Concurrency)
- **Quota pre-check on upload never fires** — handler doesn't wrap body in `*io.LimitedReader`.
  `plugin/files/files.go:256-260`. (bugs §TOCTOU)
- **CSRF secret reused as AES key for TOTP seeds.** (security #7)
- **Login Flow grant page has no CSRF and no session binding** — phishing primitive for app
  passwords. `internal/compat/login.go:139-237`. (security #17)

## Coverage gaps that hide regressions

- `plugin/files/api.go` — 22 HTTP handlers at 0% (auth, ownership, share password check).
- `internal/auth/` MFA: TOTP, WebAuthn, EmailOTP, password reset all at 0%.
- `internal/federation/webdav_token.go` token validation at 0%.
- `internal/migrate/migrate.go` migration runner + `parseColumnGuard` at 0%.
- gRPC plugin bridge (`internal/plugin/grpc.go`, `plugin/plugcmd/`) at 0%.
- Zero concurrency tests anywhere — no `t.Parallel` or `sync.WaitGroup` in tests.
- No WebDAV edge-case tests: Depth=infinity, Overwrite=F, MOVE/COPY out-of-namespace, Range
  requests, large-file PUT, `If-Match`/`If-None-Match`, PUT-to-directory.

## Cross-cutting hygiene

- Many swallowed errors (CLAUDE.md forbids). Concrete locations are in `security.md` #45 and
  `bugs-quality.md` §Error Handling.
- Several `context.Background()` calls inside HTTP handlers — disconnects don't propagate.
- Several routes return wrong status codes (500/409 for client errors; 404 swallowing DB errors).
- `RegisterRoute` (privileged plugins) doesn't wrap CSRF — every privileged plugin author must
  remember. (plugin §5.1, §11.8)
