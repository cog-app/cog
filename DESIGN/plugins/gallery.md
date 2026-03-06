# Gallery Plugin Design

The Gallery plugin (`id: gallery`) provides photo and video management, EXIF/metadata extraction, auto-album generation, configurable image recognition (tagging + face detection), template-based file renaming, and NextCloud Memories API compatibility.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin (not Protected — can be disabled).
- **Storage:** `app_data/gallery/plugin.db` per user.
- **Cache Files:** `app_data/gallery/video_thumbs/` for video thumbnail cache.
- **Dependencies:** Files plugin (media discovery, thumbnails, rename), share store, user store.
- **External Tools:** ffmpeg/ffprobe (optional, for video support).

## 2. Data Model

### Media

Core index — one row per image or video file discovered in the user's storage.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `file_id` | INTEGER NOT NULL | Stable ID from files plugin (cross-DB, app-level integrity) |
| `file_path` | TEXT NOT NULL | Denormalized for fast lookup |
| `file_etag` | TEXT NOT NULL | Change detection — rescan when etag differs |
| `mime_type` | TEXT NOT NULL | |
| `width` | INTEGER | |
| `height` | INTEGER | |
| `orientation` | INTEGER DEFAULT 1 | EXIF orientation 1–8 |
| `date_taken` | INTEGER | Unix timestamp from EXIF DateTimeOriginal |
| `date_indexed` | INTEGER NOT NULL | When this file was processed |
| `latitude` | REAL | GPS decimal degrees |
| `longitude` | REAL | |
| `altitude` | REAL | |
| `camera_make` | TEXT DEFAULT '' | |
| `camera_model` | TEXT DEFAULT '' | |
| `lens_model` | TEXT DEFAULT '' | |
| `focal_length` | REAL | |
| `f_number` | REAL | |
| `exposure_time` | TEXT DEFAULT '' | e.g. "1/250" |
| `iso` | INTEGER | |
| `flash` | INTEGER | EXIF flash bitmask |
| `duration_ms` | INTEGER | Video only |
| `video_codec` | TEXT DEFAULT '' | |
| `audio_codec` | TEXT DEFAULT '' | |
| `place_name` | TEXT DEFAULT '' | Reverse geocoded display name |
| `country` | TEXT DEFAULT '' | |
| `region` | TEXT DEFAULT '' | |
| `city` | TEXT DEFAULT '' | |
| `recognition_state` | INTEGER DEFAULT 0 | 0=pending, 1=done, 2=error, 3=skipped |
| `recognition_at` | INTEGER | |

Indexes: `UNIQUE(file_id)`, `(file_path)`, `(date_taken DESC)`, `(country, city)`, `(camera_make, camera_model)`, `(recognition_state)`.

Resource Type: `gallery:photo`.

### Albums

Both auto-generated and user-created.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `name` | TEXT NOT NULL | |
| `description` | TEXT DEFAULT '' | |
| `album_type` | INTEGER DEFAULT 0 | 0=manual, 1=date, 2=location, 3=camera, 4=folder, 5=face |
| `auto_key` | TEXT DEFAULT '' | Grouping key, e.g. "2024-07", "USA/Portland", "Canon/EOS R5" |
| `cover_media_id` | INTEGER | FK to media, SET NULL on delete |
| `sort_order` | INTEGER DEFAULT 0 | |
| `created_at` | INTEGER | |
| `updated_at` | INTEGER | |

Constraint: `UNIQUE(album_type, auto_key)`.

Resource Type: `gallery:album`.

### Album Media

Many-to-many — media can appear in multiple albums.

| Column | Type | Notes |
|--------|------|-------|
| `album_id` | INTEGER | FK to albums, CASCADE |
| `media_id` | INTEGER | FK to media, CASCADE |
| `sort_order` | INTEGER DEFAULT 0 | |
| `added_at` | INTEGER | |

