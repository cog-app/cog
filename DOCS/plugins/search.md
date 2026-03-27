# Search Indexing

Plugins can make their data searchable by registering a search source. Cog maintains a per-user Bleve full-text index that is updated asynchronously.

## Registering a search source

```go
ctx.RegisterSearchSource(plugin.SearchSourceDef{
    PrimaryTable: "notes",
    EnrichSQL: `SELECT rowid, title, notebook_name, body_text
                FROM notes
                LEFT JOIN notebooks ON notebooks.id = notes.notebook_id
                WHERE rowid IN (?)`,
})
```

## SearchSourceDef

```go
type SearchSourceDef struct {
    PrimaryTable string
    EnrichSQL    string
    ContentFunc  func(filesDir string, cols map[string]string) string
}
```

### PrimaryTable

The SQLite table to watch for changes. Cog uses SQLite update hooks to detect inserts, updates, and deletes on this table and triggers re-indexing automatically.

### EnrichSQL

A SELECT query that fetches the data to index. Requirements:

- Must return `rowid` as the first column
- Must contain `WHERE rowid IN (?)` as a placeholder
- Remaining columns are mapped by position:
  - **Column 1** (after rowid) → `name` -- the primary display text in search results
  - **Column 2** → `subtext` -- secondary display text
  - **Remaining columns** → `content` -- concatenated for full-text indexing

### ContentFunc (optional)

For internal plugins that need to extract file content (e.g. the Files plugin reading text from files on disk). Receives the user's files directory path and all SQL column values as a map.

Return `""` to skip content indexing for a row.

## How indexing works

1. Cog installs SQLite update hooks on the `PrimaryTable` in each user's plugin database
2. When a row is inserted, updated, or deleted, the change is queued
3. The search manager batches changes and runs `EnrichSQL` to fetch the current data
4. Data is indexed into the user's Bleve index
5. Deletions are automatically handled -- if `EnrichSQL` returns no rows for a rowid, the document is removed from the index

Indexing is **eventually consistent** -- there is a brief delay between a change and its appearance in search results.

## Example: Bookmarks plugin

```go
ctx.RegisterSearchSource(plugin.SearchSourceDef{
    PrimaryTable: "bookmarks",
    EnrichSQL: `SELECT b.rowid, b.title, b.url, b.description || ' ' || b.text_content
                FROM bookmarks b
                WHERE b.rowid IN (?)`,
})
```

This indexes bookmarks with the title as the display name, URL as subtext, and description + page content as searchable text.
