# WebDAV

Cog provides a WebDAV endpoint compatible with NextCloud clients and standard WebDAV tools.

## Endpoint

```
https://cloud.example.com/remote.php/dav/files/<username>/
```

Replace `<username>` with your Cog username.

## Authentication

Use an [app password](app-passwords.md) for WebDAV clients. App passwords are recommended over your main password for third-party client access.

Basic authentication and bearer tokens are both supported.

## Compatible clients

### NextCloud Desktop Client

1. Open the NextCloud desktop client
2. Enter your Cog server URL (e.g. `https://cloud.example.com`)
3. Follow the Login Flow to authenticate
4. The client will sync your files automatically

### NextCloud Mobile (Android/iOS)

1. Add a new account
2. Enter your Cog server URL
3. Authenticate via Login Flow

### macOS Finder

1. In Finder, press Cmd+K
2. Enter `https://cloud.example.com/remote.php/dav/files/<username>/`
3. Enter your username and app password

### Windows Explorer

1. Right-click "This PC" > "Map network drive"
2. Enter `https://cloud.example.com/remote.php/dav/files/<username>/`
3. Enter credentials when prompted

### rclone

```bash
rclone config create cog webdav \
    url=https://cloud.example.com/remote.php/dav/files/<username>/ \
    vendor=nextcloud \
    user=<username> \
    pass=<app-password>
```

### curl

```bash
# List files
curl -u username:app-password -X PROPFIND \
    https://cloud.example.com/remote.php/dav/files/username/

# Upload a file
curl -u username:app-password -T file.txt \
    https://cloud.example.com/remote.php/dav/files/username/file.txt

# Download a file
curl -u username:app-password -o file.txt \
    https://cloud.example.com/remote.php/dav/files/username/file.txt
```

## Chunked uploads

Cog supports the NextCloud v2 chunked upload protocol for large files. Compatible clients (NextCloud desktop/mobile) use this automatically.

## Direct download tokens

The API supports generating short-lived direct download tokens that allow unauthenticated file access. These are used internally by NextCloud clients for preview generation.
