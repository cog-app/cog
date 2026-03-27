# Backup and Restore

## What to back up

1. **System database** -- the file at `COG_DB` (default `system.db`). Contains users, sessions, groups, shares, and plugin settings.
2. **Data directory** -- the entire `COG_DATA_DIR` (default `data/`). Contains per-user files, databases, and plugin data.

These two items contain all Cog state. Back up both to fully restore a Cog instance.

## SQLite considerations

Cog uses SQLite in WAL (Write-Ahead Logging) mode. Each `.db` file may have associated `-wal` and `-shm` files.

**Recommended approach:** Stop Cog before backing up to ensure clean database state. If you need hot backups, copy the `.db`, `-wal`, and `-shm` files together atomically (e.g., using filesystem snapshots or `sqlite3 .backup`).

Do not copy only the `.db` file while Cog is running -- you may get a corrupt backup if there are uncommitted WAL entries.

## Backup script example

```bash
#!/bin/bash
# Stop cog, back up, restart
systemctl stop cog
tar czf /backup/cog-$(date +%Y%m%d).tar.gz \
    /var/lib/cog/system.db* \
    /var/lib/cog/data/
systemctl start cog
```

## Restore

1. Stop Cog
2. Replace `system.db` and `data/` with the backup contents
3. Start Cog

Cog will run any pending migrations automatically on startup.
