# NextCloud Compatibility API

To ensure Cog works seamlessly with existing NextCloud mobile and desktop apps, it implements several legacy and standardized protocols.

## 1. Authentication & Discovery (OCS API)

NextCloud apps use the Open Collaboration Services (OCS) API for authentication, user settings, and capability discovery.

### Endpoints
- `GET /ocs/v1.php/cloud/capabilities`: Returns the features Cog supports (e.g., WebDAV, sharing, notifications).
- `GET /ocs/v1.php/cloud/user`: Returns details about the logged-in user.
- `POST /ocs/v1.php/cloud/login`: Handles login requests for mobile apps.
- `POST /index.php/login/v2`: Login Flow v2 (App Passwords / OAuth-like flow).

**Response Format:** OCS responses must be wrapped in an XML or JSON `<ocs>` envelope as requested by the `format=json` query parameter.

## 2. File Access (WebDAV)

All file operations for desktop and mobile clients happen via WebDAV.

### Endpoints
- **Main Files:** `remote.php/dav/files/{username}/`
  - Maps to the user's `files/` directory in the storage backend.
  - Supports `PROPFIND`, `PUT`, `GET`, `DELETE`, `COPY`, `MOVE`, `MKCOL`.
- **Trashbin:** `remote.php/dav/trashbin/{username}/` (Stub or implemented via a hidden folder).
- **Versioning:** `remote.php/dav/versions/{username}/`.

### Optimization: Chunking
Cog supports the NextCloud-specific `OC-Chunked` header for reliable uploads of large files over unstable connections.

## 3. Calendars & Contacts (CalDAV/CardDAV)

These are managed by the **Calendar** and **Contacts** plugins, which bind routes globally to maintain compatibility.

### Endpoints
- **Discovery:** `/.well-known/caldav` and `/.well-known/carddav`
  - These must redirect to the correct service endpoints.
- **CalDAV:** `remote.php/dav/calendars/{username}/`
  - **Aggregation:** This endpoint is managed by the Host. It queries all registered `CalDAVProvider`s from all active plugins (Calendar, Tasks, Deck, etc.) and aggregates their collections into a single DAV namespace.
- **CardDAV:** `remote.php/dav/addressbooks/{username}/`
- **Principal:** `remote.php/dav/principals/users/{username}/`

## 4. News / RSS (NextCloud News API)

The News plugin implements the specialized API used by NextCloud News clients (like the Android "News" app).

### Endpoints
- `GET /index.php/apps/news/api/v1-2/feeds`: List all feeds.
- `GET /index.php/apps/news/api/v1-2/items`: List unread/starred items.
- `POST /index.php/apps/news/api/v1-2/items/read`: Mark items as read.
- `GET /index.php/apps/news/api/v1-2/folders`: Retrieve folder structure for feeds.

## 5. Implementation Strategy: Global Route Binding

While standard plugins are restricted to `/apps/{id}/`, compatibility plugins (Calendar, Contacts, News) require **Global Route Permissions**.

### Permission: `ROUTE_GLOBAL_BIND`
- **Requirement:** Can only be granted to plugins installed by a System Administrator.
- **Mechanism:** Allows the plugin to use `http.ServeMux` patterns that overlap with `remote.php/` or `index.php/`.
- **Conflict Resolution:** If multiple plugins attempt to bind the same global route, the Host prioritizes built-in/system plugins and logs a warning for others.

```go
// Example: Calendar Plugin Registration
registrar.RegisterGlobalRoute("GET /remote.php/dav/calendars/{user}/", davHandler)
```

## 6. CSRF & Security
For compatibility routes used by external apps (non-browser), CSRF checks are bypassed if a valid **App Password** or **Bearer Token** is provided in the `Authorization` header. Browser-based access to these routes still requires standard CSRF validation.
