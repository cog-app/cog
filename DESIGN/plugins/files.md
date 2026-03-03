# Files Plugin Design

The Files plugin (`id: files`) is the core storage plugin and the authoritative metadata layer for user files/directories.

## 1. Role and Loading Model

- **Plugin Type:** Core privileged internal plugin.
- **Loading Model:** In-process only; not gRPC-loadable.
- **Responsibilities:**
  - Maintain file/directory metadata in `app_data/files/plugin.db`.
  - Expose user home storage via VFS-backed operations.
  - Provide resource identity for file sharing and search indexing.

## 2. Data Model

- **Files Table:** Includes files and directories.
  - Key fields: `id`, `parent_path`, `path`, `name`, `is_dir`, `size`, `mtime`, `etag`, `mime_type`.
- **Stable Identity Rule:**
  - `id` is canonical for resource identity.
  - `path` is mutable metadata and can change on rename/move.
- **Resource Types:**
  - `files:file`
  - `files:directory`

## 3. File Operations and Semantics

- **Create/Upload:** write stream to backend, then commit metadata row.
- **Read/Download:** stream from backend through protocol handlers.
- **Rename/Move:** update path metadata while preserving stable `id`.
- **Delete:** mark/remove metadata and remove backend object.
- **Directory Listing:** return merged VFS + metadata state with lazy reconciliation.

## 4. API and Routing

- **UI:** `/files/*` (host-level route backed by files plugin handlers).
- **Plugin Routes:** Optional support endpoints under `/apps/files/*`.
- **Protocol Backends:** WebDAV/SFTP/SMB/FTP/S3 map to files plugin VFS operations.

## 5. Sharing and Permissions

- Files plugin registers resources for global sharing.
- Share checks resolve through global `shares` and group expansion.
- Directory shares imply child access via hierarchy rules.
- Public links use token-backed share records with optional expiry/password hash.

## 6. Search Indexing

- Registers `SearchProvider` for file entries.
- Suggested mapping:
  - `name`: filename
  - `subtext`: parent path + size/type
  - Searchable fields: `name`, optional extracted content, tags/metadata
- Search behavior is eventual-consistent with async Collector updates.

## 7. Performance and Maintenance

- Use streaming I/O for all large file operations.
- Keep directory listing fast with indexed metadata (`path`, `parent_path`, `name`).
- Reconciliation should be bounded; avoid full-tree scans on hot paths.
- Maintain background tasks for stale metadata cleanup and cache refresh.

## 8. Security

- Enforce canonical path normalization before backend access.
- Prevent path traversal and cross-user boundary escapes.
- Validate permission checks before backend read/write/delete.
- Audit all mutations with actor, source protocol, and timestamp.

