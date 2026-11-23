# Configuration

Cog reads its settings from a configuration file, which setup writes, and
from environment variables, which override the file. Everything has a
default that suits trying Cog out in a folder.

## The configuration file

Setup writes `cog.toml` in the folder it installs to. Cog finds it:

1. from `--config <file>` on the command line (the service passes this);
2. else from `COG_CONFIG`;
3. else beside an installed program: `<folder>/cog.toml` for
   `<folder>/bin/cog`, so admin commands run from the folder need nothing.

Without one, Cog uses the defaults and the environment, as before.

```toml
addr = "127.0.0.1:8580"
base_url = "https://cloud.example.com"
db_path = "system.db"          # relative paths are from this file's folder
data_dir = "data"
ssh_enabled = true
ssh_addr = ":8522"
ssh_no_account_passwords = true

[tls]                          # see Administration > Reaching Cog from Anywhere
mode = "acme"
addr = ":8443"
domains = ["cloud.example.com"]

[update]                       # see Administration > Updates
check = true
auto = false

[install]                      # written by setup: how the service runs
service = "user"
dir = "/home/me/.local/share/cog"
```

A setting Cog does not know is an error, so a misspelling does not go
unnoticed. Edit the file and restart Cog, or run setup again, which keeps
settings it does not ask about. The file is readable only by its owner: it
can hold DNS credentials.

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
| `COG_SSH_NO_ACCOUNT_PASSWORDS` | `false` | SSH takes keys and app passwords only |
| `COG_CONFIG` | | Configuration file |
| `COG_TLS_MODE` | | `acme`, `files` or `self-signed` to serve HTTPS |
| `COG_TLS_ADDR` | `:8443` | HTTPS listen address |
| `COG_TLS_HTTP_ADDR` | | Plain HTTP listener for http-01 and redirects |
| `COG_TLS_DOMAINS` | | Names for the certificate, comma-separated |
| `COG_TLS_EMAIL` | | Email for the ACME account |
| `COG_TLS_CHALLENGE` | `tls-alpn-01` | `tls-alpn-01`, `http-01` or `dns-01` |
| `COG_TLS_DNS_PROVIDER` | | DNS provider for dns-01; its credentials use lego's variables (`CLOUDFLARE_DNS_API_TOKEN`, `DUCKDNS_TOKEN`, ...) |
| `COG_TLS_CA` | Let's Encrypt | ACME directory URL, or `staging` |
| `COG_TLS_CERT`, `COG_TLS_KEY` | | Certificate files for `files` mode |
| `COG_UPDATE_CHECK` | `true` | Check for releases daily |
| `COG_UPDATE_AUTO` | `false` | Install releases when found |

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

`cog setup` is the simplest route. Without it, set at minimum:

```bash
export COG_BASE_URL="https://cloud.example.com"
export COG_DB="/var/lib/cog/system.db"
export COG_DATA_DIR="/var/lib/cog/data"
```

See [Reverse Proxy](../admin/reverse-proxy.md) for putting Cog behind nginx or Caddy.
