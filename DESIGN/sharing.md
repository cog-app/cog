# Sharing & Permissions Design

## Conceptual Model

Sharing in Cog is **Resource-Based** and **Hierarchical**.
The canonical share records are stored in the global/system database.

### Entities

1.  **Subject:** The entity receiving access (User or Group).
2.  **Object:** The resource being shared (File, Directory, Calendar, Plugin Object).
3.  **Permission:** What the subject can do (Read, Write, Reshare).

## Group Nesting

Groups are defined recursively.

- Group A contains User 1, User 2, and Group B.
- Group B contains User 3.
- Sharing to Group A implicitly shares to User 1, 2, 3.

To keep permission checks fast, Cog may maintain a `group_membership_closure` cache in the global DB.

**Resolution Logic:**
When checking permissions for User X on Object Y:

1.  Check direct share (User X -> Object Y).
2.  Check group shares (Is User X in any group G where G -> Object Y?).
3.  Check parent objects (Does User X have access to Parent(Y) which implies Y?).

## Implicit Sharing (Ownership)

Certain resources "own" others.

- A **Directory** owns the **Files** inside it.
- If User A shares `Directory/Photos` with User B, User B implicitly gets access to `Directory/Photos/vacation.jpg`.

**Implementation:**

- The `shares` table primarily stores explicit shares.
- Access checks for files perform a recursive check up the directory tree if no direct share is found.

For files, share records target a stable file/directory ID from the `files` plugin metadata. Path is treated as mutable metadata.

## Plugin Resources (SQLite Metadata)

Resources are **metadata definitions** that describe tables and key columns within a plugin's SQLite database. This allows Cog to manage sharing without the plugin needing to implement its own sharing logic.

### Metadata Registration
Plugins register their shareable resources by specifying:
- **Resource Type:** (e.g., `deck`, `calendar_event`)
- **Table Name:** The SQLite table where the resource is stored.
- **Identity Column:** The column (e.g., `id`, `uuid`) that uniquely identifies the resource.
- **Parent Column:** (Optional) For hierarchical sharing (e.g., `project_id` on a `task` table).

### Shared Access Execution
When User B attempts to access a resource owned by User A:
1.  **Permission Check:** Cog resolves User B's effective subjects (user ID + group IDs) and verifies access in the global `shares` table.
2.  **Context Injection:** Cog invokes the plugin, but instead of connecting it to User B's database, it provides a connection to **User A's database**.
3.  **Scoped ID/Permissions:** The request context includes the specific resource IDs User B is allowed to see and their permission bitmask.
4.  **Plugin Logic:** The plugin performs standard SQLite queries on the provided connection. Cog's SQL Proxy ensures the plugin cannot query rows outside the shared scope if restricted.

No cross-file SQLite foreign keys are assumed between the global `shares` DB and per-user/plugin databases.

### Search Indexing
When a resource is shared, Cog ensures it is indexed in the recipient's personal search index. This allows users to find shared content (e.g., a shared Deck or Note) using the global search bar without navigating to the specific plugin first.

## Implicit Sharing (Hierarchy)
If a plugin registers a hierarchy (e.g., `Deck` owns `Cards`), sharing the `Deck` implicitly grants access to all `Cards` where `card.deck_id == deck.id`.

## Public Sharing

- **Link Shares:** A unique token is generated.
- **Expiration:** Optional auto-expiry.
- **Password Protection:** Optional hashed password stored with the share.

## Global Share Record Shape

- `id` (UUID)
- `owner_user_id` (UUID)
- `resource_type` (Text)
- `resource_id` (Text)
- `subject_type` ('USER' or 'GROUP')
- `subject_id` (UUID)
- `permissions` (Integer bitmask)
- `token` (Text, Nullable)
- `password_hash` (Text, Nullable)
- `expiration` (Integer, Nullable)

## Shared with me, mounts and storage volumes

A user's files are a tree of mounts (`internal/mount`), each one of three
kinds:

- **volume** — a part of a volume, as a share of the volume allows. Only a
  *raw* volume can be mounted this way by someone it is shared with.
- **area** — the user's own space in a *storage* volume, at
  `<username>/<name>` on it. Sharing a storage volume lets its audience make
  areas, and see nothing else of it; only its owner (or an admin, for a
  system volume) sees it whole. Switching a volume from raw to storage turns
  existing mounts of it into areas in place.
- **share** — a folder someone shared with the user, named by owner and file
  ID. The resolver finds the folder's current path in the owner's files and
  mounts the owner's backend there, as the owner's shares allow each time the
  tree is built: revoked, it disappears; read-only, it is read-only.

Everything shared with a user is listed beside their files, at
`/apps/files/shared` on the web and `/shared/<owner>/<name>` over SSH, where a
folder can be browsed without mounting it. "Add to my files" (or `ln -s` in
the shell) makes a share mount; "Add storage" makes an area; removing either
(Settings → Storage, `rm`/`unlink`) only unmounts.

A change made through a share mount is made **as the owner**: the owner's
rows keep the IDs shares and favourites hang from, what is deleted goes to the
owner's trash, and the bytes count against the owner's quota on the volume
they land on. Quotas are per volume (`quota.Account`): a write is charged to
whoever the mount holding its path charges.

Over SSH, `/volumes/<name>` shows the volumes the user may look at, read-only:
a volume they may see whole, or on a storage volume, their own areas (mounted
or not). `ln -s` from there mounts a folder of the volume, or an area by name;
what is written goes through the mount, recorded and held to the quota. The
server's own files in the data directory are never shown.

Sharing on what is in a share mount needs the Reshare permission.
