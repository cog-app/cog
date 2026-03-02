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

## Plugin Resources

Plugins must register their resource hierarchy if they want implicit sharing.

- _Example:_ A "Project Management" plugin.
- Resource: `Project` (Parent) -> `Task` (Child).
- If `Project` is shared, `Task` is accessible.
- The plugin implements a `CheckParentAccess(childID string) bool` interface or provides the parent ID to Cog during registration.

## Public Sharing

- **Link Shares:** A unique token is generated.
- **Expiration:** Optional auto-expiry.
- **Password Protection:** Optional hashed password stored with the share.
