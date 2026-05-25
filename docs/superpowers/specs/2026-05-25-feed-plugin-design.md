# Feed Plugin — Design

Date: 2026-05-25
Status: Approved, ready for implementation plan

## Purpose

Replace the existing `rss` and `media` plugins with a single unified plugin, `feed`. The plugin aggregates content from RSS/Atom feeds (including podcasts via enclosures), YouTube channels, and one-off URLs into a single item store. Filters tag items at ingest. Saved smart-query "playlists" select subsets for consumption. NextCloud News API continues to be served, scoped to text items. Audio/video items are consumed via a separate API surface intended for a future Android client.

This is a greenfield plugin. No user data migration. After cutover the `rss` and `media` plugin directories are deleted.

## Goals

- One plugin, one schema, one filter/tag/search model.
- RSS feeds with audio enclosures produce both a text item and an audio item per entry, so filtering and consumption stay type-uniform.
- Filters drive everything downstream of ingest: skip, tag, download, profanity-mute, transcribe.
- Search grammar is the existing cog-level grammar (`internal/query`) verbatim, used both for per-item filter evaluation and for Bleve-backed list views.
- Playlists are saved queries (smart only).
- Folders organize sources hierarchically-in-name-only (flat, NC News–compatible). Tags apply to items.

## Non-goals (v1)

- Manual playlist ordering.
- Nested folders.
- Cross-source dedup (same article from two sources stays two items).
- Migration of existing rss/media user data.
- Mobile client implementation (only the API it will consume).
- Transcription backend (action exists but stubs/TODO).

## Architecture

New plugin at `plugin/feed/` with `PluginID = "feed"`. Per-user SQLite database, opened on demand. Embedded `sql/` directory holds `schema.sql` and migrations. Data files (downloads, processed audio, captions) live in an admin-configured volume, same pattern as `plugin/media` uses today.

Package layout:

- `feed.go` — plugin entry, registration, lifecycle.
- `store.go` — DB access for folders, sources, items, filters, tags, item_tags, downloads, playback, playlists.
- `fetcher/` — kind-dispatched fetchers:
  - `rss.go` — RSS and Atom via `gofeed`; HTML sanitized with `bluemonday.UGCPolicy`; emits one text item if body present and one audio/video item per enclosure (mime-sniffed).
  - `youtube.go` — yt-dlp metadata fetch (no download); emits one video item per entry.
  - `url.go` — single-shot one-item source.
- `poller.go` — scheduling loop over `sources.next_check_at`, ignoring paused sources.
- `pipeline/` — `download.go`, `profanity.go`, `transcribe.go` workers, driven by filter actions / `downloads` table.
- `filter.go` — parses each filter's `query` at save time, evaluates per item at ingest using `query.Expr.Match`.
- `search.go` — builds the cog-level `SearchSourceDef` so items are indexed in Bleve and surfaced through cog-wide search.
- `ncapi/` — NextCloud News API surface; results scoped to `type='text'`, sources scoped to `kind='rss'`.
- `pages.go`, `pages.templ` — web UI (sidebar of folders + tags + playlists; main item list and detail).
- `widget.templ` — dashboard widget.
- `sql/schema.sql`, `sql/migration001.sql`, … — schema + future migrations.

## Data model

