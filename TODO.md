These tasks should be completed

- Files plugin should include a list of volumes and allow browsing them directly (in addition to the VFS it does by default)

# Cog Codebase Audit Report

The following is a CLAUDE generated report of other issues to fix. I need to go through this and verify before we implement.

1. BUGS (Fix Now)

B8: Missing CSRF on unauthenticated invite registration — **LOW RISK**: the invite token itself is an unguessable secret that provides CSRF-equivalent protection

---

2. MISSING FEATURES (Design vs Implementation)

Critical — Not Implemented

┌────────────────────────────────────────────┬─────────────────────────────────────────┬───────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ Feature │ Design Doc │ Status │
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

┌───────────────────────────────────────────────┬─────────────────────────────┬──────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ Feature │ Design Doc │ Gap │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Resource registration for Gallery, Notes, RSS │ Plugin design docs │ These 3 plugins don't register resources — sharing won't work for them │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Versioning │ DESIGN/compatibility_api.md │ /remote.php/dav/versions/ — not implemented │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ mem:// backend │ DESIGN/storage.md │ Declared in URI scheme routing but returns error: "mem:// volumes do not support browsing" │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Rate limiting / account lockout │ Security best practice │ No rate limiting on login or auth endpoints │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ App password management UI │ DESIGN/frontend.md │ Can create but can't list or revoke app passwords │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Audit logging │ DESIGN/file_protocols.md §4 │ "All file modifications are logged centrally" — not implemented │
├───────────────────────────────────────────────┼─────────────────────────────┼──────────────────────────────────────────────────────────────────────────────────────────────────────┤
│ Session invalidation on password change │ Security best practice │ Existing sessions remain valid after password reset │
└───────────────────────────────────────────────┴─────────────────────────────┴──────────────────────────────────────────────────────────────────────────────────────────────────────┘

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
- NextCloud compat — WebDAV, CalDAV, CardDAV, Login Flow v2, News API, Memories API, OCS capabilities, chunked uploads, trashbin
- Frontend — HTMX+templ stack, PRG pattern with flash cookies, per-plugin CSRF, component library
- 7 of 9 planned plugins implemented with DB schemas, handlers, search providers, and widgets

---

6. PRIORITY RECOMMENDATIONS

Short term (features):

1. Add resource registration to Gallery, Notes, RSS plugins (enables sharing)
2. Add rate limiting to auth endpoints
3. Add app password revocation UI

Long term:

1. File access protocols (SFTP first — most requested)
2. Plugin capability/permission system
3. Shared resource search propagation
4. Group nesting (or remove from design docs)
5. ~~Trashbin~~ DONE and versioning
