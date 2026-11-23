# Gallery

The Gallery app provides photo and video management with automatic organization, metadata extraction, and optional image recognition.

## Features

- **Timeline view** -- browse photos chronologically
- **Albums** -- manual and auto-generated (by date, location, camera, folder)
- **EXIF metadata** -- view camera settings, GPS coordinates, and timestamps
- **Video support** -- thumbnails and playback (requires ffmpeg)
- **People/faces** -- face detection and clustering (optional, see Recognition below)
- **Tags** -- automatic and manual tagging
- **Places** -- browse photos by location (reverse geocoding)
- **File renaming** -- organize photos by date/location using templates

## Background scanning

The gallery scans your files automatically in the background. It reads from the file metadata index (not the filesystem directly), so scanning is fast even for large libraries. New, changed, and deleted files are detected incrementally.

## Auto-albums

The gallery automatically creates albums grouped by:

- **Date** -- monthly groupings
- **Location** -- city/country from GPS data
- **Camera** -- camera make and model
- **Folder** -- source directory

## Image recognition

Optional AI-powered tagging and face detection. Configure from **Gallery > Settings**:

| Backend | Description |
|---------|-------------|
| Ollama | Local or remote Ollama instance with a vision model |
| OpenAI Vision | Any OpenAI-compatible API |

Recognition processes images in the background. Results appear as tags and face clusters that you can name and organize.

## File renaming

Organize photos into folders based on EXIF metadata using Go template patterns:

```
{{`{{.Year}}/{{.Month}}/{{.Day}}/{{.Original}}{{.Ext}}`}}
```

Available fields: `Year`, `Month`, `Day`, `Hour`, `Minute`, `Second`, `Camera`, `Lens`, `Original`, `Ext`, `Place`, `Country`, `Width`, `Height`.

Preview changes before applying. Undo the last rename batch if needed.

## Video support

Requires `ffmpeg` and `ffprobe` on the system. The gallery extracts video thumbnails, duration, resolution, and codec information. Videos play natively in the browser.

## Sharing

Albums and individual photos can be shared with users, groups, or via public links.

## NextCloud Memories API

The gallery implements the [NextCloud Memories](https://memories.gallery/) API for compatibility with the Memories app.

## Search

Photos are indexed for search by filename and location.