```sql
CREATE TABLE folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
-- Folders are ordered by name in the UI; no sort_order column.

CREATE TABLE sources (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL,
    kind TEXT NOT NULL CHECK(kind IN ('rss','youtube','url')),
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    site_url TEXT NOT NULL DEFAULT '',
    image_url TEXT NOT NULL DEFAULT '',
    favicon_url TEXT NOT NULL DEFAULT '',
    etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    poll_interval_sec INTEGER NOT NULL DEFAULT 1800,
    next_check_at INTEGER NOT NULL DEFAULT 0,
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    paused INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    guid TEXT NOT NULL,
    type TEXT NOT NULL CHECK(type IN ('text','audio','video')),
    title TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',            -- canonical link to original
    content_html TEXT NOT NULL DEFAULT '',   -- sanitized text body
    media_url TEXT NOT NULL DEFAULT '',      -- enclosure / yt media URL
    media_mime TEXT NOT NULL DEFAULT '',
    duration_sec INTEGER NOT NULL DEFAULT 0,
    thumbnail_url TEXT NOT NULL DEFAULT '',
    published_at INTEGER NOT NULL DEFAULT 0,
    fetched_at INTEGER NOT NULL DEFAULT (unixepoch()),
    is_read INTEGER NOT NULL DEFAULT 0,
    is_starred INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL DEFAULT '',
    UNIQUE(source_id, guid)
);

CREATE TABLE tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE item_tags (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY (item_id, tag_id)
);

CREATE TABLE filters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    query TEXT NOT NULL DEFAULT '*',
    action TEXT NOT NULL CHECK(action IN ('skip','tag','download','mute_profanity','transcribe')),
    action_value TEXT NOT NULL DEFAULT '',   -- tag name for action='tag', else unused
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE downloads (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK(state IN ('queued','downloading','ready','error')),
    local_path TEXT NOT NULL DEFAULT '',
    clean_audio_path TEXT NOT NULL DEFAULT '',
    captions_path TEXT NOT NULL DEFAULT '',
    bytes INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL DEFAULT 0,
    completed_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE playback (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    progress_sec REAL NOT NULL DEFAULT 0,
    completed INTEGER NOT NULL DEFAULT 0,
    last_played_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE playlists (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX idx_sources_next_check ON sources(next_check_at);
CREATE INDEX idx_sources_folder ON sources(folder_id);
CREATE INDEX idx_items_source ON items(source_id);
CREATE INDEX idx_items_published ON items(published_at DESC);
CREATE INDEX idx_items_unread ON items(is_read, published_at DESC);
CREATE INDEX idx_items_starred ON items(is_starred, published_at DESC);
CREATE INDEX idx_items_type ON items(type, published_at DESC);
CREATE INDEX idx_downloads_state ON downloads(state);
CREATE INDEX idx_filters_order ON filters(sort_order);
CREATE INDEX idx_playback_last ON playback(last_played_at DESC);
```

Design notes:

- An RSS entry with both a body and one or more enclosures produces multiple items: one `type='text'` item carrying `content_html`, plus one `type='audio'`/`'video'` item per enclosure carrying `media_url` and `media_mime`. The text item has empty media fields; media items have empty `content_html`. Items share the source but not the GUID — each item's `guid` is derived as `<entry-guid>` for text and `<entry-guid>#<enclosure-url-hash>` for media, ensuring uniqueness under the `(source_id, guid)` constraint.
- `downloads` is a separate row keyed on `item_id` so re-download or reprocessing does not churn the `items` row, and so download state is only present for items that need bytes.
- `playlists` v1 are smart-only: an ordered list of saved queries shown in the sidebar.

## Data flow

### Ingest

1. Poller selects `sources` where `next_check_at <= now AND paused = 0`, ordered by `next_check_at`.
2. Dispatch by `kind`:
   - `rss`: HTTP GET with `If-None-Match`/`If-Modified-Since` from prior `etag`/`last_modified`; parse with gofeed; sanitize HTML with `bluemonday.UGCPolicy`. Emit a text item per entry that has a body; emit one audio/video item per enclosure (mime-sniffed).
   - `youtube`: yt-dlp metadata fetch (no download); emit one `type='video'` item per video.
   - `url`: resolve once, emit a single item of the appropriate type.
3. `INSERT OR IGNORE` on `(source_id, guid)` deduplicates per source.
4. For each newly-inserted item, run the filter engine.
5. Update source: `last_checked_at`, `last_success_at`, `next_check_at = now + poll_interval_sec`; reset `error_count` and `last_error`.

On fetch error: increment `error_count`, set `last_error`, set `next_check_at = now + min(poll_interval_sec * 2^error_count, 86400)`. Log via slog. Never swallow.

### Filter engine

- Filters are loaded ordered by `sort_order ASC`.
- Each filter's `query` is parsed at save time using `internal/query`. Parse errors reject the form.
- At ingest, build a `query.Record` for each new item and call `expr.Match(record)`. All matching filters apply (not first-match) so a single item can be tagged and queued for download by separate filter rules.
- Actions:
  - `skip` — delete the item. Skipping is final; the `(source_id, guid)` row stays not-present, so re-fetch will re-evaluate against current filters. To suppress re-emergence permanently, the user can change the filter or pause the source.
  - `tag` — insert into `item_tags` with the tag named by `action_value` (created on demand).
  - `download` — upsert `downloads(item_id, state='queued')`.
  - `mute_profanity` — set a pending pipeline step on the item. If `download` is not also requested, the worker pulls bytes from `media_url` directly to a temp file, processes, and writes `clean_audio_path`.
  - `transcribe` — pipeline step recorded; the backend is stubbed in v1 and the action is a no-op that logs at debug level.

