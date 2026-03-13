# Plugin Architecture

## Plugin Index

- [`dashboard`](./dashboard.md): Root page with app grid and widget aggregation (protected).
- [`files`](./files.md): Core privileged storage plugin (protected).
- [`calendar`](./calendar.md): CalDAV calendar plugin.
- [`tasks`](./tasks.md): Task/VTODO plugin with CalDAV provider integration.
- [`contacts`](./contacts.md): CardDAV contacts plugin.
- [`rss`](./rss.md): News/RSS ingestion and compatibility plugin.
- [`notes`](./notes.md): Notes and tagging plugin.
- [`recipes`](./recipes.md): Recipe book with URL import and NextCloud Cookbook compatibility.

## Overview

Cog's plugin system is designed for flexibility and performance. Plugins fall into four trust tiers (see `concepts_and_types.md` for the full matrix):

- **Protected**: Vital to the system, cannot be removed/disabled. In-process Go only. Examples: dashboard, files.
- **Privileged**: In-process Go only. Can access internal APIs and register routes outside their prefix. Must be in the main repo.
- **Trusted**: Go or gRPC. Can define routes in their prefix and render pages. 1st or 3rd party, installed instance-wide. No real security boundary against malicious plugins.
- **External**: From a remote server. Users may add these themselves. UI renders in an iframe and is sandboxed. Routes limited to their prefix, APIs restricted by permissions.

See [`grpc_bridge.md`](./grpc_bridge.md) for the gRPC protocol used by trusted out-of-process plugins.

### Package Layout

- **`plugin/`** — Public plugin API. External authors import this to implement plugins.
  - `plugin.go` — `Plugin`, `Registrar` interfaces and all public types.
  - `plugpb/` — gRPC proto definition and generated code.
  - `plugcmd/` — `Run(plugin.Plugin)` helper for building plugin binaries.
- **`internal/plugin/`** — Host-side registry and privileged types.
  - `plugin.go` — `Protected` struct, type aliases to `plugin/`.
  - `registry.go` — Registry, route mounting, enable/disable.
  - `grpc.go` — `grpcPlugin` adapter and `LoadExternal`.

### Protected Pattern

Protected plugins (dashboard, files) embed `internal/plugin.Protected`. The registry auto-detects this in `Add()` and marks them as always-on. There is no `SetProtected` call.

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
  - **CSRF:** Strictly enforced. Tokens are namespaced per plugin via `HMAC(secret, sessionID+pluginID)`, automatically applied by `RegisterAction()`.
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
// plugin/plugin.go — public API
type Plugin interface {
    ID() string
    Register(registrar Registrar) error
    Start(ctx context.Context) error
    Stop() error
}

type Registrar interface {
    RegisterAPI(path string, handler http.Handler)
    RegisterPage(path string, handler http.Handler)
    RegisterAction(method, path string, handler http.Handler)
    RegisterResource(def ResourceDef)
    RegisterHook(hookName string, callback HookCallback)
    RegisterNavItem(item NavItem)
    RegisterWidget(w Widget)
    RegisterSettingsPage(page SettingsPage)
}
```

### 2. gRPC Interface (External)

Used for trusted out-of-process plugins. The `plugin/plugpb` package defines the wire protocol; `plugin/plugcmd` provides the plugin-side helper; `internal/plugin/grpc.go` provides the host-side adapter.
Protected and privileged plugins are not gRPC-loadable.

See [`grpc_bridge.md`](./grpc_bridge.md) for the full protocol definition and usage guide.

**Proto Service (from `plugin/plugpb/plugin.proto`):**

```protobuf
service PluginService {
    rpc GetID(Empty) returns (IDResponse);
    rpc Register(Empty) returns (stream RegistrationEvent);
    rpc Start(Empty) returns (Empty);
    rpc Stop(Empty) returns (Empty);
    rpc HandleHTTPRequest(HTTPRequest) returns (HTTPResponse);
    rpc RenderWidget(RenderWidgetRequest) returns (RenderWidgetResponse);
}
```

## Plugin Databases

Each plugin gets two levels of database storage:

- **Root database** (`app_data/{id}/root.db`): Plugin-wide state shared across all users (e.g., feed poll schedules, global config). One per plugin.
- **Per-user database** (`{user_data}/app_data/{id}/plugin.db`): User-scoped state. One per user per plugin.

## Settings Pages

Plugins can register one or more settings pages via `RegisterSettingsPage()`. Pages are automatically wrapped in the standard settings layout (sidebar + content area) and receive a navigation entry in the settings sidebar.

```go
type SettingsPage struct {
    Name    string       // optional display name
    Handler http.Handler // renders the page content
}
```

**Navigation rules:**

- The sidebar entry always includes the plugin's name as a prefix.
- **Single page, no name**: Displays as just the plugin name (e.g., "RSS").
- **Single page, with name**: Displays as "Plugin: Name" (e.g., "RSS: Feeds").
- **Multiple pages**: Displayed as a nested group under the plugin name.
  - A page with an empty name becomes the overview page shown when clicking the parent item.
  - If no page has an empty name, the parent item is not clickable (only expands).

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
- **`ROUTE_GLOBAL_BIND`:** Allows the plugin to bind routes outside of its `/apps/{id}/` prefix (e.g., `remote.php/`, `index.php/`). **Restriction:** Only available to privileged or protected plugins.
- **`PRIVILEGED_INTERNAL` (built-in only):** Host-only capability for in-process privileged/protected plugins. Not grantable to trusted or external plugins.

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

- **Protected/Privileged:** Compiled into the binary. Always available.
- **Trusted:** Installed instance-wide in `/var/lib/cog/plugins/`. Available to all users.
- **External:** Added per-user from remote servers. Only active for that user.
