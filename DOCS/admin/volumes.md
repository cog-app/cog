# Volumes

Volumes are storage backends that can be mounted into user virtual filesystems.

## Volume types

| Scheme | Description |
|--------|-------------|
| `file://` | Local filesystem directory |
| `webdav://` | Remote WebDAV server |

Each user has a default local volume created automatically. Admins can create additional volumes for shared storage.

## Creating volumes

### CLI

```bash
cog volume create shared-photos file:///mnt/photos
cog volume create remote-backup webdav://backup.example.com/dav/
```

### Web admin

Go to **Admin > Volumes** to create and manage volumes.

## System vs user volumes

- **System volumes** are created by admins and have no owner. They can be shared with any user or group.
- **User volumes** are created by individual users from **Settings > Storage** for their own use.

## Mounting volumes

Volumes must be mounted into a user's virtual filesystem before they can access the contents.

```bash
cog volume mount shared-photos alice /Photos/Shared
```

This makes the volume's contents appear at `/Photos/Shared` in Alice's file browser, WebDAV, and SFTP.

Users can also mount volumes themselves from **Settings > Storage** in the web UI.

## Sharing volumes

Volumes are shareable resources. Share a system volume with a group so all members can mount it:

1. Go to **Admin > Volumes**
2. Select the volume and click **Share**
3. Choose users or groups and set permissions (Read, Read/Write)

Each member then mounts the shared volume at their preferred location in their own VFS.

## Flat file storage

Files within volumes are stored with their original names on disk. There is no content-addressable hashing or opaque ID renaming. Admins can copy files directly into a volume's backing directory and Cog will detect them on the next directory listing.
