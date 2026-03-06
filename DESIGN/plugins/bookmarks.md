# Bookmarks Plugin Design

The Bookmarks plugin (`id: bookmarks`) provides bookmark management with folders, tags, and NextCloud Bookmarks-compatible APIs.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin.
- **Storage:** `app_data/bookmarks/plugin.db` per user.
- **Compatibility Target:** NextCloud Bookmarks REST API v2 (`/index.php/apps/bookmarks/public/rest/v2/`, corresponds to app v4+).

## 2. Data Model

- **Folders:** Hierarchical containers for organizing bookmarks.
  - Properties: `id`, `parent_id` (nullable, NULL = root), `title`, `user_id`, `created_at`, `updated_at`.
  - Resource Type: `bookmarks:folder`.
  - Self-referential tree via `parent_id`.
- **Bookmarks:** Saved URLs with metadata.
  - Properties: `id`, `url`, `title`, `description`, `text_content`, `favicon_url`, `click_count`, `available`, `created_at`, `updated_at`.
  - Resource Type: `bookmarks:bookmark`.
- **Bookmark Folders:** Many-to-many mapping of bookmarks to folders. A bookmark can exist in multiple folders.
  - Properties: `bookmark_id`, `folder_id`, `index` (sort order within folder).
  - Bookmarks without a folder mapping live in the implicit root folder (`folder_id = -1`).
- **Tags:** Flat labels for bookmarks.
  - Properties: `bookmark_id`, `tag` (text).
  - Indexed on both columns for fast lookup in either direction.

### Schema

```sql
CREATE TABLE folders (
    id         INTEGER PRIMARY KEY,
    parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
    title      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE bookmarks (
    id           INTEGER PRIMARY KEY,
    url          TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    text_content TEXT NOT NULL DEFAULT '',
    favicon_url  TEXT NOT NULL DEFAULT '',
    click_count  INTEGER NOT NULL DEFAULT 0,
    available    INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE UNIQUE INDEX idx_bookmarks_url ON bookmarks(url);

CREATE TABLE bookmark_folders (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    folder_id   INTEGER NOT NULL DEFAULT -1,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bookmark_id, folder_id)
);
CREATE INDEX idx_bookmark_folders_folder ON bookmark_folders(folder_id);

CREATE TABLE tags (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    PRIMARY KEY (bookmark_id, tag)
);
CREATE INDEX idx_tags_tag ON tags(tag);
```

## 3. Compatibility API

Routes are mounted under `/index.php/apps/bookmarks/public/rest/v2/` using `ROUTE_GLOBAL_BIND`.

### Bookmark Endpoints

| Method   | Path                    | Description                     |
|----------|-------------------------|---------------------------------|
| `GET`    | `/bookmark`             | List/search bookmarks           |
| `POST`   | `/bookmark`             | Create bookmark                 |
| `GET`    | `/bookmark/{id}`        | Get single bookmark             |
| `PUT`    | `/bookmark/{id}`        | Update bookmark                 |
| `DELETE` | `/bookmark/{id}`        | Delete bookmark                 |
| `POST`   | `/bookmark/click`       | Record click (body: `{"url":"..."}`) |
| `GET`    | `/bookmark/{id}/favicon`| Get bookmark favicon image      |
| `GET`    | `/bookmark/{id}/image`  | Get bookmark thumbnail/screenshot |

#### List/Search Query Parameters

- `page` (int): page number (0-indexed).
- `limit` (int): items per page (default 10, max 300).
- `sortby` (string): `lastmodified`, `title`, `clickcount`, `url`, `added` (default `lastmodified`).
- `search[]` (string array): search terms matched against title, URL, description, tags.
- `tags[]` (string array): filter by tags (AND logic).
- `folder` (int): restrict to a specific folder (-1 for root).
- `url` (string): find bookmark by exact URL match.
- `unavailable` (bool): filter by availability status.
- `untagged` (bool): filter to bookmarks with no tags.
- `conjunction` (string): `and` (default) or `or` for combining search/tag filters.

#### Bookmark JSON Shape

```json
{
  "id": 42,
  "url": "https://example.com",
  "title": "Example",
  "description": "An example page",
  "added": 1672531200,
  "lastmodified": 1672531200,
  "clickcount": 3,
  "available": true,
  "tags": ["dev", "reference"],
  "folders": [5, 12]
}
```

- Timestamps are Unix epoch seconds for API responses (matching NC Bookmarks convention).
- **Upsert behavior:** `POST /bookmark` with a URL that already exists updates the existing bookmark rather than creating a duplicate.

### Folder Endpoints

| Method   | Path                            | Description                       |
|----------|---------------------------------|-----------------------------------|
| `GET`    | `/folder`                       | List all folders (flat or tree)   |
| `POST`   | `/folder`                       | Create folder                     |
| `GET`    | `/folder/{id}`                  | Get single folder                 |
| `PUT`    | `/folder/{id}`                  | Update folder (rename, reparent)  |
| `DELETE` | `/folder/{id}`                  | Delete folder and contents        |
| `GET`    | `/folder/{id}/hash`             | Get content hash for sync         |
| `GET`    | `/folder/{id}/childorder`       | List direct children (bookmarks + subfolders, ordered) |
| `PATCH`  | `/folder/{id}/childorder`       | Reorder children                  |
| `POST`   | `/folder/{fid}/bookmarks/{bid}` | Add bookmark to folder            |
| `DELETE` | `/folder/{fid}/bookmarks/{bid}` | Remove bookmark from folder       |

