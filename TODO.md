Cog Codebase Audit Report

1. BUGS (Fix Now)

B1: ListBySubject doesn't filter expired shares

internal/share/share.go:246 — Returns all shares including expired ones. CheckAccess (line 332) correctly filters on s.expiration IS NULL OR s.expiration > ?, but ListBySubject does not. UIs showing "shared with me" will display expired shares.

B2: Direct download tokens leak memory

internal/compat/webdav.go — directTokens map is in-memory with no cleanup goroutine. Tokens are never expired or removed, so the map grows unboundedly on a busy server.

B3: Unchecked errors return 200 OK

- internal/web/groups.go:286 — RemoveMember error silently discarded
- internal/web/groups.go:370 — Invitations.Delete error silently discarded
- internal/web/groups.go:523 — Mounts.DeleteGroupMount error silently discarded

B4: RebuildClosure not atomic with member changes

internal/group/group.go:131-169 — AddMember, RemoveMember, UpdateMemberType, and Delete all call RebuildClosure() as a separate statement. If the initial write succeeds but closure rebuild fails, the table goes stale. Should use a transaction.

B5: Login Flow v2 grant form has hardcoded credentials

internal/compat/login.go — The grant template has value="admin" hardcoded in username/password fields. Dev artifact left in production code.

B6: CreateGroup doesn't set owner_id

internal/web/groups.go:211 — Passes nil for ownerID, then separately adds creator as OWNER member. The groups.owner_id column stays NULL, inconsistent with membership.

B7: String literal instead of constant

internal/web/groups.go:219 — Uses "OWNER" instead of group.Owner constant.

B8: Missing CSRF on unauthenticated invite registration

internal/web/groups.go:434-482 — The unauthenticated POST /invite/{token} registration flow has no CSRF protection. The form in groups.templ:324 doesn't include a csrf_token field.

B9: BasicAuth timing inconsistency

internal/auth/middleware.go:62-73 — After successful user password auth, the code falls through to also try AppPasswords.Validate(). Should return early on success to avoid leaking timing info.

---

2. MISSING FEATURES (Design vs Implementation)

Critical — Not Implemented

┌────────────────────────────────────────────┬─────────────────────────────────────────┬───────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ Feature │ Design Doc │ Status │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Tasks plugin │ DESIGN/plugins/tasks.md │ No plugin/tasks/ directory. Entirely missing. │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Bookmarks plugin │ DESIGN/plugins/bookmarks.md │ No plugin/bookmarks/ directory. Entirely missing. │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ File access protocols (SFTP, SMB, FTP, S3) │ DESIGN/file_protocols.md │ No code. Only WebDAV exists. │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Plugin capability/permission system │ DESIGN/plugins/index.md §Access Control │ DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND — none implemented │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ SQL Proxy / custom SQLite functions │ DESIGN/plugins/index.md §Data Access │ read_file(), head_file(), file_exists(), file_size() — not implemented │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Shared resource search propagation │ DESIGN/search.md §4 │ No SearchPropagationJob. Search only indexes owner's data. │
├────────────────────────────────────────────┼─────────────────────────────────────────┼───────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Group nesting │ DESIGN/sharing.md │ group_members.member_type only allows USER/OWNER (no GROUP). RebuildClosure() is flat (depth=0 only). │
└────────────────────────────────────────────┴─────────────────────────────────────────┴───────────────────────────────────────────────────────────────────────────────────────────────────────┘

Moderate — Partially Implemented

┌───────────────────────────────────────────────┬─────────────────────────────┬────────────────────────────────────────────────────────────────────────────────────────────┐
│ Feature │ Design Doc │ Gap │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Resource registration for Gallery, Notes, RSS │ Plugin design docs │ These 3 plugins don't register resources — sharing won't work for them │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ OC-Chunked uploads │ DESIGN/compatibility_api.md │ Not implemented (bigfilechunking: false) │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Trashbin │ DESIGN/compatibility_api.md │ /remote.php/dav/trashbin/ — not implemented │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Versioning │ DESIGN/compatibility_api.md │ /remote.php/dav/versions/ — not implemented │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ OCS XML format │ DESIGN/compatibility_api.md │ Only JSON returned, no XML support │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ mem:// backend │ DESIGN/storage.md │ Declared in URI scheme routing but returns error: "mem:// volumes do not support browsing" │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Rate limiting / account lockout │ Security best practice │ No rate limiting on login or auth endpoints │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ App password management UI │ DESIGN/frontend.md │ Can create but can't list or revoke app passwords │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Audit logging │ DESIGN/file_protocols.md §4 │ "All file modifications are logged centrally" — not implemented │
├───────────────────────────────────────────────┼─────────────────────────────┼────────────────────────────────────────────────────────────────────────────────────────────┤
│ Session invalidation on password change │ Security best practice │ Existing sessions remain valid after password reset │
└───────────────────────────────────────────────┴─────────────────────────────┴────────────────────────────────────────────────────────────────────────────────────────────┘

---

4. TEST COVERAGE

Only 5 of ~30 packages have tests:

┌─────────────────┬──────────────────────────────────────────────────────────────┐
│ Has Tests │ No Tests │
├─────────────────┼──────────────────────────────────────────────────────────────┤
│ internal/search │ internal/share — CheckAccess, expiration, passwords untested │
├─────────────────┼──────────────────────────────────────────────────────────────┤
│ internal/web │ internal/group — RebuildClosure, member ops untested │
├─────────────────┼──────────────────────────────────────────────────────────────┤
│ plugin │ internal/auth — auth chain, CSRF untested │
├─────────────────┼──────────────────────────────────────────────────────────────┤
│ plugin/calendar │ internal/volume — volume/mount ops untested │
├─────────────────┼──────────────────────────────────────────────────────────────┤
│ plugin/contacts │ plugin/files, plugin/rss, plugin/gallery, plugin/notes │
└─────────────────┴──────────────────────────────────────────────────────────────┘

---

5. WHAT'S WORKING WELL

- Sharing system — Full implementation: CheckAccess with group expansion, hierarchy walk, public links, password protection, expiration
- Search system — Bleve-based with sqlite3 update hooks, collector pipeline, batching, deduplication, enrichment queries
- Plugin system — Clean lifecycle (Register/Start/Stop), all 8 Registrar methods, enable/disable, Protected detection
- NextCloud compat — WebDAV, CalDAV, CardDAV, Login Flow v2, News API, Memories API, OCS capabilities
- Frontend — HTMX+templ stack, PRG pattern with flash cookies, per-plugin CSRF, component library
- 7 of 9 planned plugins implemented with DB schemas, handlers, search providers, and widgets

---

6. PRIORITY RECOMMENDATIONS

Immediate (bugs):

1. Fix ListBySubject to filter expired shares (B1)
2. Add TTL/cleanup to direct download tokens (B2)
3. Check errors on DELETE handlers (B3)
4. Wrap RebuildClosure in transactions (B4)
5. Remove hardcoded credentials from login flow (B5)

Short term (features):

1. Add resource registration to Gallery, Notes, RSS plugins (enables sharing)
2. Implement Tasks plugin (needed for Calendar VTODO/CalDAV integration)
3. Implement Bookmarks plugin (design is complete)
4. Add rate limiting to auth endpoints
5. Add app password revocation UI

Long term:

1. File access protocols (SFTP first — most requested)
2. Plugin capability/permission system
3. Shared resource search propagation
4. Group nesting (or remove from design docs)
5. Trashbin and versioning
