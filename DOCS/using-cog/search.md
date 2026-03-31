# Search

Cog provides full-text search across files and plugin data using a [Bleve](https://blevesearch.com/) index.

## Using search

Use the search bar in the navigation to search across all your data. Results include:

- **Files** -- matched by filename and text content
- **Notes** -- matched by title and body text
- **Bookmarks** -- matched by title, URL, description, and tags
- **Calendar events** -- matched by summary, description, and location
- **Tasks** -- matched by summary and description
- **RSS articles** -- matched by title and content
- **Contacts** -- matched by name, email, and phone number
- **Media episodes** -- matched by title, type, and description

Results link directly to the matched item in its app.

## Query syntax

| Syntax | Example | Description |
|--------|---------|-------------|
| `term` | `alice` | Matches any field containing the term |
| `"phrase"` | `"hello world"` | Matches the exact phrase |
| `NOT` / `!` | `NOT alice` | Excludes matches |
| `AND` | `alice AND bob` | Both terms must match (implicit when space-separated) |
| `OR` | `alice OR bob` | Either term matches |
| `()` | `(alice OR bob) AND files` | Group expressions |
| `:field=value` | `:source=files` | Field equals value |
| `:field="value"` | `:source="my notes"` | Field equals quoted value |
| `:field IN (...)` | `:source IN (files, notes)` | Field matches any value in set |

### Operator precedence

From lowest to highest: `OR`, `AND` (implicit between adjacent terms), `NOT`/`!`.

### Available fields

- **source** -- the plugin that indexed the item (e.g. `files`, `notes`, `rss`, `bookmarks`, `calendar`, `tasks`, `contacts`, `media`)
- **name** -- the primary display name
- **subtext** -- secondary display text

### Examples

```
alice                         # simple text search
"Alice Wonderland"            # exact phrase
alice AND :source=users       # text + field filter
:source IN (files, notes)     # multiple sources
NOT :source=rss               # exclude RSS results
(alice OR bob) AND :source=users  # grouped OR with field filter
```

## How it works

Each plugin registers a search source that tells Cog which database tables to index and how to map columns to searchable fields. Cog maintains a per-user Bleve index that is updated asynchronously when data changes.

### Eventual consistency

Search indexing is asynchronous. After creating or modifying content, there may be a brief delay before changes appear in search results. The index catches up automatically.

## Admin search

Admins have access to a system-wide search at **Admin > Search** that can search across all users.