#### Folder JSON Shape

```json
{
  "id": 5,
  "title": "Development",
  "parent_folder": -1,
  "children": [
    {"type": "folder", "id": 12},
    {"type": "bookmark", "id": 42}
  ]
}
```

### Tag Endpoints

| Method   | Path               | Description                |
|----------|--------------------|----------------------------|
| `GET`    | `/tag`             | List all tags (array of strings) |
| `PUT`    | `/tag`             | Rename tag (body: `{"old":"...","new":"..."}`) |
| `DELETE` | `/tag`             | Delete a tag from all bookmarks (body: `{"old":"..."}`) |

### Hashing (Sync)

The `/folder/{id}/hash` endpoint returns a SHA-256 hash of the folder's contents (including nested subfolders), allowing clients to detect changes without fetching everything. The hash is computed recursively:
- Leaf folder: hash of sorted `(bookmark_id, sort_order)` pairs.
- Parent folder: hash of sorted `(child_type, child_id, child_hash)` tuples.

## 4. Import/Export

| Method | Path        | Description                     |
|--------|-------------|---------------------------------|
| `POST` | `/bookmark/import` | Import from Netscape HTML  |
| `GET`  | `/bookmark/export` | Export as Netscape HTML    |

- **Import:** Accepts `multipart/form-data` with an HTML bookmarks file. Preserves folder hierarchy from `<DT><H3>` / `<DL>` structure. Deduplicates by URL.
- **Export:** Returns `text/html` in Netscape Bookmark File Format with nested `<DL>` for folders and `<DT><A>` for bookmarks. Includes `ADD_DATE` and `TAGS` attributes.

## 5. Plugin UI Pages and Actions

- **Pages:** `/apps/bookmarks/*`
  - Folder tree sidebar with bookmark list.
  - Tag cloud / tag filter view.
  - Bookmark detail view with edit form.
- **Actions:** create/edit/delete bookmarks and folders, tag management, import/export.
- **Resources:** `/apps/bookmarks/bookmark/{id}` as canonical deep-link.

### Nav Item

- Label: "Bookmarks"
- Icon: bookmark/star icon
- Path: `/apps/bookmarks/`

### Widget

- Dashboard widget showing recently added or most-clicked bookmarks.

## 6. Sharing and Resources

- Shareable resources:
  - `bookmarks:folder` (recommended share boundary — shares all bookmarks within)
- Permissions:
  - `READ`: view bookmarks and folder contents
  - `WRITE`: add/edit/delete bookmarks within shared folder
  - `SHARE`: reshare
- Shared folders appear in the recipient's bookmark tree.
- Public share links generate a read-only page for the folder's contents.

### Compatibility Share Endpoints

| Method   | Path                              | Description                          |
|----------|-----------------------------------|--------------------------------------|
| `POST`   | `/folder/{id}/shares`             | Create share (user or group)         |
| `GET`    | `/folder/{id}/shares`             | List shares for a folder             |
| `GET`    | `/share/{share_id}`               | Get share details                    |
| `PUT`    | `/share/{share_id}`               | Update share (canWrite, canShare)    |
| `DELETE` | `/share/{share_id}`               | Remove share                         |
| `POST`   | `/folder/{id}/publictoken`        | Create public link token             |
| `DELETE` | `/folder/{id}/publictoken`        | Remove public link token             |

- Share creation body: `{"participant":"user_or_group","type":0,"canWrite":false,"canShare":false}` (type 0=user, 1=group).
- Public token allows unauthenticated read-only access via `?token={token}` query parameter on folder/bookmark endpoints.
- These endpoints map internally to Cog's global `shares` table with resource type `bookmarks:folder`.

## 7. Search Indexing

- Register a `SearchProvider` for bookmarks.
- Suggested mapping:
  - `name`: bookmark title
  - `subtext`: URL + folder name
  - Full-text fields: `title`, `description`, `url`, `text_content`, tag names
- Dependency mapping: tag changes trigger re-index of associated bookmarks.

## 8. Background Jobs

### Availability Checker

- Periodically HEAD-request bookmark URLs to detect broken links.
- Update `available` flag; track consecutive failures with backoff.
- Respect `Cache-Control` and `Retry-After` headers.
- Restrict to `http`/`https` protocols with redirect hop limits.

### Favicon Fetcher

- Fetch favicons for bookmarks that lack one.
- Check `/favicon.ico` and parse `<link rel="icon">` from HTML.
- Cache fetched favicons in `app_data/bookmarks/favicons/`.

### Content Scraper (Optional)

- Fetch page text content for full-text search enrichment.
- Store stripped text in `text_content`.
- Bounded fetch size and timeout to prevent abuse.

## 9. Performance and Maintenance

- Index `url` (unique), `folder_id`, `tag`, and `updated_at` for fast queries.
- Folder tree queries use recursive CTE for nested listing.
- Pagination enforced on all list endpoints.
- Bulk operations (import, tag rename) use bounded write batches.
- Retention: no auto-deletion; users manage their own bookmarks.

## 10. Security

- Sanitize bookmark titles, descriptions, and URLs in HTML rendering.
- Validate URLs on create/update: reject `javascript:`, `data:`, and other non-http(s) schemes.
- Outbound requests (availability check, favicon, scraper) restricted to `http`/`https` with redirect limits.
- CSRF enforced on all page/action routes.
- Rate-limit import endpoint to prevent abuse.
- Enforce max bookmark count per user (configurable, default unlimited).
