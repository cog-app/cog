# NextCloud Compatibility API

To ensure Cog works seamlessly with existing NextCloud mobile and desktop apps, it implements several legacy and standardized protocols.

## 1. Authentication & Discovery (OCS API)

NextCloud apps use the Open Collaboration Services (OCS) API for authentication, user settings, and capability discovery.

### Endpoints
- `GET /status.php`: Server status (installed, maintenance, version). **Implemented.**
- `GET /ocs/{v1,v2}.php/cloud/capabilities`: Returns the features Cog supports (e.g., WebDAV, sharing, notifications). **Implemented.**
- `GET /ocs/{v1,v2}.php/cloud/user`: Returns details about the logged-in user. **Implemented.**
- `GET /ocs/{v1,v2}.php/cloud/users/{id}`: Returns details about a user by ID or username. **Implemented.**
- `POST /index.php/login/v2`: Login Flow v2 init (App Passwords / OAuth-like flow). **Implemented.**
- `POST /index.php/login/v2/poll`: Login Flow v2 poll. **Implemented.**
- `GET|POST /index.php/login/v2/grant`: Login Flow v2 grant. **Implemented.**
- `GET /index.php/204`: Connectivity check (NC clients probe this). **Implemented.**

**Response Format:** OCS responses are wrapped in an XML or JSON `<ocs>` envelope as requested by the `format=json` query parameter.

## 2. File Access (WebDAV)

All file operations for desktop and mobile clients happen via WebDAV.

### Endpoints
- **Main Files:** `/remote.php/dav/files/{username}/` **Implemented.**
  - Maps to the user's files via the files plugin.
  - Supports `PROPFIND`, `GET`, `HEAD`, `PUT`, `DELETE`, `COPY`, `MOVE`, `MKCOL`, `PROPPATCH` (favorites), `REPORT` (filter-files/favorites), `LOCK`, `UNLOCK` (stubs).
- **Comments:** `/remote.php/dav/comments/files/{fileId}` — PROPFIND (list), POST (create), DELETE (remove). **Implemented.**
- **Direct Downloads:** `POST /ocs/{v1,v2}.php/apps/dav/api/v1/direct` — token-based download URL generation. **Implemented.**
- **Direct Serve:** `GET /remote.php/dav/direct/{token}` — unauthenticated one-time file download. **Implemented.**
- **Previews:** `GET /index.php/core/preview?fileId=N&x=W&y=H` — thumbnail generation. **Implemented.**
- **Avatars:** `GET /index.php/avatar/{userID}/{size}` — user avatar images. **Implemented.**
- **WebDAV Stub:** `/remote.php/dav` and `/remote.php/dav/` — presence check for HEAD/OPTIONS/GET/PROPFIND. **Implemented.**
- **Chunked Uploads:** `/remote.php/dav/uploads/{userId}/{transferId}/` — NC v2 chunked upload protocol (MKCOL, PUT chunks, MOVE to assemble). **Implemented.**
- **Trashbin:** `/remote.php/dav/trashbin/{userId}/trash/` — PROPFIND (list), DELETE (permanent delete), MOVE (restore). **Implemented.**
- **Versioning:** `/remote.php/dav/versions/{username}/` — **Not implemented.**

## 3. Sharing (OCS files_sharing API)

### Endpoints (all under `/ocs/{v1,v2}.php/apps/files_sharing/api/v1/`)
- `GET /shares` — List shares (supports `path` filter and `shared_with_me`). **Implemented.**
- `POST /shares` — Create share (user, group, public link with password/expiration). **Implemented.**
- `GET /shares/{shareId}` — Get share details. **Implemented.**
- `PUT /shares/{shareId}` — Update share (permissions, expiration, password). **Implemented.**
- `DELETE /shares/{shareId}` — Delete share. **Implemented.**
- `GET /sharees` — Search users/groups for share autocomplete. **Implemented.**
- `GET /sharees_recommended` — Recently shared-with targets. **Implemented.**

## 4. Activity & Notifications

### Endpoints
- `GET /ocs/{v1,v2}.php/apps/activity/api/v2/activity` — File comment activities. **Implemented.**
- `GET /ocs/{v1,v2}.php/apps/activity/api/v2/activity/filter` — Filtered activities (by object_type/object_id). **Implemented.**
- `GET /ocs/{v1,v2}.php/apps/notifications/api/v2/notifications` — Returns empty list (no notification system). **Stub.**
- `GET /ocs/{v1,v2}.php/apps/recommendations/api/v1/recommendations` — Returns empty list. **Stub.**

## 5. User Status

