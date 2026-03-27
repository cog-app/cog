# NextCloud Compatibility

Cog implements a subset of NextCloud APIs to provide compatibility with the NextCloud ecosystem of clients and apps.

## Supported APIs

| API | Status | Used by |
|-----|--------|---------|
| WebDAV Files | Supported | Desktop/mobile sync, file managers |
| CalDAV | Supported | Calendar clients (Thunderbird, Apple Calendar, DAVx5) |
| CardDAV | Supported | Contact clients (Apple Contacts, DAVx5, Thunderbird) |
| OCS Capabilities | Supported | All NextCloud clients (server feature discovery) |
| OCS User Info | Supported | Desktop/mobile clients |
| OCS Sharing | Supported | Desktop/mobile sharing UI |
| Login Flow v2 | Supported | Desktop/mobile client authentication |
| Chunked Upload v2 | Supported | Desktop client large file uploads |
| Direct Download Tokens | Supported | Client preview generation |
| News API v1-2 | Supported | RSS reader apps |
| Bookmarks API v2 | Supported | Bookmark sync (Floccus) |
| Memories API | Supported | NextCloud Memories photo app |

## Not implemented

- Server-side encryption APIs
- Activity/notification APIs
- Talk/messaging APIs
- Deck/project management APIs
- Text/collaborative editing APIs

## Endpoints

- **Status:** `GET /status.php`
- **Capabilities:** `GET /ocs/v1.php/cloud/capabilities`
- **WebDAV:** `/remote.php/dav/files/<username>/`
- **CalDAV:** `/remote.php/dav/calendars/<username>/`
- **CardDAV:** `/remote.php/dav/addressbooks/<username>/`

See [Clients](clients.md) for setup instructions for specific apps.
