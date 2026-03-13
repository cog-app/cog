These tasks should be completed

- ~~Files plugin should include a list of volumes and allow browsing them directly (in addition to the VFS it does by default)~~ DONE
- ~~Shell~~ DONE
  - ~~shell subcommand should behave like an ssh session with the vfs mounted at /files~~

# Cog Codebase Audit Report

The following is a CLAUDE generated report of other issues to fix. I need to go through this and verify before we implement.

1. BUGS (Fix Now)

~~B8: Missing CSRF on unauthenticated invite registration~~ DONE — invite token now submitted as hidden form field and verified on POST

---

2. MISSING FEATURES (Design vs Implementation)

Critical — Not Implemented

| Feature                                    | Design Doc                              | Status                                                                 |
| ------------------------------------------ | --------------------------------------- | ---------------------------------------------------------------------- |
| File access protocols (SFTP, SMB, FTP, S3) | DESIGN/file_protocols.md                | No code. Only WebDAV exists.                                           |
| Plugin capability/permission system        | DESIGN/plugins/index.md §Access Control | DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND — none implemented     |
| SQL Proxy / custom SQLite functions        | DESIGN/plugins/index.md §Data Access    | read_file(), head_file(), file_exists(), file_size() — not implemented |

Moderate — Partially Implemented

| Feature                                       | Design Doc                  | Gap                                                                                        |
| --------------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------ |
| ~~Resource registration for Gallery, Notes, RSS~~ | Plugin design docs          | ~~DONE — Gallery, Notes, RSS now register resources~~                     |
| Versioning                                    | DESIGN/compatibility_api.md | /remote.php/dav/versions/ — not implemented                                                |
| mem:// backend                                | DESIGN/storage.md           | Declared in URI scheme routing but returns error: "mem:// volumes do not support browsing" |
| ~~Rate limiting / account lockout~~               | Security best practice      | ~~DONE — Rate limiting on login, register, Basic Auth, Bearer, and Login Flow v2~~                                                |
| Audit logging                                 | DESIGN/file_protocols.md §4 | "All file modifications are logged centrally" — not implemented                            |
| Session invalidation on password change       | Security best practice      | Existing sessions remain valid after password reset                                        |

---

6. PRIORITY RECOMMENDATIONS

Short term (features):

1. ~~Add resource registration to Gallery, Notes, RSS plugins (enables sharing)~~ DONE
2. ~~Add rate limiting to auth endpoints~~ DONE

Long term:

2. Plugin capability/permission system
