# Concepts and Data Types

This document is the canonical quick-reference for Cog's core architecture concepts and shared data shapes.

## Core Concepts

- **Host:** The main Cog server process that owns auth, routing, permissions, and system/global state.
- **Plugin:** Feature module loaded by the host. Plugins are either external (gRPC) or internal (in-process).
- **Privileged Internal Plugin:** Built-in, in-process-only plugin with host-only extension interfaces. Example: `files`.
- **Resource:** Shareable object identified by `(resource_type, resource_id)` within an owner's namespace.
- **Subject:** Access target for shares. Either a user or group.
- **Global Shares DB:** System database containing canonical share records and group membership state.
- **User DB (`user.db`):** Per-user SQLite state for user-scoped data (settings/auth artifacts).
- **Plugin DB (`app_data/{id}/plugin.db`):** Per-user plugin-specific storage.
- **VFS:** Unified filesystem abstraction used by WebDAV/SFTP/SMB/FTP/S3 handlers and plugins.
- **Search Collector:** Async indexing pipeline that applies DB changes to per-user Bleve indexes.

## Plugin Kinds

```text
External Plugin:
  - Process: Separate
  - Transport: gRPC bridge
  - Privileged host extensions: No

Internal Plugin:
  - Process: In host
  - Transport: Direct Go interface
  - Privileged host extensions: Optional

Privileged Internal Plugin:
  - Process: In host
  - Transport: Direct Go interface only
  - gRPC-loadable: No
  - Example: files
```

## Canonical Data Type Outlines

```go
// User/group recipient selector used by sharing and auth checks.
type SubjectRef struct {
    Type string // "USER" or "GROUP"
    ID   string // UUID
}

// Stable pointer to a shareable object.
type ResourceRef struct {
    Type string // e.g. "files:file", "calendar:event", "tasks:list"
    ID   string // stable plugin-defined identifier (not a mutable path)
}

// Canonical global share record.
type ShareRecord struct {
    ID           string // UUID
    OwnerUserID  string // UUID
    Resource     ResourceRef
    Subject      SubjectRef
    Permissions  uint32 // bitmask: READ/WRITE/SHARE...
    Token        *string
    PasswordHash *string
    Expiration   *int64  // unix seconds
    CreatedAt    int64   // unix seconds
    UpdatedAt    int64   // unix seconds
}

// Effective authorization result after user/group expansion.
type AccessDecision struct {
    Allowed          bool
    EffectivePerms   uint32
    MatchedShareIDs  []string
    OwnerUserID      string
    ScopedResourceID string
}

// Search indexing event emitted from DB change detection.
type IndexEvent struct {
    OwnerUserID string
    PluginID    string
    Table       string
    RowID       int64
    Operation   string // "INSERT", "UPDATE", "DELETE"
}

// Queued propagation for shared-resource indexing.
type SearchPropagationJob struct {
    OwnerUserID  string
    RecipientID  string // user UUID
    Resource     ResourceRef
    TriggerShare string // share UUID
}
```

## UI Extension Types

```go
// Navigation entry registered by a plugin for the app navbar.
type NavItem struct {
    Label string // display label, e.g. "Files"
    URL   string // link target, e.g. "/apps/files"
    Icon  string // optional CSS class
}

// Dashboard widget rendered on the root page.
type Widget struct {
    Title    string
    Priority int // lower = rendered first
    Render   func(ctx context.Context, w io.Writer, userID string) error
}
```

## Invariants

- `shares` is global and canonical.
- Share targets use stable resource IDs, not mutable paths.
- Permission checks resolve subjects first (user + groups), then resolve resource ownership.
- Cross-user reads execute against the owner's DB with host-enforced scope filters.
- Search is eventually consistent: indexes converge asynchronously after writes.

