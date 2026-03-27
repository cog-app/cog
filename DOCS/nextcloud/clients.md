# Compatible Clients

Cog works with NextCloud clients and standard CalDAV/CardDAV apps. Use your Cog server URL wherever the client asks for a NextCloud or WebDAV server address.

## NextCloud Desktop Client

The official NextCloud desktop sync client works with Cog for file synchronization.

1. Download from [nextcloud.com/install](https://nextcloud.com/install/)
2. Enter your Cog server URL (e.g. `https://cloud.example.com`)
3. Click "Log in" -- this opens the Login Flow in your browser
4. Grant access in the browser
5. Choose which folders to sync

## NextCloud Android

1. Install from F-Droid or Google Play
2. Enter your Cog server URL
3. Authenticate via Login Flow
4. Auto-upload photos and access files

## NextCloud iOS

1. Install from the App Store
2. Enter your Cog server URL
3. Authenticate via Login Flow

## DAVx5 (Android)

For CalDAV and CardDAV sync on Android:

1. Install DAVx5
2. Add account with your Cog server URL
3. Enter username and app password
4. DAVx5 discovers calendars and address books automatically

## Thunderbird

### Calendar

1. Go to Calendar > New Calendar > On the Network
2. Enter: `https://cloud.example.com/remote.php/dav/calendars/<username>/`
3. Enter username and app password

### Contacts (CardBook add-on)

1. Install the CardBook add-on
2. Add a new remote address book
3. Enter: `https://cloud.example.com/remote.php/dav/addressbooks/<username>/`
4. Enter credentials

## Apple Calendar & Contacts (macOS/iOS)

1. System Settings > Internet Accounts > Add Other Account
2. Choose CalDAV or CardDAV
3. Server: `cloud.example.com`
4. Username and app password

Apple clients use `/.well-known/caldav` and `/.well-known/carddav` for automatic discovery.

## GNOME Online Accounts

1. Settings > Online Accounts > NextCloud
2. Enter your Cog server URL and credentials
3. Calendar, contacts, and files sync automatically

## Floccus (Browser Bookmark Sync)

1. Install the Floccus browser extension
2. Choose "NextCloud Bookmarks" as the adapter
3. Enter your Cog server URL and credentials
4. Bookmarks sync bidirectionally

## RSS Reader Apps

Apps compatible with the NextCloud News API:

- **NextCloud News** (Android/iOS)
- **FeedMe** (Android)
- **Newsboat** (with NextCloud News plugin)

API endpoint: `https://cloud.example.com/index.php/apps/news/api/v1-2/`
