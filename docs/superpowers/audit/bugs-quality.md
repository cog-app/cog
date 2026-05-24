# Bugs & Code Quality

Non-security bugs and code-quality issues. Security findings are in `security.md`; plugin-system
findings in `plugin-system.md`.

## Concurrency / Races / Goroutine Leaks

- ~~**`MaxOpenConns=1` never set**~~ — **RETRACTED**. WAL mode + `_busy_timeout=5000` (set in
  `internal/db/hook.go:46`) is the right design. `SetMaxOpenConns(1)` would deadlock on nested
  queries during `rows.Next()` iteration and serialize reads unnecessarily. CLAUDE.md line is
  stale.

- **`search.Manager.UserDBHook` re-enters SQLite from update hook** — `internal/search/manager.go:92-114`.
  Update hook is synchronous inside the SQLite write path. Lazy path calls
  `collector.FullSync()` (line 240), issuing `SELECT rowid FROM <table>` against the same DB.
  Likely deadlock or undefined behavior.

- **`search.Manager.ensureUserIndex` holds `m.mu` across blocking work** — `internal/search/manager.go:204-247`.
  Concurrent update hooks on other user DBs block on `m.mu` (line 94) for the duration of FullSync.

- **`search.Propagator` send-after-close panic** — `internal/search/propagator.go:59-89`. `Stop()`
  closes `p.ch`. `OnShareCreate/Delete` use `select { case p.ch <- ev: default: ... }`. The
  `default` only avoids blocking — sending to a closed channel panics regardless. Need stopped flag.

- **`search.Collector.Send` send-after-close panic** — `internal/search/collector.go:55-72`. Same
  pattern.

- **`search.Collector.run` timer not drained after `Stop`** — `internal/search/collector.go:80-125`.

- **`search.Manager.Stop` doesn't wait for collector goroutines before closing the index they
  write to** — `internal/search/manager.go:148-167`.

- **`compat.ChunkedUploadHandler` in-memory only** — `internal/compat/chunked.go:33-117`. Sessions
  reaped only when a new MKCOL arrives. On restart, in-memory map is gone but on-disk
  `cog-chunk-*` temp dirs leak forever. No background reaper.

- **`compat.LoginFlowHandler.Cleanup()` never called** — `internal/compat/login.go:240-248`.
  `pending` grows unbounded.

- **`federation.Store.NCIDFor` SELECT-then-INSERT race** — `internal/federation/store.go:232-243`.
  Two concurrent callers can both miss and both insert.

- **`volume.Store.Delete` mount-count TOCTOU** — `internal/volume/volume.go:264-283`. SELECT COUNT
  + DELETE without transaction.

- **`federation.OCSHandler` goroutine leak** — `internal/federation/ocs_handler.go:151,187,223`.
  `go h.Client.Notify(context.Background(), …)` with no timeout. Returned error dropped.

- **`seq.Peek` iterator goroutine leak** — `internal/seq/seq.go:27-59`. Caller returning early
  never invokes `stop()`.

- **`plugin/files` ops not transactional** — `plugin/files/trash.go:29-84` `Trash` does Rename →
  INSERT trash → DELETE files in three separate calls. `HardDelete` at `plugin/files/files.go:422-471`
  same shape. `Rename` (line 474-531) updates parent then bulk-updates children in two statements.
  Wrap in `sql.Tx`.

## Error Handling (CLAUDE.md forbids swallowing)

- `internal/services/services.go:318-322` — `db.Exec(INSERT INTO system_settings …)` and
  `rand.Read(secret)` both ignored when persisting CSRF secret.
- `internal/services/services.go:115-127,130-138` — iterator errors break loop silently in
  `ResolveRecipientUsernames`.
- `internal/web/routes.go:384-389` — `Groups.ListGroupsForUser` iterator errors break silently;
  `total = -1`.
- `internal/compat/sharing.go:573-574` — `Shares.Get`/`Users.GetByID` errors dropped after Update;
  can pass nil pointers to `shareToOCS`.
- `internal/compat/sharing.go:450-451` — federation rollback errors ignored.
- `internal/compat/webdav.go:418` — `rand.Read(b)` error ignored for direct-download tokens.
- `internal/compat/webdav.go:164,173,233,564,575,618,627,638,650` — `io.WriteString` /
  `fmt.Fprintf` / `enc.Encode` / `tmpl.Execute` errors dropped.
- `internal/compat/login.go:233` — `tmpl.Execute(w, …)` error dropped. Template re-parsed every
  render (perf).
