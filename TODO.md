These tasks should be completed by Claude in priority order. Check them off the list as they're completed.

- [-] Handle zip/tar files
  - Allow browsing the contents like a regular folder (updates for zip)
  - Allow extracting contents
- [ ] File versioning
  - git-tracked files/directories
  - all files are ignored by default, unless tracking is enabled
- [ ] Backup plugin with Restic REST API
- [-] Plugin installation and permissions
  - [-] Enforce DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND, etc
  - [-] Audit and minimize plugin API
  - [-] Enable trusted protobuf plugins
  - [-] Allow admins to install trusted plugins
  - [-] Allow users to install external plugins for themselves
  - [ ] SQLite methods for read_file(), head_file(), file_exists(), file_size()
- [-] Federate sharing (with NextCloud and Cog)
- [ ] Allow import from NextCloud via API (for individual user) / Database (for instance)
- [-] Audit log activities to table and expose it via WebDAV
- [ ] Plugin: Deck
- [ ] Plugin: Map
  - Shows gallery locations
  - Shows contacts
  - Works with PhoneTrack app
- [ ] News: Filtering rules
  - Article skipper (search patterns?)
  - Ad hider
  - Profanity filter
- [ ] Plugin: Media Queue; subscribe to audio/video media sources and watch a playlist
  - Multiple media sources:
    - [ ] Podcasts over RSS
    - [ ] YouTube channels/playlists
    - [ ] General conference talks
  - [ ] Organize Sources into Groups
  - [ ] Allow playback filtering by automated media tags (audio/Sunday)
  - [ ] NextPod/GPodder API compatibility
  - [ ] Cache content locally
  - [ ] Skip media by patterns (search patterns?)
  - [ ] Content filtering (ad skipping; profanity blocking)
  - [ ] Includes a mobile app
  - Calendar
    - Allow subscribing to iCal subscriptions; expose those via CalDAV (so I don't need to configure it on each device and can make edits)

---

# Audit findings — 2026-11-04

Audit of `master` @ `870ae95`. It builds on the November security audit (`e271f1a`) and does not repeat what that fixed. It covers auth, routing, sharing, the plugin registry and gRPC bridge, the files plugin, NC WebDAV/chunked uploads, SSH/SFTP/shell, search indexing, previews, email and server lifecycle. The DAV/JMAP internals and most per-plugin page handlers were only spot-checked.

Items marked **(verified)** were reproduced with a throwaway test against the real handler stack; the rest come from reading the code. Severity is my estimate of impact and how easy each one is to exploit.

## Critical

- [ ] **C1. Usernames are not validated, and the raw username builds storage paths (verified).** `user.Store.Create` (`internal/user/user.go:65`) accepts any string. The username goes into `db.PluginDBPath` (`internal/db/plugindb.go:20`, `filepath.Join(dataDir, username, …)`), the root mount subpath (`internal/web/admin.go:119`, `internal/mount/bootstrap.go:39`), the search index path, and several plugin directories. A self-registered user named `z/../victim` gets a root mount that cleans to `victim/files` and plugin DBs at `victim/app_data/…`, so they have full read/write access to the victim's files and every plugin database. The probe registered `z/../victim` and read the victim's file over WebDAV. This is reachable through `/register` (when registration is open), invite acceptance (`internal/web/groups.go:541`), admin creation, and the CLI.
  - Names can also collide with server-owned entries in the data dir: `app_data` (the registry's root data dir, `internal/plugin/registry.go:111`; user `app_data`'s home is `dataDir/app_data/files`, which is the files plugin's global dir), `avatars`, `.search`, and `cog-chunk-*` (chunk staging lives in `DataDir`). The feed orchestrator treats every directory in the data dir as a username (`plugin/feed/orchestrator/orchestrator.go:84`).
  - Fix: enforce a strict username grammar (e.g. `^[a-z0-9][a-z0-9._-]{0,63}$`, no `..`) plus a reserved-name list in `Create`. Longer term, key on-disk storage by user ID, not username. That also fixes H9 and makes renames possible.

## High

- [ ] **H1. Directory trash/delete/rename uses `LIKE 'dir/%'`, which hits sibling directories (verified).** `plugin/files/trash.go:88`, `plugin/files/files.go:578` (HardDelete) and `files.go:648` (Rename children) build `path LIKE ?` from a raw path. SQLite `LIKE` is case-insensitive for ASCII and treats `_` and `%` as wildcards. Trashing `/Photos` deleted the metadata of `/photos/keep.txt` (verified). Renaming `/a_b` rewrites the paths of rows under `/aXb/`. Reconcile later re-adds the rows under new IDs, which breaks shares, favorites, comments and NC client file IDs. Fix: `substr(path, 1, length(?)) = ?` or a range comparison (`path >= 'dir/' AND path < 'dir0'`).
- [ ] **H2. Chunked uploads are assembled in lexical order (verified).** `internal/compat/chunked.go:241` sorts chunk names as strings. The NC v2 protocol allows unpadded numeric chunk names (`1`…`10000`), and Nextcloud sorts them numerically. Eleven chunks `1..11` assembled as `ajkbcdefghi`, which silently corrupts any upload with ten or more unpadded chunks. Related bugs in the same handler:
  - `OC-FileId` is built from the ETag instead of the file ID (`chunked.go:294`).
  - The `Destination` header is never URL-decoded (`chunked.go:224-232`), so `My%20File.txt` is stored literally.
  - A repeated MKCOL on the same transfer ID replaces the session without removing the old chunk dir (`chunked.go:124`). The old bytes stop counting toward staged quota but stay on disk forever, which bypasses quota and fills the disk.
  - Sessions are in memory only, so a restart orphans every `cog-chunk-*` dir in the data dir with no cleanup.
- [ ] **H3. An over-quota overwrite destroys the original file.** `Files.Upload` (`plugin/files/files.go:346-379`) writes atomically over the existing file, then runs the quota check. On failure it deletes the path, which by then holds the replaced file. The old content is gone and the DB row still describes it. The check also counts the old and new sizes together on overwrite, so an edit near the quota is falsely rejected. Fix: check quota before the rename, using the size delta, and on failure remove only the temp file.
- [ ] **H4. Quota can be bypassed by SFTP, SCP and the SSH shell, and disk usage is unbounded during uploads.**
  - Usage is `SUM(size)` from the files DB (`files.go:1185`). SFTP and SCP write through the VFS and never update that DB, so each file passes the check (`internal/ssh/sftp.go:83-98`) while the running total never grows until something lists the directory.
  - Shell redirects (`internal/ssh/shell/handlers.go:28`) and shell `cp`/`mv` have no quota check at all, and `/dev/zero` is available, so `cat /dev/zero > f` fills the disk.
  - `Upload` only pre-checks when given an `*io.LimitedReader`. WebDAV PUT and `/api/files/upload` pass `r.Body`, so the whole body is written to disk before any check.
  - Concurrent uploads race the check-then-write.
  - Fix: bound bodies with `Content-Length` or `OC-Total-Length` plus a `LimitReader` sized to the remaining quota, and count usage from a single source of truth.
- [ ] **H5. Preview converters can read arbitrary local files into a thumbnail.** `plugin/files/preview.go:269-284`:
  - ImageMagick is called on `x.psd[0]` and `x.xcf[0]` without a coder prefix. ImageMagick sniffs the content, so an MVG/MSL/SVG payload named `.psd` gets coders like `text:@/path` and `label:@/path`, and the result lands in another user's or the server's view as a thumbnail.
  - Inkscape renders linked `<image href="/abs/path.png">` from SVG/AI/EPS, which reads other users' images from the shared data dir.
  - LibreOffice (`:354`) resolves linked content in documents.
  - All three run as the server user with access to the whole data dir and network.
  - The PSD path also splices paths into Python source with Go `%q`.
  - Fix: force coders (`psd:`/`xcf:`), ship a restrictive `policy.xml`, pass paths via argv, and run converters sandboxed (bwrap/nsjail, no network, read-only bind of the single input).
- [ ] **H6. Decompression bombs crash the server.**
  - Viewing an archive entry reads the whole decompressed entry into memory (`plugin/files/pages.go:544`); a 10 MB zip entry that expands to 10 GB means OOM.
  - `image.Decode` runs without a `DecodeConfig` dimension check in thumbnails (`preview.go:195`), avatar upload (`internal/web/avatar.go`) and likely gallery, so a tiny PNG that declares 60000×60000 pixels has the same effect.
  - `serveContent` buffers whole files from non-local backends (`pages.go:305`), and archives on remote volumes buffer up to 500 MB per request (`pages.go:410`).
  - Fix: stream with `io.LimitReader` and cap entry sizes, check dimensions first, and use `http.ServeContent` only over seekable sources.
- [ ] **H7. Basic-auth brute force bypasses the rate limiter.** `internal/auth/middleware.go:106-137` limits Basic and Bearer auth by IP only (no per-account key like `/login` has) and resets the IP counter on any success. An attacker with any valid account, or with open registration, interleaves one good login every nine guesses and gets unlimited password guesses against every non-MFA account, over WebDAV, OCS or any API. Fix: add a per-username key, and stop letting one account's success clear another's failures.
- [ ] **H8. Password reset and password change leave app passwords and SSH keys active.** The reset (`internal/web/login.go:515`), the self-service change (`internal/web/settings.go:372`) and the admin reset revoke sessions only. App passwords minted through Login Flow and SSH keys an attacker added survive, so a reset does not lock out an intruder. The CLI `user passwd` (`cog_admin.go:152`) does not even revoke sessions. Fix: revoke app passwords on reset and offer to on change; list and confirm SSH keys and WebAuthn credentials.
- [ ] **H9. Deleting a user fails, or leaves a trap.**
  - `shares.owner_user_id`, `groups.owner_id`, `volumes.owner_id` and `group_invitations.invited_by` reference `users(id)` without `ON DELETE CASCADE`, and foreign keys are on. Deleting anyone who ever shared a file, owned a group or invited someone therefore fails with a constraint error.
  - `adminHandler.DeleteUser` has already deleted the user's mounts outside the transaction (`internal/web/admin.go:222`), so the surviving user loses their root mount.
  - When deletion does succeed, plugin DBs and files remain on disk, keyed by username, and a new account with the same name inherits all of them, including calendars, notes and the search index.
- [ ] **H10. The untrusted out-of-process plugin model does not isolate anything.** This matters before the "users install external plugins" TODO.
  - The gRPC proxy forwards every request header, including `Cookie: cog_session` and `Authorization` (`internal/plugin/grpc.go:251`), plus the global CSRF token (`:266`).
  - It copies every response header back, so a plugin can `Set-Cookie` the session.
  - Page HTML is embedded raw into the same-origin shell (`internal/plugin/registry.go:834`).
  - Any such plugin therefore has full account takeover of every user who opens it.
  - Resource routes (`/apps/{plugin}/{type}/…`, `registry.go:971-984`) are mounted with no auth, no user context and no CSRF.
  - The request body is read unbounded (`grpc.go:244`).
  - Fix: strip credentials, give each plugin a scoped token, allowlist response headers, serve plugin UIs from a separate origin or iframe sandbox, and wrap resource routes in auth+CSRF.
- [ ] **H11. Any `cog-plugin-*` executable on `PATH` is launched at startup, enabled by default.** `DiscoverProcessPlugins` (`registry.go:1179-1247`) treats "never configured" as enabled (`IsEnabled` defaults to true). A binary dropped in any `PATH` directory, including user-writable ones like `~/.local/bin`, runs on the next start. Fix: discovered plugins should default to disabled until an admin enables them.
- [ ] **H12. Email header injection and open mailing through calendar invites.** iMIP (`internal/dav/schedule.go:465-494`) builds `Subject:` from `SUMMARY`, where the iCalendar `\n` escape decodes to LF, and from the display name. `email.Send` writes `To:`/`Subject:` raw (`internal/email/email.go:96-99`). Any user can make the server send mail with arbitrary headers and body to arbitrary external addresses under its SMTP identity, with no rate limit. Fix: reject CR/LF in header values, RFC 2047-encode subjects (which also fixes non-ASCII subjects), validate addresses with `mail.ParseAddress`, and rate-limit outbound invites.

## Medium

- [ ] **M1. Session tokens are stored in plaintext.** `sessions.id` is the bearer token (`internal/auth/session.go:110,148`). Every other token is hashed, and the prior audit doc says sessions are too. Combined with sliding sessions that never expire, a DB backup or leak means live sessions indefinitely. Store `HashAtRest(token)` and keep the cookie as the raw value. The CSRF token is HMAC(session ID), so derive it from the hash or keep a separate handle.
- [ ] **M2. The HTTP server has no timeouts and no graceful shutdown.** `http.ListenAndServe` (`cog_serve.go:75`, `cog.go:235`) means no `ReadHeaderTimeout`, so it is open to slowloris. SIGTERM kills the process without running `p.Close()`: bleve indexes are not closed, plugin child processes are orphaned and not reaped, and in-flight work is cut. `cog.Run(ctx, …)` ignores `ctx` for shutdown.
- [ ] **M3. `cog` builds two full service graphs, and every CLI command spawns plugins.** `Main` calls `addPluginCommands` → `services.New` → `DiscoverProcessPlugins` before parsing args (`cog.go:137,148`, `internal/services/services.go:279`). `cog serve` then builds a second `Providers`. Every server therefore runs two copies of each process plugin, the first set is never stopped (only its DB is closed), and commands like `cog user list` launch every plugin binary, run migrations and bootstrap mounts.
- [ ] **M4. Search propagation of shared items leaks after revocation and goes stale.** Shared documents are copied into recipients' indexes, but only the OCS sharing handler and the generic share UI call the propagator (`internal/web/routes.go:282`, `internal/web/share_handler.go:480`). Shares created or deleted via `/api/shares`, DAV sharing, plugin `ShareStore`, expiry, group membership removal and user deletion never propagate. Recipients keep searching content they no longer have access to. Copies are snapshots, so owner edits never reach them either. Hooking `share.Store.OnChange` covers creates and deletes. Expiry, group changes and edits need either a query-time ACL filter over owners' indexes or a reconciliation pass.
- [ ] **M5. The search change queue grows without bound for users whose index is closed.** The drainer skips users with no open index (`internal/search/drain.go:171`). Full sync on open does not truncate `_cog_row_changes`. Background writers (feed poller, WebDAV sync clients) keep appending for users who never search. User indexes are never closed or evicted once open, and `SelfSync` loads every document into memory on each open after a restart.
- [ ] **M6. Files metadata only follows writes made through the files plugin.**
  - SFTP, SCP, the shell and other plugins write to disk directly. `reconcile` (`files.go:686-776`) only inserts new rows: it never removes rows for deleted files and never updates size, mtime or ETag for changed ones. NC/WebDAV clients never see those edits (the ETag is unchanged), quota and search are wrong, and deleted files linger.
  - `Upload` also creates no metadata rows for missing parent directories, so `DELETE` of a parent made implicitly by `PUT /a/b.txt` returns 404 until something lists the root (seen while probing H1).
  - Fix: route all writers through one API, or have reconcile compare stat data in both directions.
- [ ] **M7. WebDAV correctness** (`internal/compat/webdav.go`):
  - `href`s are not percent-encoded (`:148,164`), so names with `#`, `?`, `%`, spaces or non-ASCII break NC clients.
  - `extractPath` unescapes an already-decoded `r.URL.Path` (`:111`), so a literal `%41` in a name is addressed as `A`.
  - `COPY /a` to `/a/b` recurses into its own output until the disk fills (`:388`).
  - MOVE ignores `Overwrite` and always returns 201; onto an existing path it overwrites on disk, then the DB update hits the UNIQUE constraint, leaving disk and metadata inconsistent (`:292`).
  - GET has no Range support (`:229`, and the direct-download endpoint the Android media player uses); `http.ServeContent` would provide it.
  - PROPPATCH, REPORT and comments read bodies unbounded (`:570,630`, `comments.go:150`).
  - `LOCK` is a stub that always succeeds, so concurrent editors clobber each other.
  - `oc:permissions` is always `RGDNVCK`, even on read-only mounts.
- [ ] **M8. Public-link password lockout trusts `X-Forwarded-For` from anyone.** `clientIPForLockout` (`plugin/files/api.go:891`) takes the leftmost XFF value, so an attacker rotates it to get unlimited share-password guesses. The lockout map is also never pruned (memory growth) and uses `time.Now()` instead of the clock. Use `ratelimit.ClientIP`. Separately, the password can be sent in `?password=`, which ends up in access logs.
- [ ] **M9. A password-protected public link is briefly, or permanently, unprotected.** `CreatePublicLinkWithPassword` (`internal/share/share.go:143`) inserts the link, then sets the password in a second statement. If `SetPassword` fails, the error is returned but the open link remains.
- [ ] **M10. Registration has effectively no rate limit.** `HandleRegister` checks `IsLimited(ip)` but never records anything (`internal/web/login.go:323-331`), so open registration allows unlimited account creation. Login Flow `HandleInit` is unauthenticated and unlimited (`internal/compat/login.go:69`), so its in-memory pending map can be grown to exhaustion within each 10-minute window.
- [ ] **M11. JSON and OCS mutation APIs have no CSRF check.** `/api/files/*`, `/api/shares*` (`plugin/files/api.go:61-89`), OCS sharing, and `RegisterRoute` routes accept cookie sessions with only `SameSite=Lax` as protection. That does not stop same-site (sibling subdomain) attackers, or browsers that cache Basic-auth credentials. A text/plain cross-site POST to `/api/files/upload` with cached Basic credentials overwrites files. Require `X-CSRF-Token` or `OCS-APIRequest` for cookie-authenticated mutations, or refuse cookies on these routes.
- [ ] **M12. Plugins build their own DB paths, against the CLAUDE.md rule.** `plugin/feed/orchestrator/orchestrator.go:88`, `plugin/gallery/scanner.go:64` and `plugin/gallery/gallery.go:289,351` hand-build `…/app_data/<plugin>/plugin.db`. Calendar attachments, contact photos, profanity lists, recipe images and gallery thumbnails also hand-build per-user dirs keyed by username, so none of them follow a layout change.
- [ ] **M13. Previews ignore mounts and leak caches into user space.** `Preview`/`PreviewPDF` read `filepath.Join(UserFilesDir, path)` (`preview.go:168,178,336`), so files under any non-root mount get no preview, or a shadowed file's preview. `convertToPNG` writes its cache to `filepath.Dir(src)/../converted` (`:251`), which lands inside the user's own file tree for nested paths, shows up in listings and counts against quota. The thumbnail, converted and preview caches are never pruned, and each requested size (16–1024) is cached separately.
- [ ] **M14. Per-request mount resolution is expensive.** Every files operation calls `p.userFS(username)` → `Resolver.UserFS`, which re-reads the user, all mounts, each volume and share access, and builds new backends. `Upload` does that 2–3 times. `EnsureRootDir` runs on every API and WebDAV request (two `MkdirAll` calls, mount resolution and an INSERT). Quota checks look the user up twice and `SUM` the files table on every write. Cache the per-request FS and the root-dir check.
- [ ] **M15. Lockout DoS and limitations of the in-memory limiter.** Per-account keys let anyone lock any user out of `/login` and SSH with 10 bad attempts per 15 minutes. Behind a reverse proxy without `COG_TRUSTED_PROXIES`, every client shares one IP, so 10 failures lock out everyone. The limiter is per-process (ineffective with replicas), and its cleanup goroutine never stops.

## Low

- [ ] `HandleUserByID` and the provisioning endpoints return any user's email to any authenticated user (`internal/compat/ocs.go:342-373`).
- [ ] Basic auth with an app password ignores the username, so `alice:<bob's app password>` authenticates as bob (`middleware.go:124`). SSH checks the username; HTTP should too.
- [ ] `mfaEnforcement` exempts all of `/settings/`, including data export and import, volume and mount creation and app passwords (`internal/web/mfa_enforcement.go:26`). It also applies to DAV and OCS despite its comment, sending sync clients a 303 to an HTML page.
- [ ] The first-user-becomes-admin decision is a count-then-insert race (`user.go:79-87`), and there is no minimum password policy anywhere.
- [ ] TOTP secret encryption, reset-token hashing and CSRF all derive from `csrf_secret`, which is stored in the same `system_settings` table (`services.go:225`), so a DB leak defeats the at-rest protection. The secret is also handed to trusted plugins via an environment variable (`grpc.go:442`), where `/proc/<pid>/environ` exposes it.
- [ ] `Content-Disposition: attachment; filename="%s"` is unescaped in several places (`api.go:155,728,800`, `webdav.go:516`, `pages.go:541`). Use `mime.FormatMediaType`.
- [ ] Files search escapes `%` but not `_` or `\` (`files.go:1199`). A query containing `\` makes SQLite error out with a 500. Notes search does no escaping at all (`plugin/notes/store.go:196`).
- [ ] OCS `expireDate` is parsed as 00:00 UTC, so a share expires at the start of the chosen day instead of the end (`internal/compat/sharing.go:471`). Federated shares always declare `ResourceType: "file"`, even for directories.
- [ ] Any user can create `INSTANCE` shares (shared with everyone) and share with groups they don't belong to. Users can add arbitrary users to their own groups without consent.
- [ ] `group.Store.AddMember`/`RemoveMember` rebuild the whole closure table on every change (`internal/group/group.go:258`). `member_id` is not checked for existence.
- [ ] `Registry.SetServices` runs in `web.New`, after every plugin's `Register` (`routes.go:71` vs `services.go:273`), despite its own "must be called before Add()" contract, so services read during `Register` are nil. A plugin `Register` error is only logged, leaving half-mounted routes. `loadEnabledState` swallows query and scan errors (`registry.go:1160-1173`). `FireHook` iterates a map, so subscriber order is random.
- [ ] `email.Send` and `buildMultipartAlternative` ignore `CreatePart` errors (`email.go:116,125,167,172`). `SendWithAttachments` silently drops `htmlBody` (`internal/web/adapters.go:99`).
- [ ] `COG_LOG_REQUESTS` (which `go tool dev` enables) dumps full requests, including passwords and `Authorization`, and buffers every response body in memory (`cog_serve.go:84`).
- [ ] The initials avatar interpolates up to two characters of the name into SVG unescaped (`avatar.go` `serveInitials`). It cannot run script, but `<` or `&` breaks the image.
- [ ] Direct-download tokens are bound to a path, not a file ID (a rename breaks them), live only in memory, and are only evicted when a new token is created (`webdav.go:455`).
- [ ] Many handlers return `err.Error()` to clients (files API, OCS sharing, NC News/Notes/Bookmarks APIs), leaking internal paths and SQL errors.
- [ ] Login Flow is consent-phishable in the same way as Nextcloud: the grant page names the client from an attacker-controlled User-Agent, and the resulting app password has full, unscoped access. Consider showing the initiating IP and time, and scoped app passwords.
