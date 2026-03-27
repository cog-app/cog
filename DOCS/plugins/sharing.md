# Sharing Resources

Plugins can register resource types that users can share with other users, groups, or via public links.

## Registering a resource

```go
ctx.RegisterResource(plugin.ResourceDef{
    Type:         "folder",
    Table:        "folders",
    IDColumn:     "id",
    ParentColumn: "parent_id",
    Handler:      plugin.ResourceHandlerFunc(serveFolderResource),
    PermissionLevels: []plugin.PermissionLevel{
        {Label: "Read-only", Permissions: plugin.PermRead},
        {Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
        {Label: "Full access", Permissions: plugin.PermRead | plugin.PermWrite | plugin.PermReshare},
    },
})
```

## ResourceDef

```go
type ResourceDef struct {
    Type             string             // e.g. "folder", "calendar"
    Table            string             // SQLite table name
    IDColumn         string             // Column identifying the resource
    ParentColumn     string             // Optional: for hierarchical sharing
    Handler          ResourceHandler    // Serves requests for individual resources
    PermissionLevels []PermissionLevel  // Named permission presets
}
```

The full resource type is formed as `{plugin_id}:{type}` (e.g. `bookmarks:folder`, `calendar:calendar`).

### ParentColumn

If set, sharing a parent resource implicitly grants access to all children. For example, sharing a directory grants access to files inside it. Cog walks the parent hierarchy when checking access.

### Handler

Serves requests when a user accesses a specific resource (e.g. from a search result or shared link):

```go
type ResourceHandler interface {
    ServeResource(w http.ResponseWriter, r *http.Request, resourceID string)
}
```

Use `ResourceHandlerFunc` to adapt a function.

### PermissionLevels

If empty, defaults to "Read-only" (PermRead) and "Read/Write" (PermRead | PermWrite).

## Permission constants

```go
const (
    PermRead     uint32 = 1   // View
    PermWrite    uint32 = 2   // Modify
    PermReshare  uint32 = 4   // Share with others
    PermFreeBusy uint32 = 8   // Calendar free/busy info
    PermSchedule uint32 = 16  // Calendar scheduling
)
```

## Accessing the share store

Use `ShareStore()` from `PluginContext` for programmatic share operations:

```go
type ShareStore interface {
    ListBySubject(subjectID string) iter.Seq2[ShareRecord, error]
    CheckAccess(userID, resourceType, resourceID string) (*AccessDecision, error)
    Create(ownerUserID, resourceType, resourceID, subjectType, subjectID string, permissions uint32) (*ShareRecord, error)
    Delete(id string) error
}
```

## How cross-user access works

When User B accesses a resource owned by User A:

1. Cog checks the global `shares` table for User B's access (direct, group, or instance shares)
2. If authorized, Cog invokes the plugin with a connection to **User A's database**
3. The request context includes the resource IDs and permissions User B has

The plugin doesn't need to implement sharing logic -- Cog handles the access control and database scoping.
