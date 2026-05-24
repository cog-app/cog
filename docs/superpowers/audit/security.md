# Security Audit

Audit of `master` @ `0c018ef`. Findings grouped by severity. Cites are `file:line`.

## Critical

1. **Unauthenticated pprof endpoints** — `internal/web/routes.go:38-42`. `GET /debug/pprof/*`
   (Index, Cmdline, Profile, Symbol, Trace) registered on the bare mux. `authMW.Wrap` wraps the
   mux but pprof itself enforces no auth — heap dumps expose CSRF/encryption secret in
   `services.Providers.CSRFSecret`, password hashes, plaintext TOTP secrets in flight, session
   IDs, app password plaintext during creation. Profile/Trace enable CPU-pin DoS.

2. **Trusted client-supplied IP headers for rate limiting** — `internal/ratelimit/ratelimit.go:93-110`.
   `ClientIP` blindly trusts `X-Forwarded-For` then `X-Real-IP` with no "trust proxy" config.
   Defeats brute-force protection on login / MFA / registration / forgot-password / grant. With
   spoofed XFF the 10^6 EmailOTP space is attackable.

3. **OCM federation accepts anonymous remote mutation without signing** — `internal/federation/ocm_handler.go:39-155`,
   `internal/web/routes.go:299-300`. `POST /ocm/shares` creates an inbound `FederatedShare` for any
   local user from any unauthenticated caller. `POST /ocm/notifications` mutates outbound shares
   keyed only by guessable provider ID. No peer allowlist, no signature, no shared secret.

4. **SSRF in federation client** — `internal/federation/client.go:31-141`. `remoteHost` comes from
   user form (`shareWith=user@host`), used directly to fetch `/.well-known/ocm-provider`, `/shares`,
   `/notifications`. No private-IP/loopback/link-local block, no DNS rebinding defense. Default
   redirect following compounds. Authenticated users probe internal nets / `169.254.169.254`.

5. **Federation token WebDAV writes bypass files plugin and quota** — `internal/federation/webdav_token.go:208-241`.
   PUT/MKCOL/DELETE write via `os.Create`/`os.MkdirAll`/`os.RemoveAll` directly. No metadata insert,
   no FileID/ETag/MTime, no quota, no indexing, no activity log. Remote peer with write fills disk
   and creates orphan files invisible to owner's normal listing.

