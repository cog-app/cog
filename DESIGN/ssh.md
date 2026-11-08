# SSH Entrypoint

Cog embeds an SSH daemon that provides authenticated users with file transfer (SCP, rsync) and an interactive terminal UI (TUI) built on Bubble Tea v2. Trusted plugins can extend the TUI with custom pages and commands.

## 1. Architecture Overview

The SSH server is a core Host component (not a plugin) — it shares the auth layer, VFS, and service graph with the HTTP server. It listens on a configurable port (default 2222) and is started by the Host alongside the HTTP listener.

```text
┌─────────────────────────────────────────┐
│  Host                                   │
│                                         │
│  ┌──────────┐   ┌──────────┐            │
│  │ HTTP/DAV │   │ SSH      │            │
│  │ :8580    │   │ :2222    │            │
│  └────┬─────┘   └────┬─────┘            │
│       │              │                  │
│       └──────┬───────┘                  │
│              ▼                          │
│        Auth · VFS · Services            │
│              │                          │
│        ┌─────┴──────┐                   │
│        │  Plugins   │                   │
│        └────────────┘                   │
└─────────────────────────────────────────┘
```

**Library:** `github.com/charmbracelet/ssh` (wraps `golang.org/x/crypto/ssh`) with `github.com/charmbracelet/wish` middleware.

## 2. Authentication

### Public Keys

Users manage SSH public keys through the web settings UI or the TUI itself. Keys are stored in the user's per-user database (`user.db`):

```sql
CREATE TABLE ssh_keys (
    id          TEXT PRIMARY KEY,  -- UUID
    name        TEXT NOT NULL,     -- user-assigned label
    public_key  TEXT NOT NULL,     -- OpenSSH authorized_keys format
    fingerprint TEXT NOT NULL,     -- SHA256 fingerprint for display
    created_at  INTEGER NOT NULL   -- unix seconds
);
CREATE UNIQUE INDEX idx_ssh_keys_fingerprint ON ssh_keys(fingerprint);
```

### Password Auth

Password authentication uses the same credential check as HTTP Basic Auth (bcrypt-hashed password in the global users table). This enables SCP/rsync from scripts that cannot use key-based auth. App passwords are also accepted.

### Auth Flow

1. SSH handshake begins.
2. Server checks public key against all users' `ssh_keys` tables (keyed by fingerprint for fast lookup).
3. If no key match, fall back to password auth.
4. On success, the authenticated `*user.User` is attached to the SSH session context.
5. On failure, the connection is rejected.

**Host key:** Auto-generated on first run and stored at `{dataDir}/ssh_host_ed25519_key`. Admin can override via config.

## 3. Session Dispatch

After authentication, the session is dispatched based on the SSH request type:

| Request                                      | Handler                                            |
| -------------------------------------------- | -------------------------------------------------- |
| `exec` with SCP command (`scp -t`, `scp -f`) | SCP subsystem                                      |
| `exec` with rsync command                    | rsync subsystem                                    |
| `exec` with any other command                | Shell command parser                               |
| `subsystem "sftp"`                           | SFTP subsystem (existing, see `file_protocols.md`) |
| `shell` (interactive PTY)                    | TUI application                                    |

All file operations go through the user's VFS, ensuring consistent permissions with WebDAV and other protocols.

## 4. File Transfer

### SCP

Implements the SCP protocol (source/sink modes) by translating to VFS operations. Paths are relative to the user's home directory (e.g., `scp file.txt user@host:Documents/`).

### rsync

Supports the rsync-over-SSH protocol. The server spawns an internal rsync receiver/sender that operates on the VFS. This enables efficient incremental sync without requiring rsync to be installed on the server.

It is gokrazy/rsync's server, run in process on the session's channel (`internal/ssh/rsync.go`). Upstream's receiver writes only through `*os.Root`, so `go.mod` replaces it with a fork (`github.com/duckbrain/rsync`, branch `cog-root-interface`) that writes through a `DestRoot` interface; `rsyncRoot` implements it over the session's VFS. Files are written in place through the files plugin, as SFTP's are, so they are recorded and held to the quota with no staging on the server's disk.

