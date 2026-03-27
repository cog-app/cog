# Users and Groups

## Users

### Creating users

Create users from the CLI:

```bash
cog user create alice
cog user create --admin bob
```

Or from the web admin panel at **Admin > Users > New User**.

### User data

Each user gets an isolated directory under `COG_DATA_DIR`:

- `files/` -- the user's home directory (accessible via web UI, WebDAV, SFTP)
- `app_data/` -- per-plugin databases and caches
- `user.db` -- user-scoped settings and auth state

### Admin privileges

Admins can access the admin panel, manage other users, configure plugins, and manage system volumes. Grant admin privileges:

```bash
cog user set-admin alice
```

### Quotas

Admins can set storage quotas per user from the admin panel at **Admin > Users > Edit User**. Quotas limit the total file storage for a user. Uploads that would exceed the quota are rejected.

## Groups

Groups are collections of users. Groups can contain other groups for nested membership.

### Creating groups

```bash
cog group create engineering
cog group add-member engineering alice
cog group add-member engineering bob
```

Groups can also be managed from the web UI at **Settings > Groups** (for groups you own) or **Admin > Groups** (for system-wide management).

### Group nesting

Groups can contain other groups:

- Group "Engineering" contains users Alice, Bob, and group "Frontend"
- Group "Frontend" contains user Charlie
- Sharing a resource with "Engineering" grants access to Alice, Bob, and Charlie

### Invitations

Group owners can invite users via invitation links. Invited users can accept from a link without needing admin intervention.

### Sharing with groups

Any shareable resource (files, calendars, bookmarks folders, etc.) can be shared with a group. All current and future members of the group receive the specified permissions. See [Sharing](../using-cog/sharing.md) for details.
