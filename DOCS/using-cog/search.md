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

Results link directly to the matched item in its app.

## How it works

Each plugin registers a search source that tells Cog which database tables to index and how to map columns to searchable fields. Cog maintains a per-user Bleve index that is updated asynchronously when data changes.

### Eventual consistency

Search indexing is asynchronous. After creating or modifying content, there may be a brief delay before changes appear in search results. The index catches up automatically.

## Admin search

Admins have access to a system-wide search at **Admin > Search** that can search across all users.