PK: `(album_id, media_id)`. Index on `(media_id)`.

### Tags

From recognition or manual tagging.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `name` | TEXT NOT NULL UNIQUE | |
| `tag_type` | INTEGER DEFAULT 0 | 0=manual, 1=object, 2=scene |

### Media Tags

| Column | Type | Notes |
|--------|------|-------|
| `media_id` | INTEGER | FK to media, CASCADE |
| `tag_id` | INTEGER | FK to tags, CASCADE |
| `confidence` | REAL DEFAULT 1.0 | 0.0–1.0 for recognition results |

PK: `(media_id, tag_id)`.

### People

Named people, clustered from face embeddings.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `name` | TEXT DEFAULT '' | User-assigned (empty = unnamed cluster) |
| `representative_face_id` | INTEGER | FK to faces |
| `visible` | INTEGER DEFAULT 1 | User can hide a person |
| `created_at` | INTEGER | |

### Faces

Detected faces in images.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `media_id` | INTEGER NOT NULL | FK to media, CASCADE |
| `person_id` | INTEGER | FK to people, SET NULL on delete |
| `x`, `y`, `w`, `h` | REAL NOT NULL | Normalized bounding box (0.0–1.0) |
| `embedding` | BLOB | float32 array (128 or 512-dim) |
| `confidence` | REAL DEFAULT 0.0 | |

Indexes: `(media_id)`, `(person_id)`.

### Gallery Settings

Per-user key/value configuration.

| Key | Example | Description |
|-----|---------|-------------|
| `recognition_backend` | `ollama` | `none`, `ollama`, `openai`, `compreface` |
| `recognition_url` | `http://localhost:11434` | Backend API URL |
| `recognition_api_key` | `sk-...` | API key (OpenAI) |
| `recognition_model` | `llava:13b` | Model name |
| `recognition_enabled` | `true` | Master toggle |
| `face_detection_enabled` | `true` | Face detection toggle |
| `face_min_confidence` | `0.7` | Minimum face confidence |
| `tag_min_confidence` | `0.5` | Minimum tag confidence |
| `rename_pattern` | `{{.Year}}/{{.Month}}/{{.Day}}/{{.Original}}{{.Ext}}` | Go template |
| `rename_auto` | `false` | Auto-rename on scan |
| `scan_paths` | `/,/Photos` | Paths to scan (default: all) |

### Rename Log

For undo support.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `file_id` | INTEGER NOT NULL | |
| `old_path` | TEXT NOT NULL | |
| `new_path` | TEXT NOT NULL | |
| `renamed_at` | INTEGER | |

## 3. Background Processing Pipeline

### Scan Loop

Follows the RSS polling pattern — a goroutine launched from `Start(ctx)` that runs a multi-stage pipeline on a timer (initial delay + periodic rescan every 15 minutes).

```
Stage 1: File Discovery
  Query files plugin DB for image/* and video/* MIME types.
  Compare file_etag against media.file_etag.
  Insert new, update changed, remove deleted.

Stage 2: Metadata Extraction
  Images: read EXIF (rwcarlsen/goexif or dsoprea/go-exif).
  Videos: ffprobe -print_format json -show_format -show_streams.

Stage 3: Reverse Geocoding
  For media with GPS coordinates but no place_name.
  Offline: embedded GeoNames city database (~2–3 MB, e.g. sams96/rgeo).
  Optional online fallback: configurable Nominatim URL, rate-limited.

Stage 4: Auto-Album Generation
  Group media by date (month), location (city), camera model, source folder.
  UPSERT albums with (album_type, auto_key) uniqueness.
  Link new media via album_media.

Stage 5: Image Recognition (if configured)
  For media with recognition_state=0.
  Dispatch to configured backend (see §6).
  Store tags in media_tags, faces in faces table.

Stage 6: Face Clustering
  When new face embeddings arrive.
  Incremental: compare against existing person centroids.
  Agglomerative clustering with configurable cosine distance threshold (default 0.6).
  Pure Go implementation — no external dependencies needed.
```

