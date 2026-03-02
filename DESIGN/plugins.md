# Plugin Architecture

## Overview
Cog's plugin system is designed for flexibility and performance. Plugins can be compiled directly into the binary (Core/1st Party) or run as separate processes communicating via gRPC.

## Data Access & Queries

Since plugins may run in separate processes (gRPC), they cannot always open the SQLite file directly. Cog provides ways for plugins to query data:

### 1. SQL Proxy (Low-Level)
For plugins with `DB_SANDBOX` or `DB_USER_READ/WRITE` permissions, Cog exposes a gRPC service that acts as a SQL proxy.

- **Streaming Results:** Queries return a stream of rows.
- **Isolation:** The host enforces that plugins only access their permitted database files.

### 2. Resource Metadata (Sharing)
Resources are not a separate API, but **metadata** describing tables and columns in the plugin's (or user's) SQLite database. This metadata allows Cog to manage sharing.

- **Definition:** Plugins define which tables/rows represent shareable objects (e.g., a "Deck" or "Task").
- **Sharing Logic:** Cog uses this metadata to manage entries in the global `shares` table.
- **Cross-User Access:** When User B accesses a resource shared by User A, Cog invokes the plugin with a connection to User A's database, scoped to the specific shared identifiers and User B's permissions.

### 3. Cross-Plugin Queries (The Host as Orchestrator)
When a user performs a global search, the Host:
1.  Queries its internal `files` metadata.
2.  Parallelizes "Search" RPC calls to all active plugins.
3.  Aggregates the streaming responses into a single HTMX view.

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
    RegisterRoute(method, path string, handler http.Handler)
    RegisterHook(hookName string, callback func(interface{}))
    RegisterResource(name string, handler ResourceHandler)
    GetDB() *sql.DB // Access to user's private DB
    GetFS() fs.FS   // Access to user's files
}
```

### 2. gRPC Interface (External)
Used for third-party plugins or those written in other languages (if supported later). A `bridge` package adapts the gRPC calls to the native Go interface.

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

- **`DB_SANDBOX` (Default):** The plugin is given its own SQLite file (e.g., `app_data/{id}/plugin.db`) in the user's data directory. It has full control over this file but cannot see `user.db` or other plugins' databases.
- **`DB_USER_READ / DB_USER_WRITE`:** The plugin is granted access to the main `user.db`.
- **`FILES_SANDBOX` (Default):** The plugin is granted a private directory (e.g., `app_data/{id}/`). Files here are managed by the plugin and are not visible in the user's main `files/` UI.
- **`FILES_USER_READ / FILES_USER_WRITE`:** The plugin is granted access to the user's main `files/` directory.
- **`ROUTE_GLOBAL_BIND`:** Allows the plugin to bind routes outside of its `/apps/{id}/` prefix (e.g., `remote.php/`, `index.php/`). **Restriction:** Only granted to plugins installed at the **System/Admin level**.
- **`ACCESS_NETWORK`:** Permission to make outbound network calls.

### Sharing & Resources
Plugins define "Resources" that can be shared.
- Example: A "Deck" in a Kanban plugin.
- The plugin registers `resource_type: "deck"`.
- Cog handles the UI for sharing a "deck" with User B.
- When User B accesses the deck, Cog checks `shares` table and then invokes the plugin with the context of User B accessing User A's resource.

## Security
- **CSRF:** All plugin routes are automatically protected by Cog's CSRF middleware.
- **Isolation:** gRPC plugins run in separate processes. If they crash, the main server survives.
- **Sandboxing:** (Future) External plugins could be sandboxed (e.g., Wasm) or remote.

## Distribution
- **System Plugins:** Installed in `/var/lib/cog/plugins/`. Available to all.
- **User Plugins:** Installed in User's home directory. Only active for that user.
