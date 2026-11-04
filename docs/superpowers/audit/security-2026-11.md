# Security Audit — November 2026

Audit of `master` @ `25ff4de`, 2026-11-04, with fixes on the `security-audit` branch. It follows
[`security.md`](security.md) (May 2026), most of whose findings had been fixed. This pass covers the
code added since then: volumes and mounts, DAV/JMAP, SSH, the feed, recipes, gallery and profanity
plugins, and federation.

Every finding below is fixed. Regression tests are named after their finding where one fits:
`internal/web/audit_security_test.go`, `internal/ssh/auth_audit_test.go`,
`internal/federation/{peers,webdav_token}_test.go`, `plugin/gallery/pprof_audit_test.go`,
`plugin/profanity/tts_audit_test.go`, `plugin/feed/remote_test.go`.

## Critical

**C1. System volumes open to every user.** `volume.Access.CanAccess` returned true for any volume
without an owner. The default volume *is* the data directory, so `/volumes/{default}/browse` let any
user list, download, upload and delete every other user's files.
*Fix:* a system volume is reached by admins, or through a share an admin made
(`internal/volume/access.go`). The volume browser asks for read or write permission per action.

**C2. Mounting other users' files.** `POST /settings/mounts` took any `volume_subpath` on any
reachable volume, the default one included (`bob/files`, or `""` for the whole data directory).
Mounts of read-only shares were writable, and a mount kept working after its share was revoked.
*Fix:* mounting needs read access to the volume, and a share without write permission gives a
read-only mount. `mount.Resolver.UserFS` re-checks access for every mount other than the root one
each time it builds the filesystem, so revoking or narrowing a share takes effect.

**C3. Host filesystem as a volume.** `POST /settings/volumes` accepted `conn_type=file` with any
path, `/` included. User WebDAV volumes used a plain `http.Client` (SSRF).
*Fix:* users can add only WebDAV volumes, dialled through `safehttp`. `volume.NewBackend` refuses
a local-path volume that a user owns, in case one is already in the database.

