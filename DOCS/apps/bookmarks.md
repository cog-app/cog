# Bookmarks

The Bookmarks app provides bookmark management with folders, tags, and NextCloud Bookmarks API compatibility.

## Features

- **Folders** -- organize bookmarks in a hierarchical folder tree
- **Tags** -- label bookmarks for filtering
- **Search** -- find bookmarks by title, URL, description, or tag
- **Import/Export** -- Netscape HTML bookmark format
- **Favicon fetching** -- automatic favicon retrieval
- **Availability checking** -- periodic checks for broken links
- **Click tracking** -- track how often you visit each bookmark

## Adding bookmarks

Add bookmarks from the web UI or via the NextCloud Bookmarks API. Bookmarks can exist in multiple folders simultaneously.

## Import/Export

Import bookmarks from browsers or other services using the standard Netscape HTML format. The folder hierarchy is preserved during import.

Export your bookmarks in the same format for backup or migration.

## NextCloud Bookmarks API

The Bookmarks app implements the NextCloud Bookmarks REST API v2 (`/index.php/apps/bookmarks/public/rest/v2/`) for compatibility with:

- NextCloud Bookmarks browser extensions
- Floccus (browser bookmark sync)
- Third-party bookmark managers

The API supports bookmarks, folders, tags, hash-based sync, and import/export.

## Sharing

Bookmark folders can be shared with users or groups:

- **Read** -- view bookmarks in the folder
- **Write** -- add, edit, and delete bookmarks in the folder
- **Reshare** -- manage sharing

Public links create a read-only view of the folder's bookmarks.

## Search

Bookmarks are indexed for full-text search. Search matches against title, URL, description, page content, and tags.