- Paths are relative to `/files`, as for SCP, and cannot climb out of it.
- rsync writes a changed file beside the old one and renames it into place. That is an update, as a WebDAV PUT is (which writes beside and renames too), so it goes through `vfs.Replace`: the file keeps its ID, and with it its shares, comments and favorite. A plain rename over a file (WebDAV MOVE with Overwrite, SFTP rename) still deletes the file there and moves the other to its name.
- Modification times are kept, which is what lets the next run skip unchanged files. Modes and owners are accepted and ignored; symlinks and devices are refused, each reported and the rest of the transfer carried on. `-H` (hard links) is refused outright, as upstream does.
- The server must be built with `rsyncd.DontRestrict()`: otherwise gokrazy/rsync confines the whole process with Landlock to the directories it serves.

### SFTP

Uses the existing SFTP subsystem described in `file_protocols.md`. The SSH daemon routes `subsystem "sftp"` requests to it.

## 5. Shell Interpreter

### Engine

The shell uses `mvdan.cc/sh/v3` — a pure-Go POSIX shell interpreter (the library behind `shfmt`). It provides a complete parser and interpreter with four customization seams that enable full sandboxing:

- **`ExecHandler`** — intercepts all command execution; we never call `DefaultExecHandler`, so no real binaries can run.
- **`OpenHandler`** — intercepts all file opens (redirects like `>`, `>>`, `<`); enforces VFS containment.
- **`ReadDirHandler`** — intercepts directory reads for glob expansion (`*.txt`); backed by VFS.
- **`StatHandler`** — intercepts file stat for `test -f`, `cd` validation; backed by VFS.

### Sandboxing Model

ExecHandler is terminal — it dispatches to built-in commands and plugin commands but never delegates to the OS. OpenHandler, StatHandler, and ReadDirHandler enforce path containment within the user's files directory.

### Path Model

Internally uses real filesystem paths rooted at the user's files dir. Virtual paths (`/Documents/foo.txt`) are displayed to users in prompts and `pwd` output. Containment is enforced by verifying all resolved paths (including symlink targets) stay within the files root.

### Supported Shell Features

Users get full POSIX sh syntax for free:

- Pipes (`|`), semicolons (`;`), `&&`, `||`
- Redirects (`>`, `>>`, `<`, `2>`, `2>&1`, here-docs `<<EOF`)
- Variables (`$FOO`, `${FOO:-default}`, `FOO=bar cmd`)
- Quoting (single, double, backslash, `$'...'`)
- Globbing (`*.txt`, `**/*.go`)
- For/while/until loops, if/elif/else, case statements
- Functions, command substitution (`$(cmd)`), arithmetic (`$((1+2))`)
- `set -e`, `set -x`, `pipefail`

### Blocked/Absent

No real binary execution, no `exec` builtin replacement, no network commands. Unknown commands return exit code 127.

### File Mutations

Mutating commands (`cp`, `mv`, `rm`, `mkdir`) route through the files plugin API for immediate metadata updates. File redirects (`>`, `>>`) write to the real filesystem and rely on lazy reconciliation (same as SCP/SFTP).

### Plugin Commands

Dispatched via ExecHandler when `args[0]` matches a plugin ID. Namespaced as `<plugin> <subcommand> [args...]`.

### Non-Interactive Exec

`ssh user@host 'ls | grep foo'` parses and runs through the same interpreter, just with plain I/O instead of the TUI.

### Environment Variables

Initial environment for each session:

| Variable | Value |
| -------- | ----- |
| `HOME`   | `/`   |
| `USER`   | `<username>` |
| `SHELL`  | `cog` |
| `PWD`    | `/` (managed by interpreter, virtual path) |
| `PATH`   | empty (no OS path search) |

`EDITOR` and `PAGER` are not set (no OS binaries to invoke).

### Package Layout

```
internal/ssh/shell/
    shell.go      — Shell type, NewRunner, RunLine
    exec.go       — ExecHandler: dispatches to builtins + plugin commands
    handlers.go   — OpenHandler, StatHandler, ReadDirHandler (containment)
    builtins.go   — Built-in command implementations
    resolve.go    — Virtual ↔ real path mapping, containment checks
    env.go        — Initial environment variables

cog_shell.go          — Cobra command for `cog shell` (OpenSSH login shell entry point)
```

## 6. OpenSSH Integration (`cog shell`)

The embedded SSH daemon (sections 1–5) is optional. Admins who prefer to use the host's existing OpenSSH daemon can instead use the `cog shell` subcommand as a login shell, getting the same shell interpreter without running a second SSH server.

### How It Works

1. Sysadmin creates a system user (e.g., `cog`) with login shell `/usr/bin/cog shell`.
2. The system user's `~/.ssh/authorized_keys` maps public keys to cog users via `command=` directives:
   ```
   command="/usr/bin/cog shell --user alice" ssh-ed25519 AAAA... alice@laptop
   command="/usr/bin/cog shell --user bob" ssh-ed25519 AAAA... bob@desktop
   ```