**C4. Sharing what is not yours.** `/api/shares/create`, `/api/shares/public-link-form` and
`/api/shares/federated` never checked ownership, and `share.CheckAccess` counted shares from anyone.
A user who shared someone else's volume ID with themselves got access to that volume.
*Fix:* the generic share handlers require ownership (`internal/web/share_owner.go`: volume owner,
DAV collection owner, or the row in the user's own plugin database). `share.CheckAccessGrantedBy`
counts only the resource owner's shares; volumes and the files shared-download use it.

## High

**H1. MFA bypass.** Basic auth with the primary password authenticated a user who had MFA set up.
A browser request even got a session cookie. SSH passwords did the same.
*Fix:* for an MFA user, the password alone is refused on Basic auth and SSH; app passwords and keys
still work. Login Flow grants require a session, so a Basic-auth browser cannot mint one.

**H2. Shell injection in the profanity plugin.** The system TTS command replaced `$TEXT` with the
user's wordlist replacement text and ran it under `sh -c`.
*Fix:* `TEXT` and `OUTPUT` are passed as environment variables, which the shell expands as data.

**H3. Feed plugin SSRF and yt-dlp argument injection.** The feed fetchers, kind detection and
media download used `http.DefaultClient`. yt-dlp got the URL as a bare positional argument, so a
URL beginning with `-` was read as an option, and overriding `kind=youtube` skipped validation.
*Fix:* `feed.HTTP` (`safehttp`) for every fetch; `feed.CheckURL` (http/https only) at subscription
time, at fetch time and before every yt-dlp call; `--` before the URL.

**H4. Recipe import SSRF.** *Fix:* `safehttp` transport, HTTP/2 kept on.

**H5. `/public.php/webdav` public-link fallback.** It ignored link passwords, and it served any
share's token as a file share: a calendar feed token (a published URL) whose collection ID equalled
a file ID exposed that file, or that folder's whole tree.
*Fix:* only `files:file` / `files:directory` public links are served, and a password-protected
link asks for it as Nextcloud does (token as username, password as password).

**H6. Unauthenticated gallery worker pprof** (`/apps/gallery/api/worker/pprof/*`): heap profiles of
the scan worker, plus a CPU profile that anyone could start. *Fix:* admin only. The memories
API now requires a login; without one it panicked.

**H7. Stored XSS through files.** `?raw=1`, archive entries and WebDAV GET served user HTML/SVG
inline on the site's origin. On a shared volume that hits other users.
*Fix:* `plugin.UntrustedContent` sets `Content-Security-Policy: sandbox` and `nosniff` on user
content (PDFs are left out of the sandbox, because browsers will not render them inside one). Every
response also carries `nosniff` and `X-Frame-Options: SAMEORIGIN`.

**H8. No SSH password rate limit.** *Fix:* limited per address and per account, through the
shared limiter.

## Medium

- **Open redirect:** `safeNext` accepted `/\evil.example`. Both it and the MFA-pending check now use
  `auth.LocalPath`, which also rejects control characters.
- **MFA brute force:** attempts were limited only per IP; they are now also limited per account.
- **TOTP replay:** a code could be used again within its window. `users.totp_last_step`
  (`migration027.sql`) records the last accepted step, and a code is accepted only for a later step,
  in one statement.
- **Quota bypass:** SFTP and SCP writes skipped quota, and chunked-upload chunks were unbounded until
  assembly. All three now check through `Registry.QuotaCheck`.
- **SCP setuid:** SCP took mode bits from the client; they are masked to permission bits.
- **Federation was open by default:** an empty allowlist accepted inbound shares from any server.
  It is now off until an admin turns it on (Admin → Settings → Federation), with an optional
  allowlist. `COG_FEDERATION_PEERS` still works: it adds to the allowlist, and enables federation
  when no admin has set it. When federation is off, the server neither sends nor accepts shares, and
  remote servers lose access to what was shared with them. The peer check runs before the user
  lookup, so a closed server doesn't reveal which accounts exist. OCM bodies are capped at 64 KiB.
- **Invitations opened accounts while registration was closed,** and any user can make a group and
  invite to it. Now only an admin's invitation can create an account, unless public registration is on.
- **ffmpeg reads user media:** an uploaded "video" or a downloaded "episode" can be an HLS playlist
  or concat list that makes ffmpeg read other files. Every ffmpeg/ffprobe run on user media now goes
  through `internal/ffargs`: `-protocol_whitelist file` plus a whitelist of audio/video demuxers.
- **Password reset race:** the token was checked and then marked used, so two concurrent resets
  could both succeed. `PasswordResetStore.Consume` does both in one `UPDATE … RETURNING`.
- **Email change without re-authentication** let a hijacked session redirect password resets.
  Changing the email address now needs the current password.

## Low

- **Login CSRF:** login, register, MFA, forgot/reset password, logout and invite acceptance are
  wrapped in `http.CrossOriginProtection`.
- **Invite revocation:** it is now scoped to the invite's own group.
- **Feed links:** item links go through `templ.URL`, so a feed cannot supply a `javascript:` link.
- **Login Flow:** the redirect token is escaped, and the CSRF comparison is constant-time.
- **Federation WebDAV proxy:** this unused code now uses `safehttp`.

## Checked and fine

- App passwords are hashed and looked up by ID. Sessions, reset tokens and MFA-pending tokens are
  hashed at rest.
- The TOTP key is derived with HKDF and is separate from the CSRF secret.
- Trusted-proxy handling of `X-Forwarded-For`; pprof under `/admin`; `Secure`/`HttpOnly`/`SameSite`
  on cookies.
- DAV and JMAP authorise through granted collections and check that a share's owner is the
  collection's owner. Feed links compare the link owner with the collection owner.
- The OCS share endpoints check ownership and visibility.
- Archive extraction goes through the files plugin and skips symlinks. SFTP refuses symlinks.
- SQL is parameterised. The identifiers that are interpolated come from schemas or constants.
- Notes markdown and feed HTML are sanitised with bluemonday. Calendar attachment and contact links
  are limited to http/https.
