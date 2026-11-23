# CLI Reference

Cog provides a command-line interface for server management. Running `cog` without arguments starts the HTTP server.

## Commands

### `cog serve`

Start the HTTP and SSH servers.

```bash
cog serve
```

This is also the default when running `cog` with no subcommand.

### `cog user`

Manage user accounts.

```bash
cog user create <username>           # Create a user (prompts for password)
cog user create --admin <username>   # Create an admin user
cog user list                        # List all users
cog user delete <username>           # Delete a user
cog user set-admin <username>        # Grant admin privileges
cog user set-password <username>     # Reset a user's password
```

### `cog group`

Manage groups.

```bash
cog group create <name>                      # Create a group
cog group list                               # List all groups
cog group add-member <group> <username>      # Add a user to a group
cog group remove-member <group> <username>   # Remove a user from a group
```

### `cog volume`

Manage storage volumes.

```bash
cog volume create <name> <uri>       # Create a volume (e.g. file:///mnt/storage)
cog volume list                      # List all volumes
cog volume mount <volume> <user> <path>  # Mount a volume in a user's VFS
```

### `cog setting`

View and modify system settings.

```bash
cog setting list                     # List all settings
cog setting get <key>                # Get a setting value
cog setting set <key> <value>        # Set a setting value
```

### `cog user find-key`

Print the username that owns an SSH public key, given its SHA256 fingerprint
(the `%f` token in sshd's `AuthorizedKeysCommand`). Exits non-zero if no user
has the key, or if more than one user added it.

```bash
cog user find-key SHA256:uQ8hD3...
```

### `cog shell`

Start an interactive shell session (same shell available via SSH).

```bash
cog shell <username>
```

### `cog doctor`

Run diagnostic checks on the server and its dependencies.

```bash
cog doctor
```

Reports the status of optional external tools (ffmpeg, etc.) and database integrity. Plugins can contribute their own health checks.

### `cog setup`

Installs Cog as a service, or changes, resets or removes an install. In the
browser when there is a desktop, else at the terminal. See
[Installation](../getting-started/installation.md).

```bash
cog setup          # browser with a desktop, terminal without
cog setup --cli    # always at the terminal
```

### `cog update`

Installs the newest release over the running program and restarts the
service. See [Updates](updates.md).

```bash
cog update --check
cog update
```

### `cog version`

Prints the version.

### `cog docs`

Builds this documentation as a static website, the same pages Cog serves
at `/docs/`, with a search that runs in the browser. `--out <folder>` (default
`public`) is replaced with the site; `--serve localhost:8080` then serves it.

Every command takes `--config <file>` to read a configuration file other
than the one beside the installed program.

## Plugin commands

Plugins can register CLI commands that appear as subcommands under their plugin ID:

```bash
cog <plugin-id> <command> [args...]
```

For example, a bookmarks plugin registering a `scan` command would be invoked as:

```bash
cog bookmarks scan
```
