# Database Design

Cog uses a distributed database model where each user has their own SQLite database. This improves scalability, privacy, and portability.

## System Database (Global)

A global SQLite database handles system-wide identity, sharing, and routing state.

**Table: `users`**

- `id` (UUID)
- `username` (Unique)
- `password_hash`
- `home_mount` (Path to data root, e.g., `local://var/lib/cog/data/alice`)
- `created_at`
- `is_admin` (Boolean, first user auto-promoted)
- `display_name` (Text)
- `avatar` (Text, filename in `{dataDir}/avatars/`, empty = no avatar)

**Table: `groups`**

- `id` (UUID)
- `name`
- `parent_group_id` (Nullable, for nesting)

**Table: `group_members`**

- `group_id`
- `member_id` (User UUID or Group UUID)
- `member_type` ('USER' or 'GROUP')

**Table: `shares`**

- `id` (UUID)
- `owner_user_id` (UUID)
- `resource_type` (Text, e.g., 'files:file', 'calendar:event')
- `resource_id` (Text, stable resource identifier from the owning plugin)
- `subject_type` ('USER' or 'GROUP')
- `subject_id` (UUID)
- `permissions` (Integer bitmask)
- `token` (Text, Nullable)
- `password_hash` (Text, Nullable)
- `expiration` (Integer, Nullable)
- `created_at` (Integer)
- `updated_at` (Integer)

**Indexes (shares)**

- `(subject_type, subject_id)`
- `(owner_user_id)`
- `(resource_type, resource_id)`
- `(token)` where token is not null

**Table: `plugin_settings`**

- `plugin_id` (Text PK)
- `enabled` (Integer, 1 = enabled, 0 = disabled)

Stores admin-managed plugin enable/disable state. Plugins default to enabled if no row exists. Protected plugins (dashboard, files) ignore this table.

**Table: `volumes`**

- `id` (UUID)
- `name` (Unique)
- `description` (Text)
- `uri` (Text, e.g. `file:///var/lib/cog/data/shared-media`)
- `owner_user_id` (UUID FK, Nullable — NULL = system-level)
- `created_at` (Integer)
- `config` (JSON text, default '{}')

Volumes represent storage backends. The `uri` scheme selects the backend driver (`file://`, `webdav://`, `mem://`). Volumes are shareable resources (`files:volume`) — they can be shared with users or groups like any other resource. Volumes are either system-level (created by admins, `owner_user_id` is NULL) or user-owned.

**Table: `mounts`**

- `id` (UUID)
- `user_id` (UUID FK)
- `user_path` (Text, e.g. `/external/nas`)
- `volume_id` (UUID FK)
- `volume_subpath` (Text, default '')
- `read_only` (Integer, default 0)
- `created_at` (Integer)
- Unique: `(user_id, user_path)`

Mounts bind a volume (or subpath within it) to a location in a user's virtual filesystem. Users can mount any volume they have access to (owned or shared) at any path in their VFS. Each user has a default volume created during provisioning. Groups do not have mounts or a VFS.

**Table: `group_membership_closure` (cache)**

- `ancestor_group_id`
- `descendant_user_id`
- `depth`

Materialized closure table rebuilt after any group membership change. Used by share queries (`CheckAccess`, `ListBySubject`) for fast transitive group permission lookups instead of recursive subqueries.

## Plugin Root Databases

Each plugin gets a root-level database at `app_data/{plugin_id}/root.db` for plugin-wide state shared across all users (e.g., global config, poll schedules). This is separate from the per-user plugin databases.

## User Database Schema (Per-User SQLite)

Each user has a `user.db` inside their data root for user-scoped state. Plugin data is stored in `app_data/{plugin_id}/plugin.db`.

### Database Connection Strategy

To support the `sqlite3_update_hook` required for the search indexer, Cog uses:

- **WAL Mode:** `PRAGMA journal_mode=WAL` is enabled on all databases.
- **Single Writer Connection per DB:** All writes for a given database are funneled through one writer connection that owns the `sqlite3_update_hook`. Read-only pooled connections are allowed.

### Core System State (`user.db`)

**Table: `user_settings`**

- `key` (Text)
- `value` (Blob)
- `updated_at` (Integer)
- _Primary Key: (`key`)_

**Table: `ssh_keys`**

- `id` (UUID)
- `name` (Text)
- `public_key` (Text)
- `created_at` (Integer)

### Files Plugin Metadata (`app_data/files/plugin.db`)

The `files` plugin is a privileged in-process plugin. It uses the standard plugin interface plus host-only internal extensions, and cannot run over gRPC.

**Table: `files`**

- `id` (Integer PK)
- `path` (Text, Indexed)
- `parent_path` (Text, Indexed)
- `name` (Text)
- `size` (Integer)
- `mtime` (Integer)
- `mime_type` (Text)
- `etag` (Text)
- `is_dir` (Boolean)

### Plugins (Generic Storage)

