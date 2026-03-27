# Plugin Databases

Cog provides SQLite databases for plugin data storage. Each plugin gets isolated databases that are automatically created and managed.

## Per-user databases

Each plugin gets a separate SQLite database per user, stored at:

```
{data_dir}/{username}/app_data/{plugin_id}/plugin.db
```

Access the database during request handling:

```go
// From a request handler (uses the authenticated user)
db, err := plugin.UserDB(r)

// From Register() or background work (explicit username)
db, err := ctx.PluginDB("alice")
```

Databases are opened in WAL mode with foreign keys enabled.

## Root database

For plugin-wide state shared across all users (e.g. feed poll schedules, global configuration), use the root database:

```go
db, err := ctx.RootDB()
```

Stored at `app_data/{plugin_id}/root.db`.

## Schema migrations

Use `plugin.Migrate()` to manage your database schema. Create an embedded filesystem with SQL files:

```
sql/
  schema.sql           # Full current schema (CREATE TABLE IF NOT EXISTS)
  migration001.sql     # First migration
  migration002.sql     # Second migration
```

Embed and run:

```go
//go:embed sql
var sqlFS embed.FS

// In Register() or first use:
db, err := ctx.PluginDB(username)
if err != nil {
    return err
}
if err := plugin.Migrate(db, sqlFS); err != nil {
    return err
}
```

### schema.sql

Contains the full current schema using `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`. This is the canonical schema definition.

### Migration files

Named `migration*.sql` and run in order after the schema. For adding columns or modifying existing tables.

### Guard comments

Migration files support conditional execution:

```sql
-- IF COLUMN greetings.priority NOT EXIST
ALTER TABLE greetings ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
```

The migration only runs if the specified column does not exist, preventing errors on repeated execution.

## Best practices

- Use `plugin.Migrate()` for all schema management -- don't run raw DDL
- Keep write transactions small and bounded
- Use prepared statements for repeated queries
- Remember that each user has a separate database -- there are no cross-user joins
- For cross-user data, use the root database or the share system
