# SFTP

Cog includes a built-in SSH/SFTP server for secure file transfers.

## Connection

```bash
sftp -P 8522 alice@cloud.example.com
```

Default port is `8522` (configurable via `COG_SSH_ADDR`).

## Authentication

- **Password** -- your Cog password or an app password
- **SSH keys** -- manage keys from **Settings > Security** in the web UI or via the SSH shell `keys` command

## SCP

```bash
# Upload
scp -P 8522 file.txt alice@cloud.example.com:Documents/

# Download
scp -P 8522 alice@cloud.example.com:Documents/file.txt .
```

## rsync

```bash
rsync -e "ssh -p 8522" -avz ./photos/ alice@cloud.example.com:Photos/
```

## SSH Shell

Connecting via SSH opens an interactive shell:

```bash
ssh -p 8522 alice@cloud.example.com
```

The shell provides built-in commands for browsing files, managing tasks, reading RSS feeds, and more. Plugins can register their own shell commands. Run `help` in the shell for available commands.

The same shell is also available locally via:

```bash
cog shell alice
```

## Disabling SSH

Set `COG_SSH_ENABLED=false` to disable the SSH/SFTP server entirely.
