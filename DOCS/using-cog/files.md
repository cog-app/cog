# Files

The Files app is the core of Cog. It provides a web-based file browser for uploading, downloading, organizing, and sharing files.

## File browser

Access your files at `/apps/files/` after logging in. The browser supports:

- **Upload** -- drag and drop files or use the upload button. Large files are uploaded in chunks automatically.
- **Download** -- click a file to download it, or select multiple files.
- **Create folders** -- use the "New Folder" button.
- **Rename** -- click the file name to rename.
- **Move** -- drag files between folders.
- **Delete** -- select files and delete. Deleted files go to the trash.

## Favorites

Mark files or folders as favorites for quick access. Favorites are available from the sidebar under "Favorites".

## Flat storage

Files are stored on disk with their original names. Admins can copy files directly into a user's `files/` directory on the server and Cog will detect them automatically on the next directory listing. No import scan is required.

## Volumes and mounts

Your file tree can include mounted volumes -- external storage backends that appear as folders. See [Volumes](../admin/volumes.md) for admin setup and **Settings > Storage** to mount volumes in your own file tree.

## Access methods

Files are accessible through multiple protocols:

- **Web UI** -- `/apps/files/`
- **WebDAV** -- see [WebDAV](webdav.md)
- **SFTP** -- see [SFTP](sftp.md)
- **Public links** -- see [Sharing](sharing.md)

## Search

Use the search bar to find files by name or content. See [Search](search.md) for details.