3. OpenSSH authenticates the key, then executes `cog shell --user alice`.
4. `cog shell` opens providers (no HTTP server), looks up the cog user, and runs the shell interpreter on stdin/stdout/stderr.

### CLI Interface

```
cog shell [--user <username>] [-c <command>]
```

- `--user` — required, identifies the cog user to run as.
- `-c <command>` — non-interactive mode. OpenSSH passes this automatically when the client runs `ssh cog@host 'ls /Documents'`.
- No `-c` flag → interactive mode (reads stdin line-by-line, writes stdout/stderr).

This follows the standard login shell protocol: when OpenSSH needs to run a remote command, it invokes `<login-shell> -c '<command>'`.

### Shared Interpreter

`cog shell` reuses the same `internal/ssh/shell/` package as the embedded SSH daemon. Both paths create a shell runner with the user's VFS and plugin commands — the only difference is the I/O source (host OpenSSH PTY vs. embedded SSH PTY).

### Non-Interactive Exec

When invoked with `-c`, the command string is parsed and executed through the shell interpreter, then the process exits with the command's exit code. This enables scripting:

```bash
ssh cog@host 'ls /Documents'
ssh cog@host 'cat /Documents/notes.txt | grep TODO'
echo "hello" | ssh cog@host 'cat > /Documents/hello.txt'
```

### SCP/SFTP Note

`cog shell` handles the shell interpreter only. SCP and SFTP through OpenSSH would require additional `ForceCommand` or subsystem configuration — this is out of scope for the shell subcommand.

### Implementation

The Cobra command is defined in `cog_shell.go` (alongside `cog_admin.go` and `cog_serve.go`) and registered in `Main()`:

```go
root.AddCommand(shellCmd(plugins))
```

The command opens providers via `openProviders()`, looks up the user by username, constructs a shell runner from `internal/ssh/shell/`, and connects it to `os.Stdin`/`os.Stdout`/`os.Stderr`.

## 7. Interactive TUI

When a user opens an interactive shell (`ssh user@host`), they enter a Bubble Tea v2 application. The TUI provides a command-line shell with a prompt and a set of pages for visual interaction.

### Framework

- **Bubble Tea v2** (`github.com/charmbracelet/bubbletea/v2`): Core TUI framework.
- **Lip Gloss**: Styling and layout.
- **Bubbles**: Standard input components (text input, lists, tables, viewports).

### Shell Mode

The default mode is a command-line shell with a prompt:

```
user@cog:~$ _
```

The shell supports:

- Command history (up/down arrows, stored in-memory per session).
- Tab completion for commands and file paths.
- Ctrl+C to cancel current input, Ctrl+D to disconnect.
- `help` to list available commands.

### Built-in Commands (POSIX-like)

File manipulation commands that operate on the user's VFS. Paths are relative to the current working directory within the VFS.

| Command                       | Description                                         |
| ----------------------------- | --------------------------------------------------- |
| `ls [path]`                   | List directory contents (supports `-l`, `-a`, `-h`) |
| `cd [path]`                   | Change working directory                            |
| `pwd`                         | Print working directory                             |
| `cat <file>`                  | Print file contents                                 |
| `head [-n N] <file>`          | Print first N lines (default 10)                    |
| `tail [-n N] <file>`          | Print last N lines (default 10)                     |
| `cp <src> <dst>`              | Copy file or directory                              |
| `mv <src> <dst>`              | Move/rename file or directory                       |
| `rm <path>`                   | Remove file (`-r` for directories)                  |
| `mkdir [-p] <path>`           | Create directory                                    |
| `touch <file>`                | Create empty file or update mtime                   |
| `du [-h] [path]`              | Disk usage summary                                  |
| `df [-h]`                     | Total disk usage summary                            |
| `find <path> -name <pattern>` | Find files by name pattern                          |
| `tree [path]`                 | Display directory tree                              |
| `stat <path>`                 | Show file metadata                                  |
| `grep <pattern> [file...]`    | Search for pattern (reads stdin if no file)          |
| `wc [-lwc] [file...]`         | Count lines/words/chars                              |
| `sort [-r] [-n] [file...]`    | Sort lines                                           |
| `uniq [-c] [file...]`         | Deduplicate adjacent lines                           |
| `tee <file>`                  | Copy stdin to stdout and file                        |
| `xargs <cmd>`                 | Build and execute commands from stdin                |

