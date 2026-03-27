# Configuration

Cog is configured via environment variables. All settings have sensible defaults for local development.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `COG_ADDR` | `:8580` | HTTP listen address |
| `COG_DB` | `system.db` | Path to the system SQLite database |
| `COG_DATA_DIR` | `data` | Root directory for user data |
| `COG_BASE_URL` | `http://<hostname>:8580` | Public URL used for generating links and login flows |
| `COG_LOG_FORMAT` | `text` | Log format: `text` or `json` |
| `COG_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `COG_SSH_ADDR` | `:8522` | SSH/SFTP listen address |
| `COG_SSH_ENABLED` | `true` | Enable the SSH/SFTP server |
| `COG_SSH_HOST_KEY` | (auto-generated) | Path to SSH host key file |

## Directory layout

Cog organizes data under the `COG_DATA_DIR` directory:

```
data/
├── alice/                  # Per-user directory
│   ├── user.db             # User settings and auth state
│   ├── files/              # User's home files (WebDAV root)
│   │   ├── Documents/
│   │   └── Photos/
│   └── app_data/           # Plugin data
│       ├── files/
│       │   └── plugin.db   # File metadata index
│       ├── calendar/
│       │   └── plugin.db   # Calendar data
│       └── notes/
│           └── plugin.db   # Notes data
└── bob/
    ├── user.db
    ├── files/
    └── app_data/
```

Key points:

- Each user gets an isolated directory with their own SQLite databases.
- Files are stored flat on disk with their original names. You can copy files directly into a user's `files/` directory and Cog will detect them automatically.
- Plugin data is isolated per-user in `app_data/<plugin_id>/`.

## System database

The `system.db` (path set by `COG_DB`) stores global state: users, sessions, groups, shares, and plugin settings. This file is separate from user data.

## Production deployment

For production, you should set at minimum:

```bash
export COG_BASE_URL="https://cloud.example.com"
export COG_DB="/var/lib/cog/system.db"
export COG_DATA_DIR="/var/lib/cog/data"
```

See [Reverse Proxy](../admin/reverse-proxy.md) for putting Cog behind nginx or Caddy.
