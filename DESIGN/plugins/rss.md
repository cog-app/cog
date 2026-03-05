# RSS Plugin Design

The RSS plugin (`id: rss`) provides feed subscriptions, article ingestion, and NextCloud News-compatible APIs.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin.
- Uses only the public plugin interface
- **Storage:** `app_data/rss/plugin.db` per user.
- **Cache Files:** Optional under `app_data/rss/cache/` for HTTP cache metadata, favicons, and temporary parser artifacts.

## 2. Data Model

- **Folders:** Logical grouping of feeds.
  - Properties: `id`, `name`, `order`.
  - Resource Type: `rss:folder`.
- **Feeds:** Subscription sources owned by a user.
  - Properties: `id`, `folder_id`, `url`, `site_url`, `title`, `etag`, `last_modified`, `poll_interval_sec`, `last_checked_at`, `last_success_at`, `error_count`.
  - Resource Type: `rss:feed`.
- **Items:** Normalized entries from feeds.
  - Properties: `id`, `feed_id`, `guid`, `title`, `author`, `url`, `content_html`, `published_at`, `fetched_at`, `is_read`, `is_starred`.
- **User Item State:** Optional split table if item content dedupe is introduced later.

## 3. Sync and Fetch Pipeline

### Polling

- Scheduler selects feeds using `next_check_at` and backoff state.
- Fetches use conditional requests (`If-None-Match`, `If-Modified-Since`) to minimize bandwidth.
- 304 responses only update check timestamps.

### Parsing and Upsert

- Parse RSS/Atom with strict size/time limits.
- Normalize feed/item fields and upsert by `(feed_id, guid)` fallback `(feed_id, url)`.
- Keep write batches bounded to avoid long SQLite write transactions.

### Failure Handling

- Track consecutive failures per feed.
- Apply exponential backoff and jitter.
- Mark feeds as paused after a threshold; UI exposes retry controls.

## 4. API and Routing

- **Plugin UI Pages:** `/apps/rss/*`
- **Actions:** mark read/unread, star/unstar, add/remove feeds, move feeds between folders.
- **Compatibility API (News):** `/index.php/apps/news/api/v1-2/*`
  - Should be host-bound or privileged-internal route binding for best compatibility.

## 5. Sharing and Resources

- Default behavior: feeds and items are private to the user.

## 6. Search Indexing

- Register a `SearchProvider` for items.
- Suggested mapping:
  - `name`: item title
  - `subtext`: feed title + published date
  - Full-text fields: `content_html`, `author`, `site_url`
- Use eventual-consistent indexing pipeline with batched updates.

## 7. Performance and Maintenance

- Enforce response size caps and parser timeouts for hostile feeds.
- Keep a retention policy (for example max items per feed and max age).
- Use indexed read models for unread/starred timelines.
- Vacuum/analyze periodically during low-traffic windows.
- Emit metrics: feed checks, fetch latency, parse errors, queue depth, item ingest rate.

## 8. Security

- Sanitize HTML content before UI rendering.
- Do not execute remote scripts or inline event handlers from feed content.
- Restrict outbound fetch protocols to `http`/`https`.
- Validate and normalize redirect chains with max-hop limits.

## 9. Import/Export

- Abonnements (OPML) format to import/export feeds.