### Pipeline workers

- `downloader` pulls `downloads` rows where `state='queued'`, in `started_at IS 0 OR oldest` order, with a small concurrency limit. For `rss` audio/video items it HTTP GETs `media_url`; for `youtube` it invokes yt-dlp. On completion, writes `local_path`, `bytes`, `completed_at`, `state='ready'`. On error, `state='error'` with `error` text.
- `profanity` worker reads `local_path` (or `media_url` if `download` wasn't requested), invokes the existing `plugin/profanity` package, writes `clean_audio_path`.
- `transcribe` worker is stubbed; logs and marks no captions path.

### Read path

- Web UI item list reads through the cog-level search manager. Items are registered as a `SearchSourceDef` so the search box accepts the standard cog query grammar (`folder:Development tag:sunday is:unread`, free text, AND/OR/NOT, phrases). Bleve handles ranking and pagination.
- Playlist view executes the saved query through the same search manager.
- NextCloud News API filters to `type='text'` items from `kind='rss'` sources; folder, item, and feed shapes match the existing rss plugin's responses verbatim.
- A new feed-native JSON API exposes items where `type IN ('audio','video')` plus playback state, intended for a future mobile client. The API serves signed URLs to `clean_audio_path` when present, falling back to `local_path`, then `media_url`. Detailed shape is out of scope for this design.

### Search grammar and `internal/query`

The plugin reuses `internal/query` (`Parse`, `Expr`, `Record`) for both ingest-time filter matching and cog-level Bleve translation. To support multi-valued fields like `tag`, `query.Record` is changed from `map[string]string` to `map[string][]string`. All field-matching expressions (`FieldEqExpr`, `FieldInExpr`, `TextExpr`, `PhraseExpr`) iterate values. Callers that produce single-valued records continue to work by wrapping their string in a one-element slice.

The per-item record for the filter engine populates:

- `title`, `author`, `content` — text searched by `TextExpr`/`PhraseExpr`
- `type` — one of `text|audio|video`
- `kind` — source kind: `rss|youtube|url`
- `folder` — source's folder name, if any
- `tag` — multi-valued, set of currently-assigned tag names (tags added by earlier filters in the same ingest pass are visible to later filters)
- `is` — multi-valued status flags: `unread`, `starred`, `downloaded`
- `source` — source title
- `published` — RFC3339 timestamp

## Errors and operational notes

- Fetch errors: backoff on the source row, slog at warn.
- Filter save: parse errors reject the form; runtime match never errors.
- Download/processing errors: stored on `downloads.error`, last writer wins; UI shows pipeline state per item with a manual retry button that re-queues.
- No swallowed errors; every error path either wraps and returns or logs via slog with context.
- Per-user SQLite uses the default pool; do not set `MaxOpenConns=1` (deadlocks).
- Downloaded media and processed audio live under an admin-configured volume, same as `plugin/media` uses today (`<feed-volume>/<user>/<item_id>.<ext>` for raw, `clean/<item_id>.<ext>` for processed, `captions/<item_id>.<ext>` for captions).

## Testing

- **Unit:** filter engine (grammar parse + match per item), per-kind fetchers (RSS body-only, RSS with enclosure, YouTube via stub yt-dlp), record builder, download state transitions.
- **Integration via `cogtest`:** ingest fixtures covering text-only RSS, RSS with audio enclosure, RSS with video enclosure, YouTube channel, single URL. Filter rules assert tag/skip/download outcomes. NC News API responses match shapes from the current `plugin/rss` tests.
- **Browser via `webtest`:** sidebar (folders + tags + playlists), item list, search box uses cog-level search and returns expected items, settings forms for sources/folders/filters/playlists.
- **Testdata:** copied and extended from `plugin/rss/testdata`, adding feed-with-enclosure fixtures; YouTube tests use a mock yt-dlp binary in PATH, as the existing media tests do.
- **`internal/query` changes:** updated tests verifying multi-valued field matching.
- **Skip:** transcribe pipeline tests in v1 (action stubbed).
- **Search-source registration:** verified through existing search manager test patterns.

## Cutover

1. Change `internal/query.Record` to `map[string][]string` and update all existing callers (rss, media, search manager) to wrap values; tests stay green.
2. Build `plugin/feed/` end-to-end against the new `Record` type.
3. Switch service registration to use `feed` instead of `rss` and `media`.
4. Delete `plugin/rss/` and `plugin/media/` and their references.

No data migration is required (no existing users).