- `internal/compat/sharing.go:642,677,770,818` — `range users.List()` / `Shares.ListByOwner`
  iterators break silently.
- `internal/quota/quota.go:115-153` — `EffectiveQuota` swallows non-`ErrNoRows` Scan errors → real
  DB error returns "unlimited" silently. `rand.Read(b)` ignored in `generateID` line 177.
- `internal/settings/settings.go:72-75` — `GetBool` discards `Get` error.
- `internal/federation/store.go:156,184` — neither iterator checks `rows.Err()`.
- `internal/federation/store.go:196` — `RowsAffected()` error swallowed.
- `internal/group/group.go:213-214,219-220` — `tx.Query`/`rows.Scan` errors swallowed in
  `isGroupDescendant`, returns false on error.
- `plugin/files/trash.go:67` — `os.Rename` rollback after DB insert failure ignored.
- `plugin/files/trash.go:206,208,226` — `os.RemoveAll`/`os.Remove` results ignored.
- `plugin/files/files.go:137-142` — widget renderer swallows DB error and `RecentFiles` error.
- `plugin/files/files.go:236,314` — `LastInsertId()` error ignored.
- `internal/compat/webdav.go:194` — `h.Files.GetByPath(...)` error swallowed.
- `internal/compat/chunked.go:104-107` — `os.RemoveAll(s.Dir)` in stale-session GC ignored.

## Resource Leaks

- `internal/compat/login.go:217-237` — `template.Must(template.New(...).Parse(...))` re-parsed
  every grant page render. Move to package var.
- `internal/compat/chunked.go:198-204` — if `EnsureRootDir`/`Upload` errors, chunk dir is NOT
  removed and the session stays in the map. Leak.
- `internal/web/routes.go:204-277` — `directTokens` and `sessions` maps live forever, no GC
  goroutine.

## HTTP Status / Response Writing

- `internal/compat/webdav.go:552` — `SetFavorite` errors always return 404, even on DB errors.
- `internal/compat/webdav.go:230,264,289` — `Upload`/`CreateDir`/`Rename` errors always returned
  500/409, even for permission/quota errors.
- `internal/compat/webdav.go:289-292` — `Rename` returns 409 on every error including not-found.
- `internal/compat/chunked.go:233` — `Files.Upload` error always 500 even for quota.
- `internal/compat/ocs.go:67-92` — `writeOCSJSON` always sets `Meta.Status="ok"`, `Message="OK"`
  regardless of status code.
- `internal/compat/login.go:212-214` — `processGrant` POST writes HTML response directly instead
  of redirecting. PRG violation per CLAUDE.md → browser refresh resubmits credentials.

## Path Traversal (See also security.md)

- `internal/federation/webdav_token.go:103` — `strings.HasPrefix(localPath, basePath)` lacks
  trailing-sep guard. `basePath="/data/u/files/foo"` allows `/data/u/files/foobar`.
- `internal/volume/backend_file.go:40` — same flaw.
- `internal/federation/webdav_token.go:194-205` — XML response writes raw href/filename/modtime
  unescaped. Filenames with `<`, `&`, `"` produce malformed XML.
- `internal/federation/webdav_token.go:199` — `strconv.Itoa(int(info.Size()))` truncates on 32-bit
  or for files > 2 GiB.
