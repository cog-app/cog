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

Groups are collections of users, and can contain other groups. Anyone can create a group and manage the ones they own: see [Groups](../using-cog/groups.md) for how owners and members use them. This section is what an administrator has besides.

### Managing groups

**Admin > Groups** lists every group, whoever owns it. An administrator can manage any group as its owner would, and delete one.

From the command line:

```bash
cog group create engineering
cog group add-member engineering alice
cog group add-member engineering bob
```

### Invitations by email

Owners can invite people by email only when [email](email.md) is set up. Without it, they get a link to send themselves.
