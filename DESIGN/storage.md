# Storage Design

## Filesystem Architecture

Cog uses a virtual filesystem abstraction inspired by `hackpadfs`. This allows seamless integration of various storage backends while presenting a unified hierarchy to the user.

### 1. The Virtual File System (VFS) — Volumes & Mounts

The VFS is built on the **volume/mount system**. A `volume` represents a storage backend, and a `mount` binds a volume (or subpath) to a location in a user's virtual filesystem.

- Each user has a **root mount** (`/`) pointing to their default volume, created automatically during user provisioning.
- Additional mounts can attach remote or shared storage at arbitrary paths within the user's tree.
- The `volume.Backend` interface is the filesystem abstraction. Backends are selected by the volume's URI scheme.

### 2. Backends

The `volume.Backend` interface is implemented by drivers for:

- **`file://`** — Local disk. Direct mapping to the OS filesystem.
- **`webdav://`** — Remote WebDAV server.
- **`mem://`** — In-memory (used for testing).

Future backends (S3, Google Drive, SFTP, SMB, FTP) will implement the same interface.

### 3. Flat File Storage

Files are stored "flat" on the backing storage to ensure portability and ease of administration.

- **Path Mapping:** A logical path in Cog (e.g., `/users/alice/Documents/resume.pdf`) maps directly to a physical path (e.g., `/var/lib/cog/data/alice/Documents/resume.pdf`) or an object key.
- **No Rename/Move Opaque IDs:** Unlike systems that rename files to hashes (content-addressable), Cog preserves filenames. This allows admins to `cp` files directly into a user's folder, and Cog will detect them.

### 4. Metadata Strategy

Metadata (permissions, tags, sharing status, custom attributes) is decoupled from the file content.

- **Stable Identity:** Files have stable IDs in the `files` plugin metadata. Paths are mutable attributes so shares survive rename/move operations.

- **Lazy Synchronization:** When a directory is listed via Cog, the system compares the VFS state with the SQLite metadata.
  - _New File on Disk:_ Entry created in SQLite.
  - _Missing File on Disk:_ Entry marked missing or removed from SQLite.
- **Stat Cache:** SQLite acts as a hot cache for `stat` operations to reduce latency on slow backends (like S3).

## Directory Structure

```text
/var/lib/cog/
├── config/              # Global configuration
├── system/              # System-wide databases/state
├── plugins/             # External plugin binaries
└── data/                # User data root
    ├── alice/           # User 'alice' root
    │   ├── user.db      # User-scoped state (settings, auth artifacts)
    │   ├── files/       # Managed by the 'files' plugin
    │   │   ├── Documents/
    │   │   └── Photos/
    │   └── app_data/    # Plugin sandboxes
    │       ├── files/   # 'files' plugin data
    │       │   └── plugin.db
    │       ├── rss/     # 'rss' plugin sandbox
    │       │   ├── plugin.db
    │       │   └── cache/
    │       └── notes/   # 'notes' plugin sandbox
    │           └── plugin.db
    └── bob/
        ├── user.db
        ├── files/
        └── app_data/
```

### User Storage Components

1.  **`user.db`**: User-scoped SQLite state (for example settings and protocol auth artifacts). It does not contain global sharing state.
2.  **`files/`**: The "Home" directory for the user. This is what they see when they log into the web UI or connect via WebDAV.
3.  **`app_data/`**: Isolated storage for plugins. Each plugin gets a subdirectory named after its ID.
    - **`plugin.db`**: An independent SQLite database for the plugin's private state.
    - **Filesystem Sandbox**: Any additional files the plugin needs to store (e.g., thumbnails, local caches).
4.  **Global `shares` DB**: Stored under `/var/lib/cog/system/` and used for cross-user/group sharing lookups.
