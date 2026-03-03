# Notes Plugin Design

The Notes plugin (`id: notes`) provides markdown/text note management with foldering, tagging, sharing, and full-text search integration.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin.
- **Deployment:** Internal or external (gRPC) depending on install model.
- **Storage:** `app_data/notes/plugin.db` per user.

## 2. Data Model

- **Note Books (optional):** Logical containers for notes.
  - Properties: `id`, `name`, `order`.
  - Resource Type: `notes:notebook`.
- **Notes:** Primary content records.
  - Properties: `id`, `notebook_id`, `title`, `body_markdown`, `body_text`, `etag`, `created_at`, `updated_at`, `is_archived`.
  - Resource Type: `notes:note`.
- **Tags:** Labeling for filtering.
  - Properties: `id`, `name`.
- **Note Tags:** many-to-many note/tag mapping.

## 3. Edit and Sync Behavior

- Writes require optimistic concurrency via `etag` or `updated_at`.
- Autosave is debounced and writes bounded-size transactions.
- Optional history snapshots can be stored in a separate revisions table.

## 4. API and Routing

- **Pages:** `/apps/notes/*`
- **Actions:** create/update/delete/archive/restore notes, manage notebooks/tags.
- **API:** `/api/notes/*` for clients and automation.
- **Resources:** `/apps/notes/note/{id}` as canonical deep-link.

## 5. Sharing and Resources

- Shareable resources:
  - `notes:notebook` (recommended boundary)
  - `notes:note`
- Permissions:
  - `READ`: view
  - `WRITE`: edit
  - `SHARE`: reshare/manage
- Resolution uses global `shares` + owner DB scoped query enforcement.

## 6. Search Indexing

- Register `SearchProvider` for notes.
- Suggested mapping:
  - `name`: note title
  - `subtext`: notebook + updated timestamp
  - Searchable fields: `title`, `body_text`, tag names
- Use eventual-consistent indexing pipeline with dependency mapping for tags.

## 7. Performance and Maintenance

- Index `updated_at`, `notebook_id`, and tag joins for fast listing/filtering.
- Keep `body_text` as pre-stripped search text to avoid render cost in queries.
- Batch-tag updates and search reindex requests.
- Background maintenance can compact revisions and prune old tombstones.

## 8. Security

- Sanitize rendered markdown/HTML in UI responses.
- Enforce scope checks for shared-note access.
- Protect state-changing actions with CSRF for browser flows.
- Rate-limit large note import/export endpoints.

