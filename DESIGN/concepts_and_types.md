# Concepts and Data Types

This document is the canonical quick-reference for Cog's core architecture concepts and shared data shapes.

## Core Concepts

- **Host:** The main Cog server process that owns auth, routing, permissions, and system/global state.
- **Plugin:** Feature module loaded by the host. See Plugin Kinds below for the four trust tiers.
- **Resource:** Shareable object identified by `(resource_type, resource_id)` within an owner's namespace.
- **Subject:** Access target for shares. Either a user or group.
- **Global Shares DB:** System database containing canonical share records and group membership state.
- **User DB (`user.db`):** Per-user SQLite state for user-scoped data (settings/auth artifacts).
- **Plugin Root DB (`app_data/{id}/root.db`):** Plugin-wide state shared across all users.
- **Plugin User DB (`{user_data}/app_data/{id}/plugin.db`):** Per-user plugin-specific storage.
- **VFS:** Per-user filesystem abstraction used by WebDAV/SFTP/SMB/FTP/S3 handlers and plugins. Groups do not have a VFS.
- **Volume:** Shareable storage backend resource (`files:volume`). Either system-level or user-owned. Users mount volumes in their VFS.
- **Search Collector:** Async indexing pipeline that applies DB changes to per-user Bleve indexes.

## Plugin Kinds

Four trust tiers, from most to least privileged:

```text
Protected:
  - Process: In host
  - Transport: Direct Go interface
  - Removal: Cannot be removed or disabled
  - Notes: Vital to the system. Functionally equivalent to Privileged
           but always-on. Examples: dashboard, files.

Privileged:
  - Process: In host
  - Transport: Direct Go interface only (not gRPC-loadable)
  - Removal: Can be disabled by admin
  - Notes: Go only. Can access internal APIs and register routes
           outside their prefix. Must be defined in the main repo.

Trusted:
  - Process: In host or separate (gRPC)
  - Transport: Go interface or gRPC bridge
  - Removal: Can be disabled by admin
  - Notes: Can define routes in their prefix and render pages.
           1st or 3rd party. Must be installed instance-wide.
           No real security boundary against malicious plugins.

External:
  - Process: Remote server
  - Transport: HTTP API (sandboxed)
  - Removal: Users may add/remove these themselves
  - Notes: UI renders in an iframe and is sandboxed. Limited to
           routes in their prefix. APIs restricted by permissions.
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

