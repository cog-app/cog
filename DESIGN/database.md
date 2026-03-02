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

Each user has a `user.db` inside their data root which contains core system state (like sharing). Individual plugins (including the `files` plugin) store their data in their own `app_data/{plugin_id}/plugin.db`.

### Database Connection Strategy
To support the `sqlite3_update_hook` required for the search indexer, Cog uses:
- **WAL Mode:** `PRAGMA journal_mode=WAL` is enabled on all databases.
- **Single Connection Pool:** Each user context uses a single shared connection pool (or a dedicated primary connection for the update hook) to ensure all writes are captured.

### Core System State (`user.db`)

**Table: `shares`**
- `id` (UUID)
- `resource_type` (Text, e.g., 'files:file', 'calendar:event')
- `resource_id` (Text)
- `owner_id` (UUID)
- `shared_with` (UUID)
- `permissions` (Integer bitmask)
- `token` (Text, Nullable)
- `expiration` (Integer, Nullable)

### Files Plugin Metadata (`app_data/files/plugin.db`)
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
Plugins can request their own tables, but standard practice is to use namespaced tables or a Key-Value store provided by the host.

**Table: `plugin_kv`**
- `plugin_id` (Text)
- `key` (Text)
- `value` (Blob)
- `updated_at` (Integer)
- *Primary Key: (plugin_id, key)*

## Search Index (Per-User)

To support full-text search without bloating the main metadata database, Cog maintains a separate search index for each user.

- **File:** `search.db` (if using SQLite FTS) or a Bleve index directory.
- **Location:** `app_data/system/search/`
- **Isolation:** Independent of `user.db` to allow for easy reconstruction and specialized optimization.

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
