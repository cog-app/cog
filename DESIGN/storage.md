# Storage Design

## Filesystem Architecture

Cog uses a virtual filesystem abstraction inspired by `hackpadfs`. This allows seamless integration of various storage backends while presenting a unified hierarchy to the user.

### 1. The Virtual File System (VFS)
The root of the VFS is a composition of mount points.
- **Root:** `/`
- **User Home:** `/users/{username}/`
- **Shared:** `/shared/`

### 2. Backends
The `fs.FS` interface will be implemented by drivers for:
- **Local Disk:** Direct mapping to the OS filesystem.
- **S3 / Object Storage:** Translating object keys to file paths.
- **Google Drive:** API wrapper.

### 3. Flat File Storage
Files are stored "flat" on the backing storage to ensure portability and ease of administration.
- **Path Mapping:** A logical path in Cog (e.g., `/users/alice/Documents/resume.pdf`) maps directly to a physical path (e.g., `/var/lib/cog/data/alice/Documents/resume.pdf`) or an object key.
- **No Rename/Move Opaque IDs:** Unlike systems that rename files to hashes (content-addressable), Cog preserves filenames. This allows admins to `cp` files directly into a user's folder, and Cog will detect them.

### 4. Metadata Strategy
Metadata (permissions, tags, sharing status, custom attributes) is decoupled from the file content but linked via path.
- **Lazy Synchronization:** When a directory is listed via Cog, the system compares the VFS state with the SQLite metadata.
  - *New File on Disk:* Entry created in SQLite.
  - *Missing File on Disk:* Entry marked missing or removed from SQLite.
- **Stat Cache:** SQLite acts as a hot cache for `stat` operations to reduce latency on slow backends (like S3).

## Directory Structure

```text
/var/lib/cog/
├── config/              # Global configuration
├── system/              # System-wide databases/state
├── plugins/             # External plugin binaries
└── data/                # User data root
    ├── alice/           # User 'alice' root
    │   ├── user.db      # Main user database (Metadata, Core Apps)
    │   ├── files/       # Main GUI files (Visible to user)
    │   │   ├── Documents/
    │   │   └── Photos/
    │   └── app_data/    # Plugin sandboxes
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
1.  **`user.db`**: The primary SQLite database. Contains file metadata for the `files/` directory, sharing records, and core plugin data (Calendar, Contacts).
2.  **`files/`**: The "Home" directory for the user. This is what they see when they log into the web UI or connect via WebDAV.
3.  **`app_data/`**: Isolated storage for plugins. Each plugin gets a subdirectory named after its ID.
    - **`plugin.db`**: An independent SQLite database for the plugin's private state.
    - **Filesystem Sandbox**: Any additional files the plugin needs to store (e.g., thumbnails, local caches).
