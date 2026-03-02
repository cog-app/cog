# Search Design

Cog provides a unified search interface that aggregates results from the filesystem and all active plugins. Search is powered by a per-user Bleve index, synchronized in real-time with the underlying SQLite databases.

## 1. Architecture: The Collector Pipeline

The search system uses an asynchronous "Collector" pipeline to ensure that indexing does not block database transactions or degrade application performance.

### Data Source & Change Detection
- **Single Connection Hook:** Cog uses a shared connection pool with `PRAGMA journal_mode=WAL`. The system registers a `sqlite3_update_hook` on the primary connection to capture every `INSERT`, `UPDATE`, and `DELETE`.
- **Asynchronous Processing:** The `UpdateHook` callback performs a non-blocking push of the `(table, rowid, operation)` to a buffered Go channel.
- **Deduplication:** The Collector deduplicates `rowid`s within a batch window (e.g., 50ms). If a row is updated multiple times rapidly, it is only re-indexed once.

### Indexing Pipeline (The Collector)
The Collector accumulates IDs from the channel and triggers a "flush" based on a buffer size (e.g., 100 items) or a time limit (e.g., 200ms of inactivity).
1. **Hydration:** For each batch, the Collector executes the plugin's **Enrichment Query** to fetch the current "Source of Truth".
2. **Atomic Updates:** Hydrated rows are processed using `index.NewBatch()` in Bleve. This ensures the inverted index stays consistent and performs efficiently.
3. **Error Resilience:** If an enrichment query fails or the database is busy, the specific batch of IDs is retried with exponential backoff.

## 2. Plugin Metadata Contract

Plugins register their searchable resources by providing metadata that defines how data is extracted and mapped to the index.

### Enrichment Query
Each plugin must provide a SQL template that accepts a slice of `rowid`s (using the `IN (?)` pattern) and returns the fields necessary for a search document.

**Required Query Columns:**
- `id`: Unique identifier for the resource.
- `name`: Primary title (1st line of results).
- `subtext`: Brief description (2nd line of results).
- **Index Fields:** Additional columns for content, tags, or metadata.

### Dependency Mapping & Field Mapping
- **Secondary Tables:** Plugins define "secondary" tables (e.g., `tags` or `categories`). When the `UpdateHook` detects a change in a secondary table, the system triggers a re-index of the associated parent `rowid` (e.g., the `note_id`).
- **Field Mapping:** Metadata defines which SQL columns map to specific Bleve field types (`Text`, `Numeric`, `Date`) and whether they should be `Stored` or `Indexed Only`.

## 3. Lifecycle Management

### Delete Handling
- **Explicit Deletes:** If the hook reports a `DELETE` operation, the ID is removed from Bleve immediately.
- **Orphaned Data:** If an enrichment query for an `UPDATE` operation returns no rows, the system assumes the data is orphaned and purges the ID from the index.

### Initial Bootstrapping
- **Full Sync:** Cog provides an RPC-triggered "Full Sync" that iterates through all IDs in a plugin's main table to populate the Bleve index for the first time or after a schema change.

## 4. Shared Resource Indexing

If a resource indexed by a plugin is shared (via the Cog `shares` table), Cog ensures that the index entries are propagated to the personal search indexes of all recipients.

- **Automatic Propagation:** When a share is created, Cog triggers an index update for the recipient.
- **Real-time Updates:** When a plugin updates an indexed resource, Cog checks for active shares and automatically updates the index for all users with access.
- **Privacy:** Recipients only see results for IDs they have been granted access to. The Host manages this scoping by correlating the plugin's resource ID with active sharing records.
