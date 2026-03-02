# Sharing & Permissions Design

## Conceptual Model

Sharing in Cog is **Resource-Based** and **Hierarchical**.

### Entities

1.  **Subject:** The entity receiving access (User or Group).
2.  **Object:** The resource being shared (File, Directory, Calendar, Plugin Object).
3.  **Permission:** What the subject can do (Read, Write, Reshare).

## Group Nesting

Groups are defined recursively.

- Group A contains User 1, User 2, and Group B.
- Group B contains User 3.
- Sharing to Group A implicitly shares to User 1, 2, 3.

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
1.  **Permission Check:** Cog verifies the share exists in the global `shares` table.
2.  **Context Injection:** Cog invokes the plugin, but instead of connecting it to User B's database, it provides a connection to **User A's database**.
3.  **Scoped ID/Permissions:** The request context includes the specific resource IDs User B is allowed to see and their permission bitmask.
4.  **Plugin Logic:** The plugin performs standard SQLite queries on the provided connection. Cog's SQL Proxy ensures the plugin cannot query rows outside the shared scope if restricted.

### Search Indexing
When a resource is shared, Cog ensures it is indexed in the recipient's personal search index. This allows users to find shared content (e.g., a shared Deck or Note) using the global search bar without navigating to the specific plugin first.

## Implicit Sharing (Hierarchy)
If a plugin registers a hierarchy (e.g., `Deck` owns `Cards`), sharing the `Deck` implicitly grants access to all `Cards` where `card.deck_id == deck.id`.

## Public Sharing

- **Link Shares:** A unique token is generated.
- **Expiration:** Optional auto-expiry.
- **Password Protection:** Optional hashed password stored with the share.