### Endpoints
- `GET /ocs/{v1,v2}.php/apps/user_status/api/v1/user_status` — Returns current user's status (always "online"). **Implemented.**
- `GET /ocs/{v1,v2}.php/apps/user_status/api/v1/statuses/predefined` — Returns predefined status list. **Implemented.**
- `POST /ocs/{v1,v2}.php/apps/user_status/api/v1/heartbeat` — Accepts and ignores. **Stub.**
- `PUT /ocs/{v1,v2}.php/apps/user_status/api/v1/user_status/status` — Accepts and ignores. **Stub.**
- `PUT /ocs/{v1,v2}.php/apps/user_status/api/v1/user_status/message/predefined` — Accepts and ignores. **Stub.**
- `PUT /ocs/{v1,v2}.php/apps/user_status/api/v1/user_status/message/custom` — Accepts and ignores. **Stub.**
- `DELETE /ocs/{v1,v2}.php/apps/user_status/api/v1/user_status/message` — Accepts and ignores. **Stub.**

## 6. Search

### Endpoints
- `GET /ocs/{v1,v2}.php/search/providers` — Returns files search provider. **Implemented.**
- `GET /ocs/{v1,v2}.php/search/providers/{providerId}/search` — Searches files by name. **Implemented.**

## 7. CalDAV/CardDAV Discovery

### Endpoints
- `GET|PROPFIND /.well-known/caldav` → redirects to `/remote.php/dav/`. **Implemented.**
- `GET|PROPFIND /.well-known/carddav` → redirects to `/remote.php/dav/`. **Implemented.**
- `PROPFIND /remote.php/dav/` — returns current-user-principal. **Implemented.**

CalDAV/CardDAV data endpoints (`/remote.php/dav/calendars/`, `/remote.php/dav/addressbooks/`, `/remote.php/dav/principals/`) are managed by privileged plugins (Calendar, Contacts).

## 8. CSRF & Security

For compatibility routes used by external apps (non-browser), CSRF checks are bypassed if a valid **App Password** or **Bearer Token** is provided in the `Authorization` header. Browser-based access to these routes still requires standard CSRF validation.

## 9. Shared Concepts

Compatibility routes follow canonical concepts/data shapes from `DESIGN/concepts_and_types.md`:
- Resources identified by stable `(resource_type, resource_id)`.
- Authorization resolved from global `shares` + group membership expansion.

---

## Android App Gap Analysis

The following gaps were identified by reviewing the NextCloud Android app's API usage against Cog's implemented endpoints. This analysis is based on the app's `RemoteOperation` classes and network layer.

### Resolved Gaps

The following gaps from the original analysis have been addressed:

| # | Gap | Resolution |
|---|-----|------------|
| 1 | Chunked file uploads | **Implemented.** NC v2 chunked upload protocol at `/remote.php/dav/uploads/`. |
| 2 | Trashbin / Deleted files | **Implemented.** `Delete` now soft-deletes to trash. WebDAV trashbin at `/remote.php/dav/trashbin/`. |
| 3 | Provisioning API path | **Implemented.** Route alias for `/ocs/{v1,v2}.php/apps/provisioning_api/api/v2/users/{id}`. |
| 4 | Predefined user statuses | **Implemented.** Returns standard predefined statuses list. |
| 5 | File locking | **Stub.** WebDAV LOCK/UNLOCK return valid responses. OCS lock endpoints accept requests. |
| 6 | Unified search | **Implemented.** Search providers endpoint returns files provider; query endpoint searches files by name. |
| 7 | Tags API | **Stub.** Returns empty list. |
| 8 | User status heartbeat | **Stub.** Accepts and ignores. |
| 9 | Client diagnostics | **Stub.** Accepts and ignores. |
| 10 | Hover cards / profiles | **Implemented.** Returns user profile data. |

### Remaining Gaps

| # | Gap | Description | Priority |
|---|-----|-------------|----------|
| 1 | **File versions** | `/remote.php/dav/files/{userId}/.file/{fileId}` — version history. High effort. | Medium |
| 2 | **RichDocuments / Direct Editing** | Document editing (Collabora/OnlyOffice). NC app-specific. | Low |
| 3 | **End-to-End Encryption** | E2EE metadata and lock endpoints. Niche feature. | Low |
| 4 | **Remote wipe** | Admin notification endpoint for remote device wipe. | Low |

### Key Implementation Files

- `internal/compat/ocs.go` — OCS capabilities, user info, status, XML/JSON envelope
- `internal/compat/stubs.go` — Stub endpoints (notifications, user status, search, avatars, tags, profiles, locking, diagnostics)
- `internal/compat/webdav.go` — WebDAV operations (PROPFIND/GET/PUT/DELETE/MOVE/COPY/PROPPATCH/REPORT/LOCK/UNLOCK), direct downloads, previews
- `internal/compat/chunked.go` — NC v2 chunked upload protocol
- `internal/compat/trashbin.go` — Trashbin WebDAV handler (list, restore, permanent delete)
- `internal/compat/sharing.go` — OCS sharing API (CRUD, sharees, recommendations)
- `internal/compat/comments.go` — WebDAV comments (PROPFIND/POST/DELETE)
- `plugin/files/trash.go` — Trash operations (soft delete, list, restore, permanent delete)
- `plugin/files/sql/migration002_trash.sql` — Trash table migration
- `internal/web/routes.go` — Route registration

### Verification

- Test with Android app: install NC Android app, point at Cog server, verify login + file browsing + upload + sharing
- Check server logs for 404/405 responses to identify any additional missing endpoints
