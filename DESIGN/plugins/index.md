# Plugin Architecture

## Plugin Index

- [`files`](./files.md): Core privileged storage plugin.
- [`calendar`](./calendar.md): CalDAV calendar plugin.
- [`tasks`](./tasks.md): Task/VTODO plugin with CalDAV provider integration.
- [`contacts`](./contacts.md): CardDAV contacts plugin.
- [`rss`](./rss.md): News/RSS ingestion and compatibility plugin.
- [`notes`](./notes.md): Notes and tagging plugin.

## Overview

Cog's plugin system is designed for flexibility and performance. Plugins can be:

- Compiled directly into the binary (core/1st-party); these may use privileged hooks into the application.
- Run as separate processes communicating via gRPC (external/3rd-party).

## Data Access & Queries

External plugins may run in separate processes (gRPC), so they cannot always open SQLite files directly. Cog provides controlled data access paths:

### 1. SQL Proxy & Data Access

For plugins with `DB_READ:<plugin_id>` or `DB_WRITE:<plugin_id>` permissions, the Host automatically **attaches** the requested databases to every connection for that plugin.

- **Isolation:** Plugins cannot execute `ATTACH DATABASE` themselves; it is managed by the Host based on permissions.
- **FS Functions:** For bounded metadata/content extraction in SQL, the Host exposes custom SQLite functions:
  - `read_file(path)`: Returns the full content of a file.
  - `head_file(path, bytes)`: Returns the first N bytes of a file.
  - `file_exists(path)`: Returns 1 if the file exists, 0 otherwise.
  - `file_size(path)`: Returns the size of the file in bytes.
- **Semantics:** These functions use `fs.FS` paths and respect the user's storage backend (Local, S3, etc.).
- **Writes:** File writes should go through explicit host/plugin APIs, not SQL scalar functions.

### 2. Routing & CSRF

Plugins register routes via the `PluginRegistrar`. Cog handles the underlying routing and security (CSRF) based on the registration type.

- **APIs (`/api/{plugin_id}/{path}`):**
  - **Purpose:** Data exchange (JSON), mobile apps, external integrations.
  - **CSRF:** Bypassed (uses Bearer tokens or App Passwords).
  - **Mounting:** Default is `/api/{plugin_id}/`. Plugins with `ROUTE_GLOBAL_BIND` can mount outside this prefix.
- **Pages (`/apps/{plugin_id}/{path}`):**
  - **Purpose:** User-facing HTML (HTMX).
  - **CSRF:** The route handler receives a CSRF token to be used by any POST/PUT/DELETE actions.
  - **Registration:**
    - **Handler-based:** The plugin provides an `http.Handler` and renders the full page or fragment.
    - **Template-based:** The plugin provides a Go template string. Cog renders this template and allows it to execute SQLite queries directly to reduce gRPC round-trips for data-heavy pages.
- **Actions:**
  - **Purpose:** State-changing operations (POST/PUT/DELETE) from forms or HTMX.
  - **Format:** Returns HTMX fragments.
  - **CSRF:** Strictly enforced using the plugin's namespaced secret.
- **Resources (`/apps/{plugin_id}/{resource_name}/{resource_id}`):**
  - Standardized URLs for specific plugin objects.
  - Used as the destination for **Global Search** results and **Public Link Sharing**.

### 3. Resource Metadata (Sharing)

Resources are **metadata definitions** that describe tables and columns in the plugin's (or user's) SQLite database. This metadata allows Cog to manage sharing.

- **Definition:** Plugins define which tables/rows represent shareable objects (e.g., a "Deck" or "Task").
- **Sharing Logic:** Cog uses this metadata to manage entries in the global `shares` table.
- **Cross-User Access:** When User B accesses a resource shared by User A, Cog invokes the plugin with a connection to User A's database, scoped to the specific shared identifiers and User B's permissions.

## Communication Interfaces

### 1. Go Interface (Native)

Used for internal plugins and high-performance extensions.