- `internal/federation/webdav_token.go:208-241` — `handlePut`/`handleMkcol`/`handleDelete` bypass
  files plugin entirely (see security.md Critical #5).

## SQL Patterns / N+1

- `internal/quota/quota.go:124-141` — `EffectiveQuota` issues one query per group. Single
  `WHERE volume_id = ? AND subject_type='GROUP' AND subject_id IN (…)` would do.
- `internal/mount/resolver.go:75-110` — `UserFS` iterates mounts, calling `volumes.GetByID` +
  `volume.NewBackend` per mount, every request. Backends should cache.
- `internal/web/routes.go:384-389`, `internal/compat/sharing.go:640-699,769-839` — autocomplete
  iterates all users/groups; no SQL `LIKE` push-down. Acceptable until directory grows.
- `plugin/files/files.go:91` — `EnrichSQL` declared `WHERE rowid IN (?)` with single placeholder.
  sqlite3 driver does not expand slices into `?` natively; either expansion happens elsewhere or
  this is broken for n > 1.
- `plugin/files/files.go:460`, `plugin/files/trash.go:73` — `path LIKE filePath+"/%"` doesn't
  escape `%` or `_`. Folder named `a_b` matches `aXb`.

## TOCTOU between FS and DB

- `plugin/files/files.go:249-326` — `Upload` writes to disk first, then DB upsert. DB insert
  failure → orphan file on disk. Write-to-temp + insert + rename instead.
- `plugin/files/files.go:256-260` — quota pre-check only triggers if body is exactly
  `*io.LimitedReader`. API entry at `plugin/files/api.go:95` passes `r.Body` directly → quota
  never pre-checked. Post-write `counter.n` is never compared against quota either.
- `plugin/files/trash.go:138-190` — `RestoreFromTrash` doesn't restore child rows for
  directories. Restored folder loses children's IDs; lazy reconcile assigns new IDs, breaking
  shares pointing at them.
- `plugin/files/trash.go:29-84` — Mirror: `Trash` deletes child rows from `files` (line 73)
  without copying to `trash`. Same data-loss on restore.
- `plugin/files/trash.go:155-161` — `RestoreFromTrash`: `os.Rename` silently overwrites existing
  file at destination on Unix.

## Context Misuse

- `internal/compat/sharing.go:446` — `h.FedClient.SendShare(context.Background(), …)` inside an
  HTTP handler. Use `r.Context()`.
- `internal/federation/ocs_handler.go:151,187,223` — `go h.Client.Notify(context.Background(), …)`.
  No timeout; hanging remote leaks goroutines.
- `plugin/files/preview.go:254,338` — `context.WithTimeout(context.Background(), 60*time.Second)`
  instead of deriving from `r.Context()`. Client cancel doesn't abort.
- `internal/volume/backend_webdav.go:94,160,204,220,240,256,284` — `http.NewRequest` instead of
  `http.NewRequestWithContext`. No cancellation/timeout propagation.

## Encoding / Off-by-One

- `internal/compat/chunked.go:192-195` — chunks sorted lexicographically. NC v2 clients zero-pad,
  so works, but silently corrupts a non-padded client.
- `internal/compat/chunked.go:244` — `strings.Repeat("0", max(0, 8-len(meta.ETag)))+meta.ETag`
  constructs OC-FileId from ETag; the meta has a real integer `meta.ID` used by the WebDAV
  handler (`fmt.Sprintf("%08d", meta.ID)`). Inconsistency.
- `internal/volume/backend_webdav.go:27-31` — `url()` doesn't escape path segments. Spaces, `?`,
  `#`, `%` produce malformed URLs.
- `internal/compat/ocs.go:166-172` — `omitempty` tag detection: `tag[comma:] == ",omitempty"`
  fails on multi-option tags. Use `strings.Contains(tag[comma:], ",omitempty")`.
- `internal/compat/ocs.go:210-217` — `indexOf` is a hand-rolled `strings.IndexByte`. Dead code.

## Misc

- `vfs/overlay.go:144` — `io.NopCloser(strings.NewReader(string(data)))`: useless `[]byte → string
  → strings.Reader` copy. Use `bytes.NewReader(data)`.
- `internal/compat/sharing.go:154-155` — `CanEdit`/`CanDelete` hardcoded `true` regardless of bits.
- `internal/compat/webdav.go:670-673` — `permissions := "RGDNVCK"` then `if meta.IsDir { permissions = "RGDNVCK" }`
  is dead code (same value either way).
- `internal/compat/webdav.go:632-651` — LOCK handler returns stub token without tracking;
  UNLOCK returns 204 without validation. Clients relying on locking silently broken.
- `internal/services/services.go:308-322` — `loadOrCreateSecret` ignores `rand.Read` and
  `db.Exec`. Restart-with-malformed-row silently invalidates all CSRF tokens.
- `internal/web/routes.go:50-52` — `if plug, ok := p.Plugins.Plugin("files"); ok { fp, _ = plug.(*filesPlugin.Plugin) }`
  — assertion failure silently leaves `fp=nil`. No log.
- `plugin/files/files.go:689-696` — `generateETag` falls back to timestamp on rand failure. Not
  random; two simultaneous writes could collide.

## Highest-impact

1. Update-hook → lazy index open → blocking FullSync can deadlock the per-user DB.
2. Send-after-close panic in `search.Collector` and `search.Propagator`.
3. No transactions in Trash/Rename/Delete; crash leaves on-disk and metadata state inconsistent.
4. Path-traversal check is broken in two places (federation, volume file backend).
5. Quota pre-check never fires because uploads don't wrap body in `*io.LimitedReader`.
6. Federation WebDAV writes bypass files plugin entirely (quota, indexing, DB drift).
7. LoginFlow and chunked-upload temp dirs leak unbounded — no reaper.
