# Plugin Architecture

## Overview
Cog's plugin system is designed for flexibility and performance. Plugins can be compiled directly into the binary (Core/1st Party) or run as separate processes communicating via gRPC.

## Data Access & Queries

Since plugins may run in separate processes (gRPC), they cannot always open the SQLite file directly. Cog provides two primary ways for plugins to query data:

### 1. SQL Proxy (Low-Level)
For plugins with `ACCESS_DB_READ/WRITE` permissions, Cog exposes a gRPC service that acts as a SQL proxy. This allows plugins to execute arbitrary SQL against the user's private SQLite database.

- **Streaming Results:** Queries return a stream of rows to minimize memory overhead for large result sets.
- **Isolation:** The host enforces that plugins only access their own tables or permitted system tables.

```protobuf
service DatabaseService {
    // Executes a query and streams back rows as encoded Map/JSON or Protobuf Values
    rpc QueryStream(SQLRequest) returns (stream Row);
    rpc Exec(SQLRequest) returns (ExecResponse);
}
```

### 2. Resource API (High-Level)
This is the preferred method for most plugins. Instead of writing SQL, plugins interact with "Resources" (Files, Calendar Events, etc.).

- **Streaming Filter:** Plugins can request resources with filters (e.g., `modified_since: 1234567`).
- **Optimization:** The host optimizes these queries using indexes in the SQLite DB and streams the results.

```go
// Example Go SDK usage for a gRPC plugin
rows, err := cog.DB().Query("SELECT * FROM my_plugin_table WHERE status = ?", "active")
for rows.Next() {
    // Rows are streamed from the host to the plugin process
}
```

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

### Access Control
Permissions are requested in the plugin manifest (`plugin.json` or `Manifest` struct).
- `ACCESS_FILES_READ`: Read user files.
- `ACCESS_FILES_WRITE`: Write user files.
- `ACCESS_DB_READ`: Read user SQLite.
- `ACCESS_DB_WRITE`: Write user SQLite.
- `ACCESS_NETWORK`: Make outbound network calls.

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
