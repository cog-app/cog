These tasks should be completed

# Cog Codebase Audit Report

The following is a CLAUDE generated report of other issues to fix. I need to go through this and verify before we implement.

---

2. MISSING FEATURES (Design vs Implementation)

Critical — Not Implemented

| Feature                              | Design Doc                              | Status                                                                 |
| ------------------------------------ | --------------------------------------- | ---------------------------------------------------------------------- |
| File access protocols (SMB, FTP, S3) | DESIGN/file_protocols.md                | No code. Only WebDAV exists.                                           |
| Plugin capability/permission system  | DESIGN/plugins/index.md §Access Control | DB_SANDBOX, DB_READ, FS_READ, ROUTE_GLOBAL_BIND — none implemented     |
| SQL Proxy / custom SQLite functions  | DESIGN/plugins/index.md §Data Access    | read_file(), head_file(), file_exists(), file_size() — not implemented |

Moderate — Partially Implemented

| Feature                                 | Design Doc                  | Gap                                                                                        |
| --------------------------------------- | --------------------------- | ------------------------------------------------------------------------------------------ |
| Versioning                              | DESIGN/compatibility_api.md | /remote.php/dav/versions/ — not implemented                                                |
| mem:// backend                          | DESIGN/storage.md           | Declared in URI scheme routing but returns error: "mem:// volumes do not support browsing" |
| Audit logging                           | DESIGN/file_protocols.md §4 | "All file modifications are logged centrally" — not implemented                            |
| Session invalidation on password change | Security best practice      | Existing sessions remain valid after password reset                                        |

Long term:

2. Plugin capability/permission system
