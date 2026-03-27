# Sharing

Cog provides a unified sharing system for files, folders, and plugin resources (calendars, bookmarks folders, etc.).

## Share targets

You can share with:

- **Users** -- share directly with another user on the server
- **Groups** -- share with all members of a group (including nested groups)
- **Public link** -- generate a URL that anyone can use to access the resource

## Permissions

Shares use a permission bitmask:

| Permission | Description |
|------------|-------------|
| **Read** | View the resource |
| **Write** | Modify the resource |
| **Reshare** | Share the resource with others |

Some plugins define additional permissions (e.g., Free/Busy and Schedule for calendars).

## Sharing files and folders

1. Select a file or folder in the file browser
2. Click the share icon
3. Search for users or groups, or create a public link
4. Set permissions

Sharing a folder grants access to all files and subfolders within it.

## Public links

Public links allow anyone with the URL to access the shared resource without logging in.

Options:

- **Password protection** -- require a password to access the link
- **Expiration** -- set an automatic expiry date
- **Permissions** -- read-only or read/write

Public links are accessible at `/s/<token>`.

## Shared with me

Resources shared with you appear in the relevant app. For example, shared files appear in your file browser and shared calendars appear in the calendar app.

## Plugin resources

Plugins can register their own shareable resource types. For example:

- Calendars can be shared (Calendar app)
- Bookmark folders can be shared (Bookmarks app)
- Task lists can be shared (Tasks app)
- Photo albums can be shared (Gallery app)

The sharing UI is consistent across all resource types.