Plugins can request their own tables within their per-user `plugin.db` database. Each plugin gets an isolated SQLite database at `app_data/{plugin_id}/plugin.db`.

## Search Index (Per-User)

To support full-text search without bloating the main metadata database, Cog maintains a separate search index for each user.

- **File:** `.search` Bleve index directory.
- **Location:** `app_data/system/search/`
- **Isolation:** Independent of `user.db` to allow for easy reconstruction and specialized optimization.

### Calendar Plugin Metadata (`app_data/calendar/plugin.db`)

**Table: `calendars`**

- `id` (UUID PK)
- `name` (Text)
- `color` (Text, Hex)
- `description` (Text)
- `order` (Integer)
- `sync_token` (Text, for CalDAV)

**Table: `calendar_objects`**

- `id` (UUID PK)
- `calendar_id` (UUID FK)
- `ics_data` (Text, Full RFC 5545 blob)
- `uid` (Text, Unique ID from iCal)
- `etag` (Text, for sync)
- `start_time` (Integer, Indexed)
- `end_time` (Integer, Indexed)
- `summary` (Text)
- `is_recurring` (Boolean)

**Table: `calendar_exceptions`**

- `id` (UUID PK)
- `object_id` (UUID FK, Parent event)
- `recurrence_id` (Text, Instance date)
- `ics_data` (Text, Override data)

### Tasks Plugin Metadata (`app_data/tasks/plugin.db`)

**Table: `task_lists`**

- `id` (UUID PK)
- `name` (Text)
- `color` (Text, Hex)
- `order` (Integer)

**Table: `tasks`**

- `id` (UUID PK)
- `list_id` (UUID FK)
- `parent_id` (UUID FK, Nullable for subtasks)
- `ics_data` (Text, Full RFC 5545 VTODO blob)
- `uid` (Text, Unique ID from iCal)
- `etag` (Text, for sync)
- `summary` (Text)
- `description` (Text)
- `priority` (Integer)
- `percent_complete` (Integer)
- `completed_at` (Integer)
- `due_at` (Integer, Indexed)
- `start_at` (Integer, Indexed)

### RSS Plugin Metadata (`app_data/rss/plugin.db`)

**Table: `rss_folders`**

- `id` (UUID PK)
- `name` (Text)
- `order` (Integer)

**Table: `rss_feeds`**

- `id` (UUID PK)
- `folder_id` (UUID FK, Nullable)
- `url` (Text)
- `site_url` (Text)
- `title` (Text)
- `etag` (Text, Nullable)
- `last_modified` (Text, Nullable)
- `poll_interval_sec` (Integer)
- `next_check_at` (Integer, Indexed)
- `last_checked_at` (Integer)
- `last_success_at` (Integer, Nullable)
- `error_count` (Integer)
- `is_paused` (Boolean)

**Table: `rss_items`**

- `id` (UUID PK)
- `feed_id` (UUID FK)
- `guid` (Text, Nullable)
- `url` (Text)
- `title` (Text)
- `author` (Text)
- `content_html` (Text)
- `published_at` (Integer, Indexed)
- `fetched_at` (Integer)
- `is_read` (Boolean, Indexed)
- `is_starred` (Boolean, Indexed)
- _Unique: (`feed_id`, `guid`) where `guid` is not null_

### Notes Plugin Metadata (`app_data/notes/plugin.db`)

**Table: `note_books`**

- `id` (UUID PK)
- `name` (Text)
- `order` (Integer)
- `created_at` (Integer)
- `updated_at` (Integer)

**Table: `notes`**

- `id` (UUID PK)
- `notebook_id` (UUID FK, Nullable)
- `title` (Text, Indexed)
- `body_markdown` (Text)
- `body_text` (Text)
- `etag` (Text, Indexed)
- `is_archived` (Boolean, Indexed)
- `created_at` (Integer)
- `updated_at` (Integer, Indexed)

**Table: `note_tags`**

- `id` (UUID PK)
- `name` (Text, Unique)

**Table: `note_tag_map`**

- `note_id` (UUID FK)
- `tag_id` (UUID FK)
- _Primary Key: (`note_id`, `tag_id`)_

### Contacts (CardDAV) - Core Plugin Schema

**Table: `address_books`**

- `id` (UUID PK)
- `name` (Text)
- `description` (Text)
- `color` (Text, Hex)
- `order` (Integer)
- `sync_token` (Text)
- `created_at` (Integer)
- `updated_at` (Integer)

**Table: `cards`**

- `id` (UUID PK)
- `address_book_id` (UUID FK)
- `uri` (Text, Indexed)
- `vcard_data` (Text)
- `uid` (Text, Indexed)
- `etag` (Text, Indexed)
- `fn` (Text)
- `email` (Text, Indexed)
- `tel` (Text, Indexed)
- `rev` (Text)
- `created_at` (Integer)
- `updated_at` (Integer)
- _Unique: (`address_book_id`, `uid`)_