### Incremental Scanning

The scanner reads from the Files plugin's SQLite index (not the filesystem), making it fast even for large libraries:

```sql
SELECT id, path, name, size, mtime, mime_type, etag
FROM files WHERE is_dir = 0
  AND (mime_type LIKE 'image/%' OR mime_type LIKE 'video/%')
```

Files are classified as new (insert), changed (re-extract), or deleted (cascade remove).

## 4. API and Routing

### Pages (GET, HTML)

| Path | Handler | Description |
|------|---------|-------------|
| `{$}` | Timeline | Default view — virtual-scroll photo timeline |
| `albums` | Albums | Album grid |
| `album/{id}` | AlbumView | Single album with photo grid |
| `photo/{id}` | PhotoView | Single photo detail with EXIF sidebar |
| `people` | People | Face/person grid |
| `person/{id}` | PersonView | Photos of a specific person |
| `places` | Places | Map or location-grouped view |
| `tags` | Tags | Tag cloud / tag browser |
| `videos` | Videos | Video-only timeline |
| `settings` | Settings | Recognition config, rename pattern, scan controls |

### API (GET, JSON/HTMX)

| Path | Description |
|------|-------------|
| `api/timeline` | Day-grouped timeline data for virtual scroll |
| `api/albums` | Album list with counts |
| `api/media/{id}` | Full media detail (EXIF, tags, faces, albums) |
| `api/faces/{id}/thumbnail` | Cropped face thumbnail |
| `api/media/{id}/thumbnail` | Delegates to files.Preview or video thumb |

### Actions (POST/PUT/DELETE, CSRF-protected)

| Method | Path | Description |
|--------|------|-------------|
| POST | `albums` | Create manual album |
| POST | `albums/{id}/add` | Add media to album |
| DELETE | `albums/{id}/media/{media_id}` | Remove from album |
| DELETE | `albums/{id}` | Delete album |
| POST | `people/{id}/name` | Name a person |
| POST | `people/{id}/merge` | Merge two people |
| POST | `people/{id}/hide` | Hide a person |
| POST | `settings/rename-pattern` | Set rename template |
| POST | `settings/scan` | Trigger manual scan |
| POST | `rename/apply` | Apply rename (after preview) |
| POST | `rename/undo` | Undo last rename batch |

## 5. Sharing and Resources

- Shareable resources:
  - `gallery:album` — sharing grants access to all photos in the album.
    - Permission levels: View (READ), Contribute (READ+WRITE), Full Access (READ+WRITE+RESHARE).
  - `gallery:photo` — sharing a single media item.
- Public links supported via Cog's share system (token, optional password, expiration).
- Cross-plugin: resource handler reads from file owner's databases, not the viewer's.

## 6. Image Recognition Architecture

### Backend Interface

Recognition is optional and user-configurable. Abstracted behind a `RecognitionBackend` interface:

- `Analyze(ctx, imagePath) -> (tags []RecognizedTag, faces []DetectedFace, err)`
- `Name() string`
- `Available() bool`

### Backends (priority order)

1. **Ollama** (recommended default): Local or remote Ollama instance with a vision model (llava, moondream). Good for scene/object tagging. Pair with a dedicated face detector for faces.
2. **OpenAI Vision**: Any OpenAI-compatible API (including local vLLM). Configurable URL and key. Good for tagging, no face embeddings.
3. **CompreFace / InsightFace** (future): Self-hosted HTTP API for face detection + recognition with embeddings.
4. **ONNX Runtime** (future): Local inference via CGo ONNX bindings. YOLOv8 for objects, RetinaFace + ArcFace for faces. Most self-contained but requires C libraries.

### Face Clustering