### Built-in Utility Commands

| Command          | Description                          |
| ---------------- | ------------------------------------ |
| `help [command]` | Show help for commands               |
| `whoami`         | Print current username               |
| `clear`          | Clear terminal                       |
| `exit` / `quit`  | Disconnect                           |
| `keys`           | List/add/remove SSH public keys      |
| `<page>`         | Switch to a TUI page (e.g., `files`) |

### TUI Pages

Full-screen interactive views that take over the terminal. Users enter pages via the `open` command or directly via `ssh user@host <page>`. Press `q` or `Esc` to return to the shell.

**Built-in pages:**

- **files** — File browser with navigation, preview, upload marker, delete.
- **calendar** — Month/week/day views with event detail pane.
- **tasks** — Task lists with checkbox toggling and quick-add.
- **news** — RSS feed reader with article list and reading pane.

Each page is a self-contained Bubble Tea `Model` that receives the full terminal dimensions and the authenticated user context.

## 8. Plugin TUI Extensions

Trusted plugins (and above) can register TUI components during `Register()`. Two extension points are provided:

### TUI Pages

```go
// RegisterTUIPage registers a full-screen TUI page accessible via
// "open <name>" in the shell or "ssh user@host <name>".
RegisterTUIPage(page TUIPageDef)
```

```go
type TUIPageDef struct {
    Name  string // command name, e.g. "contacts"
    Short string // one-line description for help
    // New creates a fresh page model for a session.
    // Receives the terminal size and user context.
    New func(ctx TUIContext) tea.Model
}

type TUIContext struct {
    User     UserInfo
    Width    int
    Height   int
    PluginDB *sql.DB // plugin's per-user database
    Logger   *slog.Logger
}
```

### Shell Commands

```go
// RegisterTUICommand registers a command available in the SSH shell.
// Commands run inline in the shell (not full-screen).
RegisterTUICommand(cmd TUICommandDef)
```

```go
type TUICommandDef struct {
    Name  string   // e.g. "bookmark"
    Short string   // one-line description
    Args  []string // positional arg names for usage
    // Run executes the command with parsed arguments.
    // Stdin/stdout/stderr support pipes and redirects.
    Run func(ctx TUIContext, args []string, stdin io.Reader, stdout, stderr io.Writer) error
    Sub []TUICommandDef // sub-commands (mutually exclusive with Run)
}
```

**Namespacing:** Plugin commands are registered under their plugin ID as a prefix to avoid collisions. For example, the contacts plugin registering a `search` command would be accessible as `contacts search <query>`. The `open` command similarly uses `open contacts`. Built-in commands (file commands, `help`, etc.) have no prefix.

**Security:** Only trusted and above plugins may register TUI extensions. External plugins cannot — they have no trusted code path into the TUI process.

## 9. SSH Key Management

### Web UI

Users manage keys at `/settings/keys`:

- Add key: paste public key, assign a label.
- List keys: shows label, fingerprint, added date.
- Remove key: delete by ID with confirmation.

### TUI

The `keys` command provides the same functionality:

- `keys` / `keys list` — list registered keys.
- `keys add <name>` — interactive prompt to paste a public key.
- `keys rm <name|fingerprint>` — remove a key.

### Admin

Admins can view/manage keys for any user via `/admin/users/{id}/keys` and (if admin) `keys --user <username>` in the TUI.

## 10. Configuration

```go
type SSHConfig struct {
    Enabled     bool   // default true
    Listen      string // default ":2222"
    HostKeyPath string // default "{dataDir}/ssh_host_ed25519_key"
}
```

The SSH daemon is enabled by default. Set `Enabled: false` to disable. The host key is auto-generated if it does not exist.

## 11. Security Considerations

- **Host key persistence:** The host key must be stable across restarts to avoid TOFU warnings. It is stored on disk, not generated ephemerally.
- **Rate limiting:** Failed auth attempts are rate-limited per IP (same mechanism as HTTP).
- **Key algorithm support:** Ed25519, ECDSA, RSA (4096+ bit). DSA is not accepted.
- **No shell escape:** The TUI does not provide access to a system shell. Users cannot execute arbitrary OS commands. The `exec` path only recognizes SCP, rsync, and registered commands.
- **Session limits:** Configurable max concurrent sessions per user (default 5).
- **Idle timeout:** Sessions are disconnected after configurable inactivity (default 30 minutes).
- **Audit:** All SSH sessions and file operations are logged with user, source IP, and timestamp.
