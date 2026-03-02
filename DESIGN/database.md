# Database Design

Cog uses a distributed database model where each user has their own SQLite database. This improves scalability, privacy, and portability.

## System Database (Global)
A small global SQLite database (or manageable configuration file) handles routing and user discovery.

**Table: `users`**
- `id` (UUID)
- `username` (Unique)
- `password_hash`
- `home_mount` (Path to data root, e.g., `local://var/lib/cog/data/alice`)
- `created_at`

**Table: `groups`**
- `id` (UUID)
- `name`
- `parent_group_id` (Nullable, for nesting)

**Table: `group_members`**
- `group_id`
- `member_id` (User UUID or Group UUID)
- `member_type` ('USER' or 'GROUP')

## User Database Schema (Per-User SQLite)

Each user has a `user.db` (or `.cog.db`) inside their data root.

### Filesystem Metadata
**Table: `files`**
- `id` (Integer PK)
- `path` (Text, Indexed, Relative to user root)
- `parent_path` (Text, Indexed)
- `name` (Text)
- `size` (Integer)
- `mtime` (Integer)
- `mime_type` (Text)
- `etag` (Text, for WebDAV sync)
- `is_dir` (Boolean)

### Sharing
**Table: `shares`**
- `id` (UUID)
- `resource_type` (Text, e.g., 'file', 'calendar_event')
- `resource_id` (Text, e.g., path for files, UUID for others)
- `owner_id` (UUID, User who owns this)
- `shared_with` (UUID, User or Group ID)
- `permissions` (Integer bitmask: Read, Write, Share, etc.)
- `token` (Text, Nullable, for public link sharing)
- `expiration` (Integer, Nullable)

### Plugins (Generic Storage)
Plugins can request their own tables, but standard practice is to use namespaced tables or a Key-Value store provided by the host.

**Table: `plugin_kv`**
- `plugin_id` (Text)
- `key` (Text)
- `value` (Blob)
- `updated_at` (Integer)
- *Primary Key: (plugin_id, key)*

### Calendar (CalDAV) - Core Plugin Schema
**Table: `calendars`**
- `id` (UUID)
- `name` (Text)
- `color` (Text)
- `sync_token` (Text)

**Table: `calendar_objects`**
- `id` (UUID)
- `calendar_id` (FK)
- `ics_data` (Text)
- `uid` (Text, from iCal)
- `etag` (Text)

### Contacts (CardDAV) - Core Plugin Schema
**Table: `address_books`**
- `id` (UUID)
- `name` (Text)

**Table: `cards`**
- `id` (UUID)
- `address_book_id` (FK)
- `vcard_data` (Text)
- `uid` (Text)
- `etag` (Text)