```go
type Plugin interface {
    ID() string
    Register(registrar PluginRegistrar) error
    Start(ctx context.Context) error
    Stop() error
}

type PluginRegistrar interface {
    RegisterAPI(path string, handler http.Handler)
    RegisterPage(path string, handler http.Handler)
    RegisterTemplPage(path string, template string) // Page with direct SQL template access
    RegisterAction(method, path string, handler http.Handler)
    RegisterResource(name string, handler ResourceHandler)
    RegisterCalDAVProvider(handler CalDAVProvider) // Enumerate CalDAV collections
    RegisterHook(hookName string, callback func(interface{}))
    GetDB() *sql.DB // Access to user's private DB
    GetFS() fs.FS   // Access to user's files
}
```

### 2. gRPC Interface (External)

Used for third-party plugins or those written in other languages (if supported later). A `bridge` package adapts gRPC calls to the native Go interface.
Privileged internal plugins (for example `files`) are not gRPC-loadable.

**Proto Definition (Sketch):**

```protobuf
service CogPlugin {
    rpc Register(RegisterRequest) returns (RegisterResponse);
    rpc HandleRequest(HttpRequest) returns (HttpResponse);
    rpc OnHook(HookEvent) returns (Empty);
}
```

## Plugin Capabilities

### Lifecycle

1.  **Load:** Plugin binary started or package initialized.
2.  **Handshake:** Exchange capabilities and version compatibility.
3.  **Register:** Plugin declares routes, hooks, and resources.
4.  **Run:** Plugin accepts requests.

### Access Control & Storage Models

Plugins request permissions in their manifest. Cog enforces these via the VFS and SQL Proxy.

- **`DB_SANDBOX` (Default):** The plugin is given its own SQLite file (e.g., `app_data/{id}/plugin.db`) in the user's data directory. It has full control over this file but cannot see other plugins' databases.
- **`DB_READ:<plugin_id> / DB_WRITE:<plugin_id>`:** The plugin is granted access to a specific other plugin's database. For example, `DB_READ:files` grants access to the file metadata database.
- **`FILES_SANDBOX` (Default):** The plugin is granted a private directory (e.g., `app_data/{id}/`).
- **`FS_READ:<plugin_id> / FS_WRITE:<plugin_id>`:** The plugin is granted access to the virtual filesystem exported by another plugin. `FS_READ:files` is the equivalent of accessing the user's primary files.
- **`ROUTE_GLOBAL_BIND`:** Allows the plugin to bind routes outside of its `/apps/{id}/` prefix (e.g., `remote.php/`, `index.php/`). **Restriction:** Only granted to plugins installed at the **System/Admin level**.
- **`PRIVILEGED_INTERNAL` (built-in only):** Host-only capability for in-process plugins. Not grantable to external plugins.

### Files as a Plugin

In Cog, the core file management system is itself a privileged internal plugin with the ID `files`.

- **Metadata:** Stored in the `files` plugin's private database.
- **Storage:** Manages the user's `files/` directory via the VFS.
- **Permissions:** Other plugins request `DB_READ:files` or `FS_WRITE:files` to interact with the user's files.
- **Loading Model:** In-process only. Not implementable over gRPC.

### Search & Indexing

Plugins can extend Cog's global search by registering a `SearchProvider`.

- **Enrichment Query:** Plugins provide a SQL template (using `IN (?)`) that fetches the full "Source of Truth" for a batch of `rowid`s.
- **Dependency Mapping:** Plugins define secondary tables (e.g., tags, categories) that trigger a re-index of a parent resource when changed.
- **Field Mapping:** Plugins specify how SQL columns map to Bleve field types (`Text`, `Numeric`, `Date`) and if they should be `Stored` or `Indexed Only`.
- **Delete Handling:** Cog automatically handles `DELETE` hooks and purges the index if an enrichment query returns no rows for a resource.

### Sharing & Resources

Plugins define "Resources" that can be shared.

- Example: A "Deck" in a Kanban plugin.
- The plugin registers `resource_type: "deck"`.
- Cog handles the UI for sharing a "deck" with User B.
- When User B accesses the deck, Cog checks `shares` table and then invokes the plugin with the context of User B accessing User A's resource.

## Security

- **CSRF:** Page and action routes are protected by Cog's CSRF middleware. Token-authenticated API/compat routes are exempt.
- **Isolation:** gRPC plugins run in separate processes. If they crash, the main server survives.

## Distribution

- **System Plugins:** Installed in `/var/lib/cog/plugins/`. Available to all.
- **User Plugins:** Installed in User's home directory. Only active for that user.