- Agglomerative clustering on face embedding vectors (cosine distance, threshold 0.6).
- Incremental: new faces compared against existing person centroids first.
- User corrections are authoritative — naming/merging people updates the clustering.
- Pure Go with simple vector math (no gonum dependency needed for cosine distance).

## 7. File Rename System

### Template Engine

Uses Go `text/template` with a `RenameData` struct:

| Field | Example | Description |
|-------|---------|-------------|
| `Year` | "2024" | From EXIF DateTimeOriginal |
| `Month` | "07" | |
| `Day` | "15" | |
| `Hour` | "14" | |
| `Minute` | "32" | |
| `Second` | "08" | |
| `Camera` | "Canon_EOS_R5" | Sanitized camera model |
| `Lens` | "RF24-70mm" | |
| `Original` | "IMG_4521" | Original filename without extension |
| `Ext` | ".jpg" | |
| `Seq` | 2 | Conflict sequence number |
| `Place` | "Portland" | City from reverse geocoding |
| `Country` | "USA" | |
| `Width` | 6000 | |
| `Height` | 4000 | |

Example patterns:
- `{{.Year}}/{{.Month}}/{{.Day}}/{{.Original}}{{.Ext}}` (default)
- `{{.Year}}/{{.Place}}/{{.Camera}}_{{.Original}}{{.Ext}}`
- `Photos/{{.Year}}/{{.Month}}/IMG_{{.Year}}{{.Month}}{{.Day}}_{{.Hour}}{{.Minute}}{{.Second}}{{.Ext}}`

### Rename Flow

1. **Preview**: Parse template, compute new paths for all affected media. Show old → new in a confirmation UI. No changes made.
2. **Apply**: For each file, call `files.Rename(username, oldPath, newPath)`. This preserves the file's stable ID and shares. Log each rename in `rename_log`. Also update `media.file_path`.
3. **Conflict resolution**: If target path exists, append `_N` before extension (e.g. `IMG_001_2.jpg`).
4. **Auto-mode**: If `rename_auto` is true, runs after metadata extraction in the scan pipeline. Otherwise manual from settings.
5. **Undo**: Reverse the most recent batch using `rename_log`.

### NextCloud Auto-Upload Compatibility

NextCloud mobile apps dump photos into a flat `InstantUpload/` or `Photos/` directory. The rename system is specifically designed to sort this mess — scan those directories, extract EXIF dates, and organize into `Year/Month/Day/` hierarchies.

## 8. NextCloud Memories API Compatibility

Mounted at `/apps/memories/api/` via `RegisterRoute`. All endpoints return JSON. Auth handled by route wrapper.

### Timeline

- `GET /apps/memories/api/days` — List of `{dayid, count}` where dayid = floor(unix_ts / 86400).
- `GET /apps/memories/api/days/{dayid}` — Photo details for a specific day.

### Folders

- `GET /apps/memories/api/folders` — Folder tree with media counts.
- `GET /apps/memories/api/folders/{path}` — Media in a specific folder.

### Tags and Faces

- `GET /apps/memories/api/tags` — All tags with counts.
- `GET /apps/memories/api/tags/{tag}` — Media with a specific tag.
- `GET /apps/memories/api/faces` — All recognized people.
- `GET /apps/memories/api/faces/{name}` — Media containing a specific person.
- `GET /apps/memories/api/face/preview/{id}` — Cropped face thumbnail.

### Albums

- `GET /apps/memories/api/albums` — List albums.
- `GET /apps/memories/api/albums/{id}` — Album contents.
- `POST /apps/memories/api/albums` — Create album.
- `PUT /apps/memories/api/albums/{id}` — Update album.
- `DELETE /apps/memories/api/albums/{id}` — Delete album.
- `POST /apps/memories/api/albums/{id}/add` — Add photos.
- `DELETE /apps/memories/api/albums/{id}/remove` — Remove photos.

### Places