6. **No `Secure` flag on any cookie** — `internal/auth/middleware.go:49`, `internal/web/login.go:97,137,231,254,545,559`,
   `internal/web/flash.go:34,51`, `plugin/files/pages.go:729,744`, `internal/web/groups.go:490`. Session,
   MFA-pending, and flash cookies (flash carries plaintext app passwords on creation, see High #21)
   ride any HTTP request. Combined with #1 pprof leak, downgrade attacks are easy.

## High

7. **CSRF secret reused as AES key for TOTP secrets** — `internal/auth/totp.go:21-22`. Same bytes
   feed `hmac.New(sha256.New, secret)` for CSRF AND `aes.NewCipher` for TOTP seeds. One compromise
   = both compromised.

8. **TOTP `BeginSetup` overwrites confirmed secret without re-auth** — `internal/auth/totp.go:27-47`.
   CSRF-protected POST replaces `users.totp_secret`. Hijacked-session attacker can brick the
   legitimate user's authenticator while keeping MFA "enabled".

9. **TOTP `Validate` has no replay protection** — `internal/auth/totp.go:73-85`. Same code reusable
   within the 30s window. Violates NIST SP 800-63B.

10. **Email OTP has no per-account attempt counter / lockout** — `internal/auth/emailotp.go:59-80`.
    6-digit code. IP rate limiter is spoofable (#2). `Validate` bcrypts every unused row per
    attempt — repeated wrong codes succeed indefinitely until expiry.

11. **App password validation is O(N) bcrypt per request** — `internal/auth/apppassword.go:106-132`.
    `Validate` loads every `app_passwords` row and bcrypts. No per-user cap in `Create` (line 33);
    one user spawning thousands DoS's the bearer auth path.

12. **Password reset tokens stored in plaintext** — `internal/auth/passwordreset.go:27-50`. Anyone
    with DB read (backup, file leak, SQL inj, admin shell) takes over any account with an
    outstanding reset. Same plaintext-at-rest for `mfa_pending.token` and `webauthn_sessions.session_data`.

13. **WebDAV `Content-Disposition` filename header injection** — `internal/compat/webdav.go:473`.
    `fmt.Sprintf` of stored `meta.Name` with embedded `"` or CR/LF lets the user inject headers
    into their own response.

14. **WebDAV PROPPATCH/LOCK responses inject unescaped XML** — `internal/compat/webdav.go:526-576`
    (PROPPATCH), `630-651` (LOCK). `hrefPrefix + filePath` and `u.Username + "-" + filePath` go
    raw into `fmt.Fprintf` XML. Filenames with `<`, `&` survive `path.Clean`. Same at
    `internal/federation/webdav_token.go:188-206`.

15. **`csrfReject` open-redirect via `Referer`** — `internal/auth/csrf.go:90-95`. 303-redirect to
    `r.Header.Get("Referer")` with no same-origin check.

16. **OCS `GET .../shares/{shareId}` IDOR** — `internal/compat/sharing.go:474-505`. Returns share
    by ID with no owner/subject check. Public-link tokens, expiry, target identity leak across
    users. `NCIDFor` gives stable integers — enumeration is trivial.

17. **Login Flow grant has no CSRF and no session binding** — `internal/compat/login.go:139-237`.
    `processGrant` accepts username+password POST and mints an app password against the *attacker's*
    loginToken. Phishing primitive.

18. **MFA pending `nextURL` stored unvalidated** — `internal/auth/mfapending.go:25-78`,
    `internal/web/login.go:241-247`. `safeNext` is applied in `createSessionAndRedirect` (good) but
    the raw `next` flows into form actions via `loginRedirect`. One-refactor-away from open redirect.

19. **`CreatePublicLinkForm` allows shares for resources the user does not own** —
    `internal/web/share_handler.go:130-160,89-127`. No ownership check before
    `Shares.CreatePublicLink(u.ID, resType, resID, ...)`. For plugins that resolve resource by ID
    without scoping by owner, this is privilege escalation.

20. **Basic Auth auto-creates `sessions` row per request** — `internal/auth/middleware.go:46-57`.
    Long-running NC client traffic produces unbounded session rows. No per-user cap.

## Medium

21. **Plaintext app password in flash cookie** — `internal/web/settings.go:85`. `"token:"+plaintext`
    in a cookie without `Secure` (#6).

22. **Login Flow `HandlePoll` has no rate-limit** — `internal/compat/login.go:94-135`. Mutex
    contention under spam.

23. **Forgot-password iterates entire `users.List()` to match email** — `internal/web/login.go:373-388`.
    User enumeration via timing.

24. **`HandleRegister` leaks user existence via error string** — `internal/web/login.go:316`.

25. **`Authenticate` short-circuits before bcrypt for non-existent users** — `internal/user/user.go:113-127`.
    Username enumeration via timing.

26. **WebAuthn `BeginRegistration` accepts caller-supplied displayName** — `internal/auth/webauthn.go:66-93`.
    Cosmetic but spoofable.

27. **TOTP `regenerateBackupCodes` not transactional** — `internal/auth/totp.go:151-182`. Mid-failure
    leaves partial code set.

28. **TOTP `Disable` not gated on TOTP re-auth or current password.** Hijacked-session attacker
    removes MFA.

29. **`emailotp.generateOTPCode` ignores `rand.Int` error** — `internal/auth/emailotp.go:83-86`.
    Returns deterministic `"000000"` on crypto/rand failure.

30. **`webdav.go createDirectToken` ignores `rand.Read` error** — `internal/compat/webdav.go:417-418`.
    Token becomes zero string on rand failure.

31. **`totp.generateBackupCode` ignores `rand.Read` error** — `internal/auth/totp.go:189`. Modulo
    bias on 256 % 31 is acceptable.

32. **CSRF token is deterministic HMAC of session ID** — `internal/auth/csrf.go:11-25`. Same session
    → same token forever. Leak = lifetime compromise.

33. **CSRF middleware accepts token from query string via `FormValue`** — `internal/auth/csrf.go:31-32`.
    Token in URL log = valid token.

34. **TOTP backup code lookup bcrypts every unused row.** Bounded by 10 codes; lower risk than #11.

35. **`OCMShare.Owner` host portion trusted** — `internal/federation/ocm_handler.go:73-99`. Stored
    `remoteHost` feeds callbacks → SSRF pivot from inbound shares (#4).

36. **No brute-force protection on public-share password** — `internal/share/share.go:160-175`,
    `plugin/files/api.go:510-590`. Bcrypt slow but no per-token attempt counter.

37. **No session-ID rotation on privilege change.** Password change / MFA enable / admin grant
    keeps the same session ID.

38. **`MarkUsed` for password reset fire-and-forget** — `internal/web/login.go:457`. Combined with
    no `used` check inside `Validate` running in same tx → TOCTOU on token.

39. **Federation prefix-traversal check is weak** — `internal/federation/webdav_token.go:103`.
    `strings.HasPrefix(localPath, basePath)` missing trailing-sep guard. Not currently reachable
    via `filepath.Join`, but fragile.

40. **API endpoints rely on `SameSite=Lax` for CSRF** — `internal/web/routes.go:32-48,269-273`.
    Lax doesn't block top-level form-POST. Future state-changing GET endpoints would be exposed.

41. **Admin SMTP-test renders error strings as HTML** — `internal/web/admin.go:605,611`. Admin-on-admin
    persistence via crafted templates.

42. **`avatar.Serve` opens file by DB-stored name** — `internal/web/avatar.go:35-49`. Code path
    sets `u.ID + ext`, but DB column accepts any string. Defense-in-depth: `filepath.Base`.

43. **`extractPath` reflects URL `user_id` into multi-status XML hrefs** — `internal/compat/webdav.go:96-114,131,558-559,599`.
    Combined with #14 → XML injection into peer parsers.

44. **`activity.Store.Log` fire-and-forget** — `internal/compat/sharing.go:374-380`. Audit log
    writes vanish on DB outage.

## Low

45. **Many swallowed errors** (CLAUDE.md says don't):
    - `emailotp.go:31,32,75`, `mfapending.go:39,73`, `passwordreset.go:29,31`, `webauthn.go:309,352`,
      `totp.go:144`
    - `login.go:230,252,253,544` — MFA-pending delete, session delete on logout, app-password create
    - `share.go:153,209` — `RowsAffected` discarded
    - `sharing.go:573-574` — refetch errors after Update
    - `chunked.go:241` — `os.RemoveAll` cleanup ignored

46. **Logout doesn't invalidate concurrent MFA-pending / webauthn sessions** — `internal/web/login.go:250-262`.

47. **`mfa_pending.next_url` has no length cap or scheme check** — `internal/auth/mfapending.go:37-56`.

48. **OCS sharees discloses full user/group list to any authenticated user** — `internal/compat/sharing.go:622-754`.

49. **`createSessionAndRedirect` does not delete previous session on re-login** — `internal/web/login.go:130-145`.

50. **`@templ.Raw(item.Icon)` in `ui/layout.templ:36,43,59,...` renders plugin-supplied SVG verbatim.**
    Privileged today; XSS-by-plugin once external plugins land.

51. **`Authenticate` does not record per-user lockout** — only per-IP.

52. **MFA-pending cookie ↔ DB row bound only by random token equality.** No signed binding.

53. **`forgot-password` leaks user existence via outbound SMTP traffic** — `internal/web/login.go:390-405`.

54. **`reset-password` echoes token into URL on every redirect** — `internal/web/login.go:435,440,453`.
    Logs / Referer leak.

55. **`storeSession` (`webauthn.go:307-327`) deletes prior pending sessions before insert** — no tx.

56. **Login Flow `pending` map unbounded** — `internal/compat/login.go:73-78`. `Cleanup()` not
    periodically called. Spam `HandleInit` → memory exhaustion.

57. **`MFAPending.Delete` and `Sessions.Delete` errors ignored at logout** — `internal/web/login.go:250-252,230`.

58. **Avatar GET initials path interpolates user-controlled initials into SVG text body** — bounded
    by `initials()` to two runes, not currently XSS.

## What looks fine

- bcrypt + `CompareHashAndPassword` (timing-safe) throughout.
- `safeNext` rejects scheme/protocol-relative redirects.
- `xmlEscape` used in OCS XML output (`internal/compat/ocs.go`).
- Path normalization in files plugin via `filepath.Clean("/"+p)` prevents `..` escapes.
- Public-link tokens 24 bytes `crypto/rand` (192 bits); `generateID` 16 bytes.
- AES-256-GCM with fresh random nonce per encryption (`encrypt.go:22-26`).
- WebAuthn validation delegated to go-webauthn; sign-count cloning detection persists.
- All parameterized SQL on the auth/sharing/compat/web paths read. `fmt.Sprintf` SQL at
  `plugin/rss/store.go:382` and `plugin/bookmarks/store.go:81` uses whitelisted identifiers.