- `GET /apps/memories/api/places` — Location clusters with counts.
- `GET /apps/memories/api/places/{id}` — Media at a specific place.

### Image Serving

- `GET /apps/memories/api/image/preview/{id}` — Delegates to files.Preview().
- `GET /apps/memories/api/image/info/{id}` — Full EXIF data as JSON.
- `GET /apps/memories/api/video/transcode/{id}` — Future: HLS adaptive streaming.

## 9. Video Handling

- **Detection:** MIME prefix `video/*` (MP4, MOV, WebM, MKV, AVI).
- **Thumbnail:** ffmpeg extracts frame at 1s (or 10% for short clips), scales to fit, caches at `app_data/gallery/video_thumbs/{etag}_{size}.jpg`.
- **Metadata:** ffprobe extracts duration, resolution, codecs, creation timestamp, rotation.
- **Playback:** Serve original file via Files plugin download. Browser handles MP4/WebM natively. `<video>` element with file URL.
- **Availability:** Check `exec.LookPath("ffmpeg")` at init. Skip video features gracefully if unavailable.
- **Future:** HLS transcoding for adaptive bitrate streaming, behind a setting.

## 10. Search Indexing

- Register `SearchSourceDef` for media table.
- Enrichment query returns `file_path` (as name) and `place_name` (as subtext).
- Tags could be indexed via secondary table dependency (re-index media when tags change).

## 11. Performance and Maintenance

- **Incremental scanning:** Only process files whose etag changed. First-time indexing may take minutes; subsequent scans are fast.
- **Timeline queries:** `idx_media_date_taken` makes day-grouping efficient.
- **Thumbnail reuse:** Images delegate to `files.Preview()`. Videos have their own cache.
- **Face embeddings:** ~512 bytes per face. 100k photos with 2 faces each = ~100 MB, manageable in-memory for clustering.
- **Recognition rate-limiting:** Configurable concurrency limit. Non-blocking background processing with progress visible in settings.
- **Batch writes:** Bounded SQLite transactions for bulk metadata inserts during scanning.

## 12. Security

- Validate rename templates — reject templates that produce paths outside the user's home.
- Sanitize camera model strings used in filenames (strip path separators, control chars).
- Recognition backends: validate/sanitize API responses. Do not trust external model output for filenames or SQL.
- Face bounding boxes: clamp to 0.0–1.0 range.
- Rate-limit recognition API calls to avoid quota exhaustion.
- CSRF protection on all state-changing endpoints (via RegisterAction).

## 13. File Layout

```
internal/plugin/gallery/
  gallery.go          Plugin struct, ID, Register, Start, Stop, DB
  store.go            CRUD for media, albums, tags, faces, people
  scanner.go          Background scan loop, file discovery, metadata extraction
  recognition.go      RecognitionBackend interface + dispatch
  recognition_ollama.go
  recognition_openai.go
  geocode.go          Reverse geocoding (embedded GeoNames or online)
  rename.go           Template-based rename engine + preview + undo
  clustering.go       Face embedding clustering
  video.go            ffmpeg/ffprobe integration
  pages.go            HTTP handlers
  pages.templ         Page templates
  widget.templ        Dashboard widget (recent photos grid)
  memoriesapi.go      Memories API compat handler
  seed.go             Dev seed data
  sql/
    schema.sql
    migration*.sql
```

## 14. Implementation Phases

1. **Core:** Plugin skeleton, schema, scanner (file discovery + EXIF), timeline page, basic album views.
2. **Auto-albums + rename:** Auto-album generation (date, folder, camera), reverse geocoding, rename engine with preview/undo.
3. **Video:** ffmpeg thumbnails, video metadata, browser playback, video timeline.
4. **Recognition + faces:** Backend interface, Ollama backend, face detection, clustering, people UI.
5. **Memories API:** Compat endpoints for the NextCloud Memories app.
6. **Sharing + polish:** Album/photo sharing, public link galleries, location views, widget.
